package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const ruleColumns = `id, container_id, container_name, action, cron_expression, timezone,
	enabled, created_by, created_at, updated_at`

func scanRule(row interface{ Scan(...any) error }) (Rule, error) {
	var r Rule
	var action, created, updated string
	var enabled int
	var createdBy sql.NullInt64
	err := row.Scan(&r.ID, &r.ContainerID, &r.ContainerName, &action, &r.CronExpression,
		&r.Timezone, &enabled, &createdBy, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Rule{}, ErrNotFound
		}
		return Rule{}, err
	}
	r.Action = Action(action)
	r.Enabled = enabled == 1
	r.CreatedBy = nullInt64(createdBy)
	r.CreatedAt = parseTime(created)
	r.UpdatedAt = parseTime(updated)
	return r, nil
}

// CreateRule inserts a rule and returns it with its ID and timestamps.
func (s *Store) CreateRule(ctx context.Context, r Rule) (Rule, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO container_rules
		 (container_id, container_name, action, cron_expression, timezone, enabled, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ContainerID, r.ContainerName, r.Action, r.CronExpression, r.Timezone,
		boolInt(r.Enabled), argInt64(r.CreatedBy), formatTime(now), formatTime(now))
	if err != nil {
		return Rule{}, fmt.Errorf("create rule: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Rule{}, fmt.Errorf("create rule id: %w", err)
	}
	r.ID = id
	r.CreatedAt = now
	r.UpdatedAt = now
	return r, nil
}

// UpdateRule replaces all mutable fields of a rule.
func (s *Store) UpdateRule(ctx context.Context, r Rule) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE container_rules
		 SET container_id = ?, container_name = ?, action = ?, cron_expression = ?, timezone = ?,
		     enabled = ?, updated_at = ?
		 WHERE id = ?`,
		r.ContainerID, r.ContainerName, r.Action, r.CronExpression, r.Timezone,
		boolInt(r.Enabled), formatTime(time.Now()), r.ID)
	if err != nil {
		return fmt.Errorf("update rule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRule removes a rule. Execution logs keep rule_id = NULL.
func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM container_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete rule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RuleByID returns one rule.
func (s *Store) RuleByID(ctx context.Context, id int64) (Rule, error) {
	r, err := scanRule(s.db.QueryRowContext(ctx, `SELECT `+ruleColumns+` FROM container_rules WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Rule{}, ErrNotFound
		}
		return Rule{}, fmt.Errorf("rule by id: %w", err)
	}
	return r, nil
}

// ListRules returns all rules ordered by container and action.
func (s *Store) ListRules(ctx context.Context) ([]Rule, error) {
	return s.queryRules(ctx, `SELECT `+ruleColumns+` FROM container_rules ORDER BY container_name, action, id`)
}

// ListEnabledRules returns all active rules.
func (s *Store) ListEnabledRules(ctx context.Context) ([]Rule, error) {
	return s.queryRules(ctx, `SELECT `+ruleColumns+` FROM container_rules WHERE enabled = 1 ORDER BY id`)
}

func (s *Store) queryRules(ctx context.Context, query string) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	defer rows.Close()

	var rules []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan rule: %w", err)
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// SetRuleEnabled toggles a rule.
func (s *Store) SetRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE container_rules SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolInt(enabled), formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("set rule enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
