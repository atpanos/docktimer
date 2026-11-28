package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/atpanos/docktimer/internal/database"
	"github.com/moby/moby/api/types/container"
)

func composeContainer(id, name, project, service, state string) Container {
	return Container{
		ID: id, Name: "/" + name, State: state, Project: project, Service: service,
	}
}

func TestFromSummaryReadsComposeLabels(t *testing.T) {
	got := fromSummary(container.Summary{
		ID:      "abc123456789",
		Names:   []string{"/media-web-1"},
		Image:   "nginx:alpine",
		State:   "running",
		Status:  "Up 2 hours",
		Created: 123,
		Labels: map[string]string{
			composeProjectLabel: "media",
			composeServiceLabel: "web",
			composeOneOffLabel:  "True",
		},
	})
	if got.Project != "media" || got.Service != "web" || !got.OneOff {
		t.Fatalf("compose metadata not parsed: %+v", got)
	}

	plain := fromSummary(container.Summary{ID: "def", Names: []string{"/plain"}})
	if plain.Project != "" || plain.Service != "" || plain.OneOff {
		t.Fatalf("plain container got compose metadata: %+v", plain)
	}
}

func TestGroupStacksExcludesOneOffAndSortsByService(t *testing.T) {
	containers := []Container{
		composeContainer("w", "web", "media", "web", "running"),
		composeContainer("d", "db", "media", "db", "exited"),
		composeContainer("j", "job", "media", "job", "exited"),
		composeContainer("x", "plain", "", "", "running"),
		composeContainer("b", "cache", "backup", "cache", "running"),
	}
	containers[2].OneOff = true

	stacks := GroupStacks(containers)
	if len(stacks) != 2 {
		t.Fatalf("stacks = %d, want 2: %+v", len(stacks), stacks)
	}
	if stacks[0].Project != "backup" || stacks[1].Project != "media" {
		t.Fatalf("unexpected stack order: %+v", stacks)
	}
	media := stacks[1]
	if media.TotalCount() != 2 || media.RunningCount() != 1 {
		t.Errorf("counts = %d/%d", media.RunningCount(), media.TotalCount())
	}
	if len(media.Containers) != 2 || media.Containers[0].Service != "db" || media.Containers[1].Service != "web" {
		t.Errorf("members not sorted by service: %+v", media.Containers)
	}
}

func TestScopeMatchesProjectName(t *testing.T) {
	api := &fakeAPI{}
	svc := NewService(api, 30*time.Second, nil, []string{"media"}, "", testLogger())
	if svc.Allowed(Container{ID: "a", Name: "/web", Project: "media"}) {
		t.Error("denylisted project member accepted")
	}
	if !svc.Allowed(Container{ID: "b", Name: "/other", Project: "other"}) {
		t.Error("unrelated project rejected")
	}

	svc = NewService(api, 30*time.Second, []string{"media"}, nil, "", testLogger())
	if !svc.Allowed(Container{ID: "a", Name: "/web", Project: "media"}) {
		t.Error("allowlisted project member rejected")
	}
	if svc.Allowed(Container{ID: "b", Name: "/other", Project: "other"}) {
		t.Error("unrelated project accepted")
	}

	// Self-protection still wins over a matching project.
	svc = NewService(api, 30*time.Second, nil, nil, "aaaa1111", testLogger())
	if svc.Allowed(Container{ID: "aaaa1111", Name: "/docktimer", Project: "media"}) {
		t.Error("self container became controllable through its project")
	}
}

func TestStacksAndFindStack(t *testing.T) {
	api := &fakeAPI{containers: []Container{
		composeContainer("w", "web", "media", "web", "running"),
		composeContainer("d", "db", "media", "db", "exited"),
	}}
	svc := newTestService(api)

	stacks, err := svc.Stacks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || !stacks[0].Actionable() {
		t.Fatalf("unexpected stacks: %+v", stacks)
	}

	stack, err := svc.FindStack(context.Background(), "media")
	if err != nil {
		t.Fatal(err)
	}
	if stack.Project != "media" || stack.TotalCount() != 2 {
		t.Errorf("unexpected stack: %+v", stack)
	}

	if _, err := svc.FindStack(context.Background(), "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestExecuteTargetStackStartSkipsRunningAndSorts(t *testing.T) {
	api := &fakeAPI{containers: []Container{
		composeContainer("web", "web", "media", "web", "running"),
		composeContainer("db", "db", "media", "db", "exited"),
		composeContainer("cache", "cache", "media", "cache", "running"),
	}}
	svc := newTestService(api)

	results, err := svc.ExecuteTarget(context.Background(), database.ActionStart, database.TargetStack, "media")
	if err != nil {
		t.Fatal(err)
	}
	if len(api.started) != 1 || api.started[0] != "db" {
		t.Fatalf("started = %v, want [db]", api.started)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0].ContainerID != "cache" || results[0].Message != "already running" {
		t.Errorf("unexpected first result: %+v", results[0])
	}
	if results[1].ContainerID != "db" || results[1].Err != nil {
		t.Errorf("unexpected second result: %+v", results[1])
	}
	if results[2].ContainerID != "web" || results[2].Message != "already running" {
		t.Errorf("unexpected third result: %+v", results[2])
	}
}

func TestExecuteTargetStackStopUsesReverseServiceOrder(t *testing.T) {
	api := &fakeAPI{containers: []Container{
		composeContainer("web", "web", "media", "web", "running"),
		composeContainer("db", "db", "media", "db", "running"),
		composeContainer("cache", "cache", "media", "cache", "running"),
	}}
	svc := newTestService(api)

	results, err := svc.ExecuteTarget(context.Background(), database.ActionStop, database.TargetStack, "media")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"web", "db", "cache"}
	if len(api.stopped) != len(want) {
		t.Fatalf("stopped = %v", api.stopped)
	}
	for i := range want {
		if api.stopped[i] != want[i] {
			t.Fatalf("stopped = %v, want %v", api.stopped, want)
		}
		if results[i].ContainerID != want[i] {
			t.Errorf("result %d = %s, want %s", i, results[i].ContainerID, want[i])
		}
	}
}

