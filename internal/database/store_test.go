package database

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atpanos/docktimer/migrations"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "docktimer-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestMigrateIsIdempotent(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if err := Migrate(ctx, store.DB(), migrations.FS); err != nil {
		t.Fatalf("second migrate run failed: %v", err)
	}
}

func TestUserLifecycle(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	u, err := store.CreateUser(ctx, "Admin", "hash-a", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || u.Role != RoleAdmin {
		t.Fatalf("unexpected user: %+v", u)
	}
	if _, err := store.CreateUser(ctx, "admin", "hash-b", RoleViewer); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("expected case-insensitive duplicate error, got %v", err)
	}
	got, err := store.UserByUsername(ctx, "admin")
	if err != nil || got.ID != u.ID {
		t.Fatalf("lookup failed: %v %+v", err, got)
	}
	if n, _ := store.CountUsers(ctx); n != 1 {
		t.Errorf("CountUsers = %d", n)
	}
	if n, _ := store.CountAdmins(ctx); n != 1 {
		t.Errorf("CountAdmins = %d", n)
	}
	if err := store.UpdatePassword(ctx, u.ID, "hash-c"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.UserByID(ctx, u.ID)
	if got.PasswordHash != "hash-c" {
		t.Errorf("password not updated: %q", got.PasswordHash)
	}
	viewer, err := store.CreateUser(ctx, "viewer", "hash-d", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser(ctx, viewer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UserByID(ctx, viewer.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected not found, got %v", err)
	}
}

func TestDeleteUserProtectsLastAdmin(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	admin, err := store.CreateUser(ctx, "admin", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}
	if _, err := store.UserByID(ctx, admin.ID); err != nil {
		t.Fatalf("last admin must survive: %v", err)
	}

	viewer, err := store.CreateUser(ctx, "viewer", "hash", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser(ctx, viewer.ID); err != nil {
		t.Fatalf("deleting a viewer failed: %v", err)
	}
	if err := store.DeleteUser(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing user, got %v", err)
	}

	if _, err := store.CreateUser(ctx, "admin2", "hash", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser(ctx, admin.ID); err != nil {
		t.Fatalf("deleting an admin while another remains failed: %v", err)
	}
}

func TestDeleteUserConcurrentKeepsOneAdmin(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	a, err := store.CreateUser(ctx, "admin-a", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.CreateUser(ctx, "admin-b", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []int64{a.ID, b.ID} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			errs <- store.DeleteUser(ctx, id)
		}(id)
	}
	wg.Wait()
	close(errs)

	deleted, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			deleted++
		case errors.Is(err, ErrLastAdmin):
			refused++
		default:
			t.Fatalf("unexpected delete error: %v", err)
		}
	}
	if deleted != 1 || refused != 1 {
		t.Fatalf("deleted=%d refused=%d, want 1/1", deleted, refused)
	}
	if n, err := store.CountAdmins(ctx); err != nil || n != 1 {
		t.Fatalf("admins after concurrent delete = %d, err=%v", n, err)
	}
}

func TestCreateFirstAdminOnlyOnce(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.CreateFirstAdmin(ctx, "root", "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFirstAdmin(ctx, "second", "hash"); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("expected ErrSetupComplete, got %v", err)
	}
}

func TestSessionsAndCascade(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	u, _ := store.CreateUser(ctx, "user", "hash", RoleViewer)
	now := time.Now().UTC()
	sess := Session{
		ID: "abc", UserID: u.ID, ExpiresAt: now.Add(time.Hour),
		CreatedAt: now, LastSeenAt: now, IP: "127.0.0.1", UserAgent: "test",
	}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := store.SessionByID(ctx, "abc")
	if err != nil || got.UserID != u.ID {
		t.Fatalf("session lookup: %v %+v", err, got)
	}
	if err := store.TouchSession(ctx, "abc", now.Add(time.Minute), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = store.SessionByID(ctx, "abc")
	if diff := got.ExpiresAt.Sub(now.Add(2 * time.Hour)); diff > 2*time.Millisecond || diff < -2*time.Millisecond {
		t.Errorf("expiry not extended: %v", got.ExpiresAt)
	}

	expired := Session{ID: "old", UserID: u.ID, ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-2 * time.Hour)}
	_ = store.CreateSession(ctx, expired)
	if n, err := store.DeleteExpiredSessions(ctx, now); err != nil || n != 1 {
		t.Fatalf("delete expired: n=%d err=%v", n, err)
	}

	if err := store.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SessionByID(ctx, "abc"); !errors.Is(err, ErrNotFound) {
		t.Error("sessions must be deleted with the user (cascade)")
	}
}

func TestRuleCRUD(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	u, _ := store.CreateUser(ctx, "admin", "hash", RoleAdmin)
	createdBy := u.ID

	r, err := store.CreateRule(ctx, Rule{
		ContainerID: "abc123", ContainerName: "/web", Action: ActionStart,
		CronExpression: "0 8 * * *", Timezone: "Europe/Berlin", Enabled: true, CreatedBy: &createdBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == 0 || !r.Enabled || r.CreatedBy == nil || *r.CreatedBy != u.ID {
		t.Fatalf("unexpected rule: %+v", r)
	}

	enabled, err := store.ListEnabledRules(ctx)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("enabled rules: %v %d", err, len(enabled))
	}

	r.Action = ActionStop
	r.Enabled = false
	if err := store.UpdateRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, _ := store.RuleByID(ctx, r.ID)
	if got.Action != ActionStop || got.Enabled {
		t.Errorf("rule not updated: %+v", got)
	}

	if err := store.SetRuleEnabled(ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	got, _ = store.RuleByID(ctx, r.ID)
	if !got.Enabled {
		t.Error("rule not enabled")
	}

	if err := store.DeleteRule(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RuleByID(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Error("rule still present")
	}
}

func insertLog(t *testing.T, store *Store, status Status, containerID string, cap int) ExecutionLog {
	t.Helper()
	l, err := store.InsertLog(context.Background(), ExecutionLog{
		ContainerID: containerID, ContainerName: "/c", Action: ActionStart,
		Trigger: TriggerManual, Status: status, Message: "msg", DurationMS: 5,
	}, cap, 0)
	if err != nil {
		t.Fatalf("insert log: %v", err)
	}
	return l
}

func TestLogsRetentionAndPagination(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	for i := 0; i < 60; i++ {
		status := StatusSuccess
		if i%3 == 0 {
			status = StatusFailed
		}
		insertLog(t, store, status, "container-a", 50)
	}
	count, err := store.CountLogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 50 {
		t.Fatalf("retention kept %d logs, want 50", count)
	}

	page, err := store.LogsPage(ctx, LogFilter{Limit: 10})
	if err != nil || len(page) != 10 {
		t.Fatalf("page: %v %d", err, len(page))
	}
	for i := 1; i < len(page); i++ {
		if page[i].ID >= page[i-1].ID {
			t.Error("logs not ordered newest first")
		}
	}

	next, err := store.LogsPage(ctx, LogFilter{Limit: 10, BeforeID: page[len(page)-1].ID})
	if err != nil || len(next) != 10 {
		t.Fatalf("next page: %v %d", err, len(next))
	}
	if next[0].ID >= page[len(page)-1].ID {
		t.Error("cursor pagination overlaps")
	}

	failed, err := store.LogsPage(ctx, LogFilter{Limit: 100, Status: StatusFailed})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range failed {
		if l.Status != StatusFailed {
			t.Error("filter returned wrong status")
		}
	}

	failures, _ := store.RecentFailures(ctx, 3)
	if len(failures) != 3 {
		t.Errorf("RecentFailures = %d", len(failures))
	}
}

func TestPerContainerRetention(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := store.InsertLog(ctx, ExecutionLog{
			ContainerID: "a", ContainerName: "/a", Action: ActionStart,
			Trigger: TriggerManual, Status: StatusSuccess,
		}, 0, 3); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(1) FROM execution_logs WHERE container_id = 'a'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("per-container retention kept %d, want 3", n)
	}
}

func TestLogKeepsRuleIDNullAfterRuleDelete(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	r, _ := store.CreateRule(ctx, Rule{
		ContainerID: "abc", ContainerName: "/web", Action: ActionStart,
		CronExpression: "0 8 * * *", Timezone: "UTC", Enabled: true,
	})
	ruleID := r.ID
	if _, err := store.InsertLog(ctx, ExecutionLog{
		RuleID: &ruleID, ContainerID: "abc", ContainerName: "/web", Action: ActionStart,
		Trigger: TriggerSchedule, Status: StatusSuccess,
	}, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRule(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	logs, _ := store.RecentLogs(ctx, 1)
	if len(logs) != 1 || logs[0].RuleID != nil {
		t.Fatalf("expected log with NULL rule_id, got %+v", logs)
	}
}
