package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/docker"
	"github.com/atpanos/docktimer/internal/logging"
)

// stackAPI is a minimal Docker API double that tracks container state.
type stackAPI struct {
	containers []docker.Container
	calls      []string
}

func (a *stackAPI) List(ctx context.Context) ([]docker.Container, error) {
	return append([]docker.Container(nil), a.containers...), nil
}

func (a *stackAPI) Start(ctx context.Context, id string) error {
	a.calls = append(a.calls, "start:"+id)
	a.setState(id, "running")
	return nil
}

func (a *stackAPI) Stop(ctx context.Context, id string, timeoutSeconds int) error {
	a.calls = append(a.calls, "stop:"+id)
	a.setState(id, "exited")
	return nil
}

func (a *stackAPI) Restart(ctx context.Context, id string, timeoutSeconds int) error {
	a.calls = append(a.calls, "restart:"+id)
	a.setState(id, "running")
	return nil
}

func (a *stackAPI) Ping(ctx context.Context) error { return nil }

func (a *stackAPI) setState(id, state string) {
	for i := range a.containers {
		if a.containers[i].ID == id {
			a.containers[i].State = state
		}
	}
}

func TestStackRuleRunIntegration(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := database.Open(ctx, filepath.Join(t.TempDir(), "docktimer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	api := &stackAPI{containers: []docker.Container{
		{ID: "db-id", Name: "/media-db-1", State: "running", Project: "media", Service: "db"},
		{ID: "web-id", Name: "/media-web-1", State: "running", Project: "media", Service: "web"},
		{ID: "job-id", Name: "/media-job-1", State: "exited", Project: "media", Service: "job", OneOff: true},
	}}
	svc := docker.NewService(api, 30*time.Second, nil, nil, "", log)
	logSvc := logging.NewService(store, 100, 0, log)
	sched := New(svc, store, logSvc, log)

	rule, err := store.CreateRule(ctx, database.Rule{
		TargetType: database.TargetStack, TargetRef: "media", TargetName: "media",
		Action: database.ActionStop, CronExpression: "0 8 * * *",
		Timezone: "Europe/Berlin", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	sched.job(rule, 0)

	wantStop := []string{"stop:web-id", "stop:db-id"}
	if strings.Join(api.calls, ",") != strings.Join(wantStop, ",") {
		t.Fatalf("calls = %v, want %v", api.calls, wantStop)
	}
	assertStackLogs(t, ctx, store, "stack media (1/2)", "stack media (2/2)")

	// Start the stack again; the stopped members are started in service order.
	api.calls = nil
	rule.Action = database.ActionStart
	if err := store.UpdateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	sched.job(rule, 0)
	wantStart := []string{"start:db-id", "start:web-id"}
	if strings.Join(api.calls, ",") != strings.Join(wantStart, ",") {
		t.Fatalf("calls = %v, want %v", api.calls, wantStart)
	}
	for _, call := range api.calls {
		if strings.Contains(call, "job-id") {
			t.Errorf("one-off container was part of the stack: %v", api.calls)
		}
	}

	// An unknown stack produces one clear failure instead of a panic.
	api.calls = nil
	missing, err := store.CreateRule(ctx, database.Rule{
		TargetType: database.TargetStack, TargetRef: "gone", TargetName: "gone",
		Action: database.ActionStop, CronExpression: "0 8 * * *",
		Timezone: "Europe/Berlin", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sched.job(missing, 0)
	logs, err := store.RecentLogs(ctx, 1)
	if err != nil || len(logs) != 1 {
		t.Fatalf("recent logs: %v %d", err, len(logs))
	}
	if logs[0].Status != database.StatusFailed || logs[0].ContainerID != "stack:gone" {
		t.Errorf("unexpected missing-stack log: %+v", logs[0])
	}
}

func assertStackLogs(t *testing.T, ctx context.Context, store *database.Store, want ...string) {
	t.Helper()
	logs, err := store.RecentLogs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) < len(want) {
		t.Fatalf("logs = %d, want at least %d: %+v", len(logs), len(want), logs)
	}
	got := map[string]bool{}
	for _, l := range logs {
		got[l.Message] = true
	}
	for _, wantMsg := range want {
		if !got[wantMsg] {
			t.Errorf("missing log message %q in %+v", wantMsg, logs)
		}
	}
}