func TestExecuteTargetStackRestartSkipsNothing(t *testing.T) {
	api := &fakeAPI{containers: []Container{
		composeContainer("web", "web", "media", "web", "exited"),
		composeContainer("db", "db", "media", "db", "running"),
	}}
	svc := newTestService(api)

	_, err := svc.ExecuteTarget(context.Background(), database.ActionRestart, database.TargetStack, "media")
	if err != nil {
		t.Fatal(err)
	}
	if len(api.restarted) != 2 || api.restarted[0] != "db" || api.restarted[1] != "web" {
		t.Fatalf("restarted = %v", api.restarted)
	}
}

func TestExecuteTargetStackBestEffort(t *testing.T) {
	api := &fakeAPI{
		containers: []Container{
			composeContainer("web", "web", "media", "web", "exited"),
			composeContainer("db", "db", "media", "db", "exited"),
		},
		actionErr: errors.New("daemon hiccup"),
	}
	svc := newTestService(api)

	results, err := svc.ExecuteTarget(context.Background(), database.ActionStart, database.TargetStack, "media")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0].Err == nil || results[1].Err == nil {
		t.Errorf("expected per-member failures: %+v", results)
	}
}

func TestExecuteTargetStackStopDoesNotSkipPausedOrRestarting(t *testing.T) {
	api := &fakeAPI{containers: []Container{
		composeContainer("paused", "paused", "media", "paused", "paused"),
		composeContainer("restarting", "restarting", "media", "restarting", "restarting"),
		composeContainer("exited", "exited", "media", "exited", "exited"),
	}}
	svc := newTestService(api)

	results, err := svc.ExecuteTarget(context.Background(), database.ActionStop, database.TargetStack, "media")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"restarting", "paused"}
	if len(api.stopped) != len(want) || api.stopped[0] != want[0] || api.stopped[1] != want[1] {
		t.Fatalf("stopped = %v, want %v", api.stopped, want)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d", len(results))
	}
	last := results[2]
	if last.ContainerID != "exited" || last.Message != "already stopped" {
		t.Errorf("exited member not skipped: %+v", last)
	}
}

func TestExecuteTargetStackStartDoesNotStartPaused(t *testing.T) {
	api := &fakeAPI{containers: []Container{
		composeContainer("paused", "paused", "media", "paused", "paused"),
		composeContainer("exited", "exited", "media", "exited", "exited"),
	}}
	svc := newTestService(api)

	results, err := svc.ExecuteTarget(context.Background(), database.ActionStart, database.TargetStack, "media")
	if err != nil {
		t.Fatal(err)
	}
	if len(api.started) != 1 || api.started[0] != "exited" {
		t.Fatalf("started = %v, want [exited]", api.started)
	}
	if len(results) != 2 || results[0].ContainerID != "exited" {
		t.Fatalf("unexpected results: %+v", results)
	}
	if results[1].ContainerID != "paused" || results[1].Message != "already running" {
		t.Errorf("paused member not skipped: %+v", results[1])
	}
}

func TestExecuteTargetStackReportsLockedMembersAsSkipped(t *testing.T) {
	api := &fakeAPI{containers: []Container{
		composeContainer("web", "web", "media", "web", "running"),
		composeContainer("self", "docktimer", "media", "docktimer", "running"),
	}}
	svc := NewService(api, 30*time.Second, nil, []string{"docktimer"}, "", testLogger())

	results, err := svc.ExecuteTarget(context.Background(), database.ActionStop, database.TargetStack, "media")
	if err != nil {
		t.Fatal(err)
	}
	if len(api.stopped) != 1 || api.stopped[0] != "web" {
		t.Fatalf("stopped = %v, want [web]", api.stopped)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0].ContainerID != "web" || results[0].Err != nil {
		t.Errorf("unexpected first result: %+v", results[0])
	}
	if results[1].ContainerID != "self" || results[1].Message == "" || results[1].Err != nil {
		t.Errorf("locked member not reported as skipped: %+v", results[1])
	}
}

func TestExecuteTargetStackErrors(t *testing.T) {
	api := &fakeAPI{}
	svc := newTestService(api)
	if _, err := svc.ExecuteTarget(context.Background(), database.ActionStart, database.TargetStack, "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown stack: %v", err)
	}

	api.containers = []Container{
		composeContainer("self", "docktimer", "media", "docktimer", "running"),
	}
	svc = NewService(api, 30*time.Second, nil, []string{"docktimer"}, "", testLogger())
	results, err := svc.ExecuteTarget(context.Background(), database.ActionStart, database.TargetStack, "media")
	if err != nil {
		t.Fatalf("fully locked stack must not be an unresolved-target error: %v", err)
	}
	if len(results) != 1 || !errors.Is(results[0].Err, ErrNotAllowed) {
		t.Errorf("locked stack members not reported: %+v", results)
	}
}
