package docker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeAPI struct {
	containers []Container
	listErr    error
	actionErr  error
	pings      int
	lastStop   int
	started    []string
	stopped    []string
	restarted  []string
}

func (f *fakeAPI) List(ctx context.Context) ([]Container, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]Container(nil), f.containers...), nil
}

func (f *fakeAPI) Start(ctx context.Context, id string) error {
	f.started = append(f.started, id)
	return f.actionErr
}

func (f *fakeAPI) Stop(ctx context.Context, id string, timeoutSeconds int) error {
	f.stopped = append(f.stopped, id)
	f.lastStop = timeoutSeconds
	return f.actionErr
}

func (f *fakeAPI) Restart(ctx context.Context, id string, timeoutSeconds int) error {
	f.restarted = append(f.restarted, id)
	f.lastStop = timeoutSeconds
	return f.actionErr
}

func (f *fakeAPI) Ping(ctx context.Context) error {
	f.pings++
	return f.actionErr
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestService(api API) *Service {
	return NewService(api, 30*time.Second, nil, nil, "", testLogger())
}

func TestListSortsRunningFirstAndMarksScope(t *testing.T) {
	api := &fakeAPI{containers: []Container{
		{ID: "b", Name: "/zebra", State: "exited"},
		{ID: "a", Name: "/alpha", State: "running"},
	}}
	svc := newTestService(api)
	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Name != "/alpha" || !list[0].Controllable {
		t.Errorf("running container not first/controllable: %+v", list[0])
	}
	if list[1].Name != "/zebra" {
		t.Errorf("unexpected order: %+v", list)
	}
}

func TestScopeAllowAndDeny(t *testing.T) {
	api := &fakeAPI{containers: []Container{
		{ID: "aaaa1111bbbb", Name: "/web", State: "running"},
		{ID: "cccc2222dddd", Name: "/db", State: "running"},
		{ID: "eeee3333ffff", Name: "/docktimer", State: "running"},
	}}
	svc := NewService(api, 30*time.Second, []string{"web", "db", "docktimer"}, []string{"docktimer"}, "", testLogger())
	list, _ := svc.List(context.Background())
	for _, c := range list {
		want := c.Name != "/docktimer"
		if c.Controllable != want {
			t.Errorf("%s controllable=%v, want %v", c.Name, c.Controllable, want)
		}
	}

	// Allowlist only.
	svc = NewService(api, 30*time.Second, []string{"web"}, nil, "", testLogger())
	if !svc.Allowed(Container{ID: "aaaa1111", Name: "/web"}) {
		t.Error("allowlisted container rejected")
	}
	if svc.Allowed(Container{ID: "cccc2222", Name: "/db"}) {
		t.Error("non-allowlisted container accepted")
	}

	// Self protection by container ID prefix.
	svc = NewService(api, 30*time.Second, nil, nil, "aaaa1111bbbb", testLogger())
	if svc.Allowed(Container{ID: "aaaa1111bbbb", Name: "/docktimer"}) {
		t.Error("self container must never be controllable")
	}
}

func TestActionsPassTimeoutAndRecordCall(t *testing.T) {
	api := &fakeAPI{containers: []Container{{ID: "aaaa1111bbbb", Name: "/web", State: "running"}}}
	svc := newTestService(api)
	if err := svc.Stop(context.Background(), "aaaa1111bbbb"); err != nil {
		t.Fatal(err)
	}
	if api.lastStop != 30 || len(api.stopped) != 1 {
		t.Errorf("stop timeout/calls = %d %v", api.lastStop, api.stopped)
	}
	if err := svc.Start(context.Background(), "aaaa1111bbbb"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Restart(context.Background(), "aaaa1111bbbb"); err != nil {
		t.Fatal(err)
	}
	if len(api.started) != 1 || len(api.restarted) != 1 {
		t.Errorf("unexpected calls: %+v", api)
	}
}

func TestExecuteRejectsDeniedContainer(t *testing.T) {
	api := &fakeAPI{containers: []Container{{ID: "aaaa1111bbbb", Name: "/docktimer", State: "running"}}}
	svc := NewService(api, 30*time.Second, nil, []string{"docktimer"}, "", testLogger())
	err := svc.Execute(context.Background(), "START", "aaaa1111bbbb")
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("expected ErrNotAllowed, got %v", err)
	}
	if len(api.started) != 0 {
		t.Error("denied action reached the Docker API")
	}
}

func TestUnknownContainer(t *testing.T) {
	svc := newTestService(&fakeAPI{})
	if err := svc.Start(context.Background(), "deadbeef"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestErrorMapping(t *testing.T) {
	api := &fakeAPI{
		containers: []Container{{ID: "aaaa1111bbbb", Name: "/web", State: "running"}},
		actionErr:  context.DeadlineExceeded,
	}
	svc := newTestService(api)
	if err := svc.Start(context.Background(), "aaaa1111bbbb"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
}

func TestActionUsesAuthorizedContainerID(t *testing.T) {
	const allowedID = "abc123456789"
	api := &fakeAPI{containers: []Container{
		// A denied container whose *name* collides with the allowed ID prefix.
		// Docker resolves names before ID prefixes, so forwarding the original
		// reference would act on this container instead of the checked one.
		{ID: "ffff0000ffff", Name: "/abc", State: "running"},
		{ID: allowedID, Name: "/web", State: "running"},
	}}
	svc := NewService(api, 30*time.Second, nil, []string{"abc"}, "", testLogger())
	if err := svc.Start(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if len(api.started) != 1 || api.started[0] != allowedID {
		t.Fatalf("started = %v, want [%s]", api.started, allowedID)
	}
}

func TestPing(t *testing.T) {
	api := &fakeAPI{}
	svc := newTestService(api)
	if err := svc.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.pings != 1 {
		t.Errorf("pings = %d", api.pings)
	}
}
