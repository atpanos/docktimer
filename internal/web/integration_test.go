package web_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/atpanos/docktimer/internal/auth"
	"github.com/atpanos/docktimer/internal/config"
	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/docker"
	"github.com/atpanos/docktimer/internal/logging"
	"github.com/atpanos/docktimer/internal/rules"
	"github.com/atpanos/docktimer/internal/scheduler"
	"github.com/atpanos/docktimer/internal/web"
	"github.com/atpanos/docktimer/internal/web/handlers"
	"github.com/atpanos/docktimer/internal/web/templates"
	"github.com/atpanos/docktimer/internal/web/view"
	webassets "github.com/atpanos/docktimer/web"
)

const testContainerID = "27ffb171e786af717bfb4c8949073c78c6f15d95743dd2a8016ddd92a8e9653c"

type fakeAPI struct {
	containers []docker.Container
	calls      []string
}

func (f *fakeAPI) List(ctx context.Context) ([]docker.Container, error) {
	return append([]docker.Container(nil), f.containers...), nil
}

func (f *fakeAPI) Start(ctx context.Context, id string) error {
	f.calls = append(f.calls, "start:"+id)
	return nil
}

func (f *fakeAPI) Stop(ctx context.Context, id string, timeoutSeconds int) error {
	f.calls = append(f.calls, "stop:"+id)
	return nil
}

func (f *fakeAPI) Restart(ctx context.Context, id string, timeoutSeconds int) error {
	f.calls = append(f.calls, "restart:"+id)
	return nil
}

func (f *fakeAPI) Ping(ctx context.Context) error { return nil }

