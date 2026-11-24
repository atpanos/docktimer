// Package scheduler runs active rules with robfig/cron. Each distinct timezone
// gets its own cron instance because robfig/cron only supports one location per
// instance.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/rules"
	"github.com/robfig/cron/v3"
)

// Runner executes a container action. Implemented by docker.Service.
type Runner interface {
	Execute(ctx context.Context, action database.Action, id string) error
}

// RuleSource loads active rules. Implemented by database.Store.
type RuleSource interface {
	ListEnabledRules(ctx context.Context) ([]database.Rule, error)
}

// LogSink stores execution logs. Implemented by logging.Service.
type LogSink interface {
	Record(ctx context.Context, l database.ExecutionLog) (database.ExecutionLog, error)
}

// Scheduler owns the cron instances.
type Scheduler struct {
	runner Runner
	source RuleSource
	logs   LogSink
	log    *slog.Logger

	mu      sync.RWMutex
	crons   map[string]*cron.Cron
	entries map[int64]cron.EntryID
	ruleTZ  map[int64]string
	next    map[int64]time.Time
	running map[int64]struct{}

	rootCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started bool
}

// New creates a scheduler.
func New(runner Runner, source RuleSource, logs LogSink, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		runner:  runner,
		source:  source,
		logs:    logs,
		log:     log,
		crons:   make(map[string]*cron.Cron),
		entries: make(map[int64]cron.EntryID),
		ruleTZ:  make(map[int64]string),
		next:    make(map[int64]time.Time),
		running: make(map[int64]struct{}),
		rootCtx: ctx,
		cancel:  cancel,
	}
}

// Start loads all enabled rules and starts the cron instances.
func (s *Scheduler) Start(ctx context.Context) error {
	rulesList, err := s.source.ListEnabledRules(ctx)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("scheduler already started")
	}
	for _, r := range rulesList {
		if err := s.addLocked(r); err != nil {
			s.log.Error("cannot schedule rule", "rule_id", r.ID, "error", err)
			continue
		}
	}
	for tz, c := range s.crons {
		c.Start()
		s.log.Info("scheduler started", "timezone", tz)
	}
	s.started = true
	return nil
}

// Stop stops all cron instances and waits for running jobs.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		s.cancel()
		return
	}
	crons := make([]*cron.Cron, 0, len(s.crons))
	for _, c := range s.crons {
		crons = append(crons, c)
	}
	s.started = false
	s.mu.Unlock()

	stopped := make(chan struct{})
	go func() {
		for _, c := range crons {
			<-c.Stop().Done()
		}
		s.wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		s.log.Warn("scheduler shutdown timed out")
	}
	s.cancel()
}

// Upsert adds, updates or removes a rule from the schedule. Disabled rules are
// removed from the schedule.
func (s *Scheduler) Upsert(r database.Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(r.ID)
	if !r.Enabled {
		return
	}
	if err := s.addLocked(r); err != nil {
		s.log.Error("cannot schedule rule", "rule_id", r.ID, "error", err)
	}
}

// Remove deletes a rule from the schedule.
func (s *Scheduler) Remove(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(id)
}

// Next returns the next planned run for a rule.
func (s *Scheduler) Next(id int64) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.next[id]
	return t, ok
}

func (s *Scheduler) addLocked(r database.Rule) error {
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return err
	}
	next, err := rules.NextRun(r.CronExpression, r.Timezone, time.Now())
	if err != nil {
		return err
	}

	c, exists := s.crons[r.Timezone]
	if !exists {
		c = cron.New(cron.WithLocation(loc))
		s.crons[r.Timezone] = c
	}
	rule := r
	eid, err := c.AddFunc(r.CronExpression, func() { s.job(rule) })
	if err != nil {
		if !exists {
			delete(s.crons, r.Timezone)
		}
		return err
	}
	// A cron instance created after Start must be started as well. Otherwise a
	// rule added at runtime would be registered but never executed, even though
	// Next() reports a future run.
	if !exists && s.started {
		c.Start()
	}
	s.entries[r.ID] = eid
	s.ruleTZ[r.ID] = r.Timezone
	s.next[r.ID] = next
	return nil
}

func (s *Scheduler) removeLocked(id int64) {
	eid, ok := s.entries[id]
	tz := s.ruleTZ[id]
	if ok {
		if c, found := s.crons[tz]; found {
			c.Remove(eid)
		}
	}
	delete(s.entries, id)
	delete(s.ruleTZ, id)
	delete(s.next, id)

	if tz == "" {
		return
	}
	// Stop and drop a cron instance once its last rule is gone. Otherwise every
	// time zone that was ever used would keep a sleeping scheduler goroutine.
	for _, otherTZ := range s.ruleTZ {
		if otherTZ == tz {
			return
		}
	}
	if c, found := s.crons[tz]; found {
		delete(s.crons, tz)
		c.Stop()
	}
}

func (s *Scheduler) job(r database.Rule) {
	s.mu.Lock()
	if _, busy := s.running[r.ID]; busy {
		s.mu.Unlock()
		_, _ = s.logs.Record(s.rootCtx, database.ExecutionLog{
			RuleID:        &r.ID,
			ContainerID:   r.ContainerID,
			ContainerName: r.ContainerName,
			Action:        r.Action,
			Trigger:       database.TriggerSchedule,
			Status:        database.StatusFailed,
			Message:       "Previous run was still active; this run was skipped.",
		})
		return
	}
	s.running[r.ID] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()

	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("scheduled job panicked", "rule_id", r.ID, "panic", rec)
		}
		s.mu.Lock()
		delete(s.running, r.ID)
		if next, err := rules.NextRun(r.CronExpression, r.Timezone, time.Now()); err == nil {
			if _, exists := s.entries[r.ID]; exists {
				s.next[r.ID] = next
			}
		}
		s.mu.Unlock()
		s.wg.Done()
	}()

	ctx, cancel := context.WithTimeout(s.rootCtx, 10*time.Minute)
	defer cancel()

	start := time.Now()
	err := s.runner.Execute(ctx, r.Action, r.ContainerID)
	elapsed := time.Since(start).Milliseconds()

	entry := database.ExecutionLog{
		RuleID:        &r.ID,
		ContainerID:   r.ContainerID,
		ContainerName: r.ContainerName,
		Action:        r.Action,
		Trigger:       database.TriggerSchedule,
		Status:        database.StatusSuccess,
		DurationMS:    elapsed,
	}
	if err != nil {
		entry.Status = database.StatusFailed
		entry.Message = err.Error()
	}
	if _, recErr := s.logs.Record(ctx, entry); recErr != nil {
		s.log.Error("cannot record scheduled run", "rule_id", r.ID, "error", recErr)
	}
}
