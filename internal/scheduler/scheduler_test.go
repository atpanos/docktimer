package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/atpanos/docktimer/internal/database"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls int
	err   error
	last  database.Action
}

func (f *fakeRunner) Execute(ctx context.Context, action database.Action, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = action
	return f.err
}

type fakeSource struct{ rules []database.Rule }

func (f *fakeSource) ListEnabledRules(ctx context.Context) ([]database.Rule, error) {
	return f.rules, nil
}

type fakeSink struct {
	mu      sync.Mutex
	entries []database.ExecutionLog
}

func (f *fakeSink) Record(ctx context.Context, l database.ExecutionLog) (database.ExecutionLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, l)
	return l, nil
}

func (f *fakeSink) last() (database.ExecutionLog, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.entries) == 0 {
		return database.ExecutionLog{}, false
	}
	return f.entries[len(f.entries)-1], true
}

func testRule() database.Rule {
	return database.Rule{
		ID: 7, ContainerID: "abc", ContainerName: "/web",
		Action: database.ActionStop, CronExpression: "0 8 * * *",
		Timezone: "Europe/Berlin", Enabled: true,
	}
}

func newScheduler(r Runner, s RuleSource, sink LogSink) *Scheduler {
	return New(r, s, sink, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestJobRecordsSuccess(t *testing.T) {
	runner := &fakeRunner{}
	sink := &fakeSink{}
	s := newScheduler(runner, &fakeSource{}, sink)

	s.job(testRule(), 0)

	if runner.calls != 1 || runner.last != database.ActionStop {
		t.Fatalf("runner calls = %d action = %s", runner.calls, runner.last)
	}
	entry, ok := sink.last()
	if !ok || entry.Status != database.StatusSuccess {
		t.Fatalf("unexpected log entry: %+v", entry)
	}
	if entry.Trigger != database.TriggerSchedule || entry.RuleID == nil || *entry.RuleID != 7 {
		t.Errorf("missing schedule metadata: %+v", entry)
	}
}

func TestJobRecordsFailure(t *testing.T) {
	runner := &fakeRunner{err: errors.New("daemon down")}
	sink := &fakeSink{}
	s := newScheduler(runner, &fakeSource{}, sink)

	s.job(testRule(), 0)

	entry, _ := sink.last()
	if entry.Status != database.StatusFailed || entry.Message != "daemon down" {
		t.Fatalf("unexpected log entry: %+v", entry)
	}
}

func TestNextRunIsTrackedOnlyForEnabledRules(t *testing.T) {
	sink := &fakeSink{}
	s := newScheduler(&fakeRunner{}, &fakeSource{}, sink)

	r := testRule()
	s.Upsert(r)
	next, ok := s.Next(r.ID)
	if !ok {
		t.Fatal("no next run tracked")
	}
	if next.Before(time.Now().Add(-time.Minute)) {
		t.Errorf("next run in the past: %v", next)
	}

	r.Enabled = false
	s.Upsert(r)
	if _, ok := s.Next(r.ID); ok {
		t.Error("disabled rule still has a next run")
	}
	s.Remove(r.ID)
}

func TestUpsertWithInvalidTimezoneDoesNotPanic(t *testing.T) {
	sink := &fakeSink{}
	s := newScheduler(&fakeRunner{}, &fakeSource{}, sink)
	r := testRule()
	r.Timezone = "Nowhere/Invalid"
	s.Upsert(r)
	if _, ok := s.Next(r.ID); ok {
		t.Error("invalid rule must not be scheduled")
	}
}

func TestStartLoadsRules(t *testing.T) {
	r := testRule()
	sink := &fakeSink{}
	s := newScheduler(&fakeRunner{}, &fakeSource{rules: []database.Rule{r}}, sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	next, ok := s.Next(r.ID)
	if !ok {
		t.Fatal("rule not scheduled on start")
	}
	_ = next
	if err := s.Start(ctx); err == nil {
		t.Error("double start must fail")
	}
	s.Stop()
}

// waitForActiveEntry waits until the cron entry of a rule has a next run. An
// entry in a cron instance that was never started keeps a zero Next forever, so
// this also proves that the instance is actually running.
func waitForActiveEntry(t *testing.T, s *Scheduler, id int64) time.Time {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.RLock()
		c := s.crons[s.ruleTZ[id]]
		eid, ok := s.entries[id]
		s.mu.RUnlock()
		if ok && c != nil {
			if next := c.Entry(eid).Next; !next.IsZero() {
				return next
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("cron entry for rule %d never became active", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUpsertAfterStartStartsNewTimezoneCron(t *testing.T) {
	sink := &fakeSink{}
	s := newScheduler(&fakeRunner{}, &fakeSource{}, sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	r := testRule()
	s.Upsert(r)

	next := waitForActiveEntry(t, s, r.ID)
	if next.Before(time.Now().Add(-time.Minute)) {
		t.Errorf("next run in the past: %v", next)
	}
}

func TestUpsertAfterStartStartsCronForAdditionalTimezone(t *testing.T) {
	existing := testRule()
	s := newScheduler(&fakeRunner{}, &fakeSource{rules: []database.Rule{existing}}, &fakeSink{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	added := testRule()
	added.ID = 8
	added.Timezone = "Asia/Tokyo"
	s.Upsert(added)

	waitForActiveEntry(t, s, existing.ID)
	waitForActiveEntry(t, s, added.ID)
}

func TestRemoveLastRuleDropsTimezoneCron(t *testing.T) {
	s := newScheduler(&fakeRunner{}, &fakeSource{}, &fakeSink{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	r := testRule()
	s.Upsert(r)
	waitForActiveEntry(t, s, r.ID)

	s.Remove(r.ID)

	s.mu.RLock()
	_, exists := s.crons[r.Timezone]
	s.mu.RUnlock()
	if exists {
		t.Errorf("cron instance for %q kept although no rule uses it", r.Timezone)
	}
}

func TestJobIsSkippedWhilePreviousRunIsActive(t *testing.T) {
	// Simulate a still-running job by occupying the running marker.
	sink := &fakeSink{}
	runner := &fakeRunner{}
	s := newScheduler(runner, &fakeSource{}, sink)
	rule := testRule()

	s.mu.Lock()
	s.running[rule.ID] = struct{}{}
	s.mu.Unlock()

	s.job(rule, 0)

	if runner.calls != 0 {
		t.Error("job ran although a previous run was active")
	}
	entry, _ := sink.last()
	if entry.Status != database.StatusFailed || entry.DurationMS != 0 {
		t.Errorf("expected skipped-run log, got %+v", entry)
	}
	delete(s.running, rule.ID)
}
