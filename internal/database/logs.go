package database

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const logColumns = `id, rule_id, container_id, container_name, action, trigger, status,
	message, duration_ms, created_at`

// InsertLog stores an execution log and enforces the retention limits in the
// same transaction. A globalCap <= 0 disables pruning.
func (s *Store) InsertLog(ctx context.Context, l ExecutionLog, globalCap, perContainerCap int) (ExecutionLog, error) {
	now := time.Now().UTC()
	if l.CreatedAt.IsZero() {
		l.CreatedAt = now
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionLog{}, fmt.Errorf("begin log insert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO execution_logs
		 (rule_id, container_id, container_name, action, trigger, status, message, duration_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		argInt64(l.RuleID), l.ContainerID, l.ContainerName, l.Action, l.Trigger,
		l.Status, l.Message, l.DurationMS, formatTime(l.CreatedAt))
	if err != nil {
		return ExecutionLog{}, fmt.Errorf("insert log: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ExecutionLog{}, fmt.Errorf("insert log id: %w", err)
	}
	l.ID = id

	if globalCap > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM execution_logs WHERE id NOT IN (
				SELECT id FROM execution_logs ORDER BY id DESC LIMIT ?
			)`, globalCap); err != nil {
			return ExecutionLog{}, fmt.Errorf("prune logs: %w", err)
		}
	}
	if perContainerCap > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM execution_logs
			 WHERE container_id = ? AND id NOT IN (
				SELECT id FROM execution_logs WHERE container_id = ? ORDER BY id DESC LIMIT ?
			 )`, l.ContainerID, l.ContainerID, perContainerCap); err != nil {
			return ExecutionLog{}, fmt.Errorf("prune container logs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ExecutionLog{}, fmt.Errorf("commit log insert: %w", err)
	}
	return l, nil
}

// LogsPage returns one page of logs, newest first.
func (s *Store) LogsPage(ctx context.Context, f LogFilter) ([]ExecutionLog, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	var where []string
	var args []any
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Trigger != "" {
		where = append(where, "trigger = ?")
		args = append(args, f.Trigger)
	}
	if f.ContainerID != "" {
		where = append(where, "container_id = ?")
		args = append(args, f.ContainerID)
	}
	query := `SELECT ` + logColumns + ` FROM execution_logs`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	return s.queryLogs(ctx, query, args...)
}

// RecentLogs returns the newest n logs.
func (s *Store) RecentLogs(ctx context.Context, n int) ([]ExecutionLog, error) {
	return s.queryLogs(ctx, `SELECT `+logColumns+` FROM execution_logs ORDER BY id DESC LIMIT ?`, n)
}

// RecentFailures returns the newest n failed logs.
func (s *Store) RecentFailures(ctx context.Context, n int) ([]ExecutionLog, error) {
	return s.queryLogs(ctx,
		`SELECT `+logColumns+` FROM execution_logs WHERE status = ? ORDER BY id DESC LIMIT ?`,
		StatusFailed, n)
}

func (s *Store) queryLogs(ctx context.Context, query string, args ...any) ([]ExecutionLog, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query logs: %w", err)
	}
	defer rows.Close()

	var logs []ExecutionLog
	for rows.Next() {
		l, err := scanLog(rows)
		if err != nil {
			return nil, fmt.Errorf("scan log: %w", err)
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func scanLog(row interface{ Scan(...any) error }) (ExecutionLog, error) {
	var l ExecutionLog
	var ruleID *int64
	var action, trigger, status, created string
	err := row.Scan(&l.ID, &ruleID, &l.ContainerID, &l.ContainerName, &action, &trigger,
		&status, &l.Message, &l.DurationMS, &created)
	if err != nil {
		return ExecutionLog{}, err
	}
	l.RuleID = ruleID
	l.Action = Action(action)
	l.Trigger = Trigger(trigger)
	l.Status = Status(status)
	l.CreatedAt = parseTime(created)
	return l, nil
}

// CountLogs returns the total number of stored logs.
func (s *Store) CountLogs(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM execution_logs`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count logs: %w", err)
	}
	return n, nil
}

// PurgeLogs deletes all execution logs.
func (s *Store) PurgeLogs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM execution_logs`)
	if err != nil {
		return 0, fmt.Errorf("purge logs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
