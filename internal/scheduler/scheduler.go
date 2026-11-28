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
	"github.com/atpanos/docktimer/internal/docker"
	"github.com/atpanos/docktimer/internal/rules"
	"github.com/robfig/cron/v3"
)

// Runner executes an action against a container or a Compose stack. Implemented
// by docker.Service.
type Runner interface {
	ExecuteTarget(ctx context.Context, action database.Action, targetType database.TargetType, ref string) ([]docker.Result, error)
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
	gen     map[int64]uint64
	seq     uint64
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
		gen:     make(map[int64]uint64),
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

	// Stop every instance before waiting for any of them: cron's Stop context
	// only completes after that instance's running jobs finish, so waiting in
	// the loop would let later timezones dispatch new actions during shutdown.
	waiters := make([]context.Context, 0, len(crons))
	for _, c := range crons {
		waiters = append(waiters, c.Stop())
	}
	stopped := make(chan struct{})
	go func() {
		for _, ctx := range waiters {
			<-ctx.Done()
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
	s.seq++
	gen := s.seq
	eid, err := c.AddFunc(r.CronExpression, func() { s.job(rule, gen) })
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
	s.gen[r.ID] = gen
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
	delete(s.gen, id)
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

// advanceNextLocked refreshes the next run for the current registration only.
// A delayed completion from an older registration must not overwrite the
// schedule of a replacement rule.
func (s *Scheduler) advanceNextLocked(r database.Rule, gen uint64) {
	if gen == 0 || s.gen[r.ID] != gen {
		return
	}
	if next, err := rules.NextRun(r.CronExpression, r.Timezone, time.Now()); err == nil {
		s.next[r.ID] = next
	}
}

func (s *Scheduler) job(r database.Rule, gen uint64) {
	targetType := r.TargetType
	if targetType == "" {
		targetType = database.TargetContainer
	}
	targetID, targetName := ruleTarget(r)

	s.mu.Lock()
	if _, busy := s.running[r.ID]; busy {
		// The dispatch still counts: move the displayed next run forward even
		// though this execution is skipped because the previous one is active.
		s.advanceNextLocked(r, gen)
		s.mu.Unlock()
		s.record(database.ExecutionLog{
			RuleID:        &r.ID,
			ContainerID:   targetID,
			ContainerName: targetName,
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
		s.advanceNextLocked(r, gen)
		s.mu.Unlock()
		s.wg.Done()
	}()

	ctx, cancel := context.WithTimeout(s.rootCtx, 10*time.Minute)
	defer cancel()

	results, err := s.runner.ExecuteTarget(ctx, r.Action, targetType, r.TargetRef)
	if len(results) == 0 {
		entry := database.ExecutionLog{
			RuleID:        &r.ID,
			ContainerID:   targetID,
			ContainerName: targetName,
			Action:        r.Action,
			Trigger:       database.TriggerSchedule,
			Status:        database.StatusSuccess,
		}
		if err != nil {
			entry.Status = database.StatusFailed
			entry.Message = err.Error()
		} else {
			entry.Message = "No matching containers."
		}
		s.record(entry)
		return
	}

	for i, res := range results {
		entry := database.ExecutionLog{
			RuleID:        &r.ID,
			ContainerID:   res.ContainerID,
			ContainerName: res.ContainerName,
			Action:        r.Action,
			Trigger:       database.TriggerSchedule,
			Status:        database.StatusSuccess,
			DurationMS:    res.Duration.Milliseconds(),
		}
		if res.Err != nil {
			entry.Status = database.StatusFailed
		}
		if targetType == database.TargetStack {
			prefix := fmt.Sprintf("stack %s (%d/%d)", r.TargetRef, i+1, len(results))
			switch {
			case res.Err != nil:
				entry.Message = prefix + ": " + res.Err.Error()
			case res.Message != "":
				entry.Message = prefix + ": " + res.Message
			default:
				entry.Message = prefix
			}
		} else if res.Message != "" {
			entry.Message = res.Message
		}
		s.record(entry)
	}
}

// ruleTarget returns the log fields used when no member container is available
// (skipped run or an unresolved stack).
func ruleTarget(r database.Rule) (id, name string) {
	if r.TargetType == database.TargetStack {
		return "stack:" + r.TargetRef, r.TargetName
	}
	return r.TargetRef, r.TargetName
}

// record stores one log entry with a fresh deadline. The action context may
// already be expired after a timeout or shutdown, which must not drop the
// outcome from history. If the rule was deleted while the job was running, the
// dangling rule reference is dropped and the entry is retried.
func (s *Scheduler) record(entry database.ExecutionLog) {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(s.rootCtx), 5*time.Second)
	defer cancel()
	ruleID := entry.RuleID
	if _, err := s.logs.Record(recordCtx, entry); err != nil {
		entry.RuleID = nil
		if _, retryErr := s.logs.Record(recordCtx, entry); retryErr != nil {
			s.log.Error("cannot record scheduled run", "rule_id", ruleID, "error", retryErr)
		}
	}
}
