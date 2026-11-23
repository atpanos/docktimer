// Package logging stores execution logs and enforces retention.
package logging

import (
	"context"
	"log/slog"
	"strings"

	"github.com/atpanos/docktimer/internal/database"
)

// Service writes and queries execution logs.
type Service struct {
	store     *database.Store
	retention int
	perCap    int
	log       *slog.Logger
}

// NewService creates the logging service. retention is the global cap; perCap
// is the optional per-container cap (0 disables it).
func NewService(store *database.Store, retention, perCap int, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, retention: retention, perCap: perCap, log: log}
}

// Record normalizes and stores one execution log entry.
func (s *Service) Record(ctx context.Context, l database.ExecutionLog) (database.ExecutionLog, error) {
	l.Message = sanitize(l.Message)
	if l.Trigger == "" {
		l.Trigger = database.TriggerManual
	}
	if l.Status == "" {
		l.Status = database.StatusFailed
	}
	stored, err := s.store.InsertLog(ctx, l, s.retention, s.perCap)
	if err != nil {
		s.log.Error("failed to write execution log", "error", err,
			"container_id", l.ContainerID, "action", l.Action, "status", l.Status)
		return l, err
	}
	return stored, nil
}

// Recent returns the newest logs.
func (s *Service) Recent(ctx context.Context, n int) ([]database.ExecutionLog, error) {
	return s.store.RecentLogs(ctx, n)
}

// Failures returns the newest failed logs.
func (s *Service) Failures(ctx context.Context, n int) ([]database.ExecutionLog, error) {
	return s.store.RecentFailures(ctx, n)
}

// Page returns a filtered page.
func (s *Service) Page(ctx context.Context, f database.LogFilter) ([]database.ExecutionLog, error) {
	return s.store.LogsPage(ctx, f)
}

// Count returns the total number of logs.
func (s *Service) Count(ctx context.Context) (int64, error) {
	return s.store.CountLogs(ctx)
}

// Purge deletes all logs.
func (s *Service) Purge(ctx context.Context) (int64, error) {
	return s.store.PurgeLogs(ctx)
}

func sanitize(msg string) string {
	msg = strings.TrimSpace(strings.ReplaceAll(msg, "\r", " "))
	msg = strings.ReplaceAll(msg, "\n", " ")
	if len(msg) > 1000 {
		msg = msg[:1000] + "…"
	}
	return msg
}