type harness struct {
	server *httptest.Server
	store  *database.Store
	api    *fakeAPI
	cfg    *config.Config
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cfg := &config.Config{
		AppPort:         "0",
		DatabasePath:    filepath.Join(t.TempDir(), "docktimer.db"),
		SessionSecret:   strings.Repeat("s", 40),
		DefaultTimezone: "Europe/Berlin",
		DockerHost:      "unix:///var/run/docker.sock",
		StopTimeout:     30 * time.Second,
		LogRetention:    1000,
		AppURL:          "http://example.test",
		CookieSecure:    "false",
		SessionTTL:      time.Hour,
		SessionIdleTTL:  30 * time.Minute,
		LogLevel:        "error",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	store, err := database.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	api := &fakeAPI{containers: []docker.Container{{
		ID: testContainerID, Name: "/web", Image: "nginx:alpine",
		State: "running", Status: "Up 2 hours",
	}}}
	dockerSvc := docker.NewService(api, cfg.StopTimeout, nil, nil, "", log)
	csrf := auth.NewCSRF(cfg.SessionSecret)
	authSvc := auth.NewService(store, csrf, auth.NewLimiter(5, 15*time.Minute), cfg.SessionTTL, cfg.SessionIdleTTL)
	rulesSvc := rules.NewService(store)
	logSvc := logging.NewService(store, cfg.LogRetention, 0, log)
	sched := scheduler.New(dockerSvc, store, logSvc, log)

	renderer, err := view.New(templates.FS, false)
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.New(handlers.Deps{
		Cfg: cfg, Auth: authSvc, Store: store, Docker: dockerSvc, Rules: rulesSvc,
		Logs: logSvc, Scheduler: sched, Renderer: renderer, Log: log, Version: "test",
	})
	srv, err := web.New(cfg, log, h, authSvc, webassets.FS)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{server: ts, store: store, api: api, cfg: cfg}
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

var (
	csrfInputRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	csrfMetaRe  = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`)
)

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func csrfFromForm(t *testing.T, body string) string {
	t.Helper()
	m := csrfInputRe.FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatal("csrf_token input not found in form")
	}
	return m[1]
}

func csrfFromMeta(t *testing.T, body string) string {
	t.Helper()
	m := csrfMetaRe.FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatal("csrf meta tag not found")
	}
	return m[1]
}

func post(t *testing.T, c *http.Client, u string, vals url.Values, token string, htmx bool) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(vals.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("X-CSRF-Token", token)
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body), res.Header.Get("Location")
}

func TestSetupLoginRuleToggleAndLog(t *testing.T) {
	h := newHarness(t)
	c := newClient(t)
	base := h.server.URL

	// Unauthenticated access is redirected to the login page.
	status, _ := get(t, c, base+"/")
	if status != http.StatusSeeOther {
		t.Fatalf("GET / unauthenticated = %d, want 303", status)
	}

	// Setup page renders and carries a CSRF token.
	status, body := get(t, c, base+"/setup")
	if status != http.StatusOK {
		t.Fatalf("GET /setup = %d", status)
	}
	if !strings.Contains(body, "Create admin account") {
		t.Fatal("setup form missing")
	}
	token := csrfFromForm(t, body)

	// Create the admin.
	status, _, loc := post(t, c, base+"/setup", url.Values{
		"csrf_token": {token},
		"username":   {"admin"},
		"password":   {"admin-password-123"},
		"confirm":    {"admin-password-123"},
	}, "", false)
	if status != http.StatusSeeOther || !strings.HasPrefix(loc, "/?flash=setup_done") {
		t.Fatalf("POST /setup = %d loc=%q", status, loc)
	}

	// Setup is complete; the wizard must not be reachable again.
	if status, _ := get(t, c, base+"/setup"); status != http.StatusSeeOther {
		t.Errorf("GET /setup after setup = %d", status)
	}

	// Dashboard shows the fake container.
	status, body = get(t, c, base+"/")
	if status != http.StatusOK || !strings.Contains(body, "Dashboard") {
		t.Fatalf("dashboard = %d", status)
	}
	if !strings.Contains(body, "Running") {
		t.Error("dashboard stats missing")
	}
	adminToken := csrfFromMeta(t, body)

	// A second POST /setup must be rejected before hashing, so a completed
	// setup cannot be used as a bcrypt CPU-exhaustion amplifier. The public
	// route validates CSRF against an empty session, so reuse the token from
	// the initial (unauthenticated) setup form.
	status, body, _ = post(t, c, base+"/setup", url.Values{
		"csrf_token": {token},
		"username":   {"other"},
		"password":   {"other-password-123"},
		"confirm":    {"other-password-123"},
	}, "", false)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "Setup has already been completed") {
		t.Fatalf("second POST /setup = %d, body=%q", status, body)
	}

	// Create a rule through the real HTTP handler.
	status, _, loc = post(t, c, base+"/rules", url.Values{
		"csrf_token":      {adminToken},
		"container_id":    {testContainerID},
		"action":          {"STOP"},
		"cron_expression": {"0 8 * * 1-5"},
		"timezone":        {"Europe/Berlin"},
		"enabled":         {"1"},
	}, "", false)
	if status != http.StatusSeeOther || !strings.Contains(loc, "rule_created") {
		t.Fatalf("POST /rules = %d loc=%q", status, loc)
	}

	rulesList, err := h.store.ListRules(context.Background())
	if err != nil || len(rulesList) != 1 {
		t.Fatalf("rules in db: %v %d", err, len(rulesList))
	}
	if !rulesList[0].Enabled || rulesList[0].CronExpression != "0 8 * * 1-5" {
		t.Errorf("unexpected rule: %+v", rulesList[0])
	}

	status, body = get(t, c, base+"/rules")
	if status != http.StatusOK || !strings.Contains(body, "0 8 * * 1-5") {
		t.Fatalf("rules page = %d", status)
	}

	// Form pages render and inline validation works.
	status, body = get(t, c, base+"/rules/new")
	if status != http.StatusOK || !strings.Contains(body, "Cron expression") {
		t.Fatalf("new rule form = %d", status)
	}
	status, body, _ = post(t, c, base+"/rules", url.Values{
		"csrf_token": {adminToken}, "container_id": {testContainerID},
		"action": {"STOP"}, "cron_expression": {"99 8 * * *"}, "timezone": {"Europe/Berlin"},
	}, "", false)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "Invalid cron expression") {
		t.Fatalf("invalid cron = %d", status)
	}
	if n, _ := h.store.ListRules(context.Background()); len(n) != 1 {
		t.Errorf("invalid rule was persisted")
	}
	status, _ = get(t, c, base+"/rules/1/edit")
	if status != http.StatusOK {
		t.Errorf("edit form = %d", status)
	}
	status, _ = get(t, c, base+"/settings")
	if status != http.StatusOK {
		t.Errorf("settings page = %d", status)
	}

	// Toggle the rule off via HTMX.
	status, body, _ = post(t, c, base+"/rules/1/toggle", url.Values{"enabled": {"0"}}, adminToken, true)
	if status != http.StatusOK {
		t.Fatalf("toggle = %d", status)
	}
	if !strings.Contains(body, "inactive") {
		t.Error("toggled row does not show inactive")
	}
	stored, _ := h.store.RuleByID(context.Background(), 1)
	if stored.Enabled {
		t.Error("rule still enabled in database")
	}

	// Manual stop writes an execution log.
	status, _, _ = post(t, c, base+"/containers/"+testContainerID+"/stop", url.Values{}, adminToken, true)
	if status != http.StatusOK {
		t.Fatalf("manual stop = %d", status)
	}
	if len(h.api.calls) != 1 || h.api.calls[0] != "stop:"+testContainerID {
		t.Fatalf("docker calls = %v", h.api.calls)
	}
	logs, err := h.store.RecentLogs(context.Background(), 5)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs: %v %d", err, len(logs))
	}
	if logs[0].Trigger != database.TriggerManual || logs[0].Status != database.StatusSuccess {
		t.Errorf("unexpected log: %+v", logs[0])
	}

	status, body = get(t, c, base+"/logs")
	if status != http.StatusOK || !strings.Contains(body, "MANUAL") {
		t.Fatalf("logs page = %d", status)
	}

	// Requests without CSRF are rejected.
	status, _, _ = post(t, c, base+"/rules", url.Values{
		"container_id": {testContainerID}, "action": {"STOP"},
		"cron_expression": {"0 8 * * *"}, "timezone": {"UTC"},
	}, "", false)
	if status != http.StatusForbidden {
		t.Fatalf("POST without CSRF = %d, want 403", status)
	}

	// A viewer can log in but must not control containers.
	status, _, _ = post(t, c, base+"/settings/users", url.Values{
		"username": {"viewer"}, "password": {"viewer-password-123"}, "role": {"VIEWER"},
	}, adminToken, false)
	if status != http.StatusSeeOther {
		t.Fatalf("create viewer = %d", status)
	}

	vc := newClient(t)
	_, body = get(t, vc, base+"/login")
	vtoken := csrfFromForm(t, body)
	status, _, _ = post(t, vc, base+"/login", url.Values{
		"csrf_token": {vtoken}, "username": {"viewer"}, "password": {"viewer-password-123"},
	}, "", false)
	if status != http.StatusSeeOther {
		t.Fatalf("viewer login = %d", status)
	}
	_, body = get(t, vc, base+"/")
	viewerToken := csrfFromMeta(t, body)

	status, _, _ = post(t, vc, base+"/containers/"+testContainerID+"/start", url.Values{}, viewerToken, true)
	if status != http.StatusForbidden {
		t.Fatalf("viewer start = %d, want 403", status)
	}
	if len(h.api.calls) != 1 {
		t.Errorf("viewer action reached Docker: %v", h.api.calls)
	}
}

func TestHealthEndpoints(t *testing.T) {
	h := newHarness(t)
	c := newClient(t)
	status, body := get(t, c, h.server.URL+"/healthz")
	if status != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Fatalf("healthz = %d %s", status, body)
	}
	status, body = get(t, c, h.server.URL+"/readyz")
	if status != http.StatusOK {
		t.Fatalf("readyz = %d %s", status, body)
	}
}
