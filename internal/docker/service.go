package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/atpanos/docktimer/internal/database"
)

// Container is a read-only view of a Docker container.
type Container struct {
	ID           string
	Name         string
	Image        string
	State        string
	Status       string
	Created      time.Time
	Controllable bool
}

// Running reports whether the container is currently running.
func (c Container) Running() bool { return c.State == "running" }

// API is the narrow Docker interface. It is implemented by engine and replaced
// by a fake in tests.
type API interface {
	List(ctx context.Context) ([]Container, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, timeoutSeconds int) error
	Restart(ctx context.Context, id string, timeoutSeconds int) error
	Ping(ctx context.Context) error
}

// Service applies Docktimer's policy (scope, timeouts) on top of the Docker API.
type Service struct {
	api        API
	stopAfter  int
	allow      []string
	deny       []string
	selfID     string
	log        *slog.Logger
	actionTime time.Duration
}

// NewService creates a Docker service.
func NewService(api API, stopTimeout time.Duration, allow, deny []string, selfID string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		api:        api,
		stopAfter:  int(stopTimeout.Seconds()),
		allow:      normalize(allow),
		deny:       normalize(deny),
		selfID:     strings.TrimSpace(selfID),
		log:        log,
		actionTime: 30 * time.Second,
	}
}

// SetActionTimeout overrides the maximum duration of one action (tests).
func (s *Service) SetActionTimeout(d time.Duration) { s.actionTime = d }

// Ping checks the daemon connection.
func (s *Service) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.api.Ping(ctx)
}

// List returns all containers, marked with whether Docktimer may control them.
func (s *Service) List(ctx context.Context) ([]Container, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	containers, err := s.api.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range containers {
		containers[i].Controllable = s.Allowed(containers[i])
	}
	sort.Slice(containers, func(i, j int) bool {
		if containers[i].Running() != containers[j].Running() {
			return containers[i].Running()
		}
		return strings.ToLower(containers[i].Name) < strings.ToLower(containers[j].Name)
	})
	return containers, nil
}

// Allowed reports whether a container is inside the configured scope.
func (s *Service) Allowed(c Container) bool {
	id := strings.ToLower(strings.TrimSpace(c.ID))
	name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(c.Name)), "/")

	if s.selfID != "" {
		self := strings.ToLower(s.selfID)
		if id != "" && (strings.HasPrefix(id, self) || strings.HasPrefix(self, id)) {
			return false
		}
	}
	for _, d := range s.deny {
		if matches(d, id, name) {
			return false
		}
	}
	if len(s.allow) == 0 {
		return true
	}
	for _, a := range s.allow {
		if matches(a, id, name) {
			return true
		}
	}
	return false
}

// Find returns the container with the given ID, or ErrNotFound.
func (s *Service) Find(ctx context.Context, id string) (Container, error) {
	containers, err := s.List(ctx)
	if err != nil {
		return Container{}, err
	}
	for _, c := range containers {
		if c.ID == id || strings.HasPrefix(c.ID, id) {
			return c, nil
		}
	}
	return Container{}, ErrNotFound
}

// Start starts a container.
func (s *Service) Start(ctx context.Context, id string) error {
	return s.execute(ctx, id, "start", func(ctx context.Context, cid string) error {
		return s.api.Start(ctx, cid)
	})
}

// Stop stops a container using the configured graceful timeout.
func (s *Service) Stop(ctx context.Context, id string) error {
	return s.execute(ctx, id, "stop", func(ctx context.Context, cid string) error {
		return s.api.Stop(ctx, cid, s.stopAfter)
	})
}

// Restart restarts a container using the configured graceful timeout.
func (s *Service) Restart(ctx context.Context, id string) error {
	return s.execute(ctx, id, "restart", func(ctx context.Context, cid string) error {
		return s.api.Restart(ctx, cid, s.stopAfter)
	})
}

// Execute runs an action by name. It is used by manual handlers and scheduler.
func (s *Service) Execute(ctx context.Context, action database.Action, id string) error {
	switch action {
	case database.ActionStart:
		return s.Start(ctx, id)
	case database.ActionStop:
		return s.Stop(ctx, id)
	case database.ActionRestart:
		return s.Restart(ctx, id)
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
}

func (s *Service) execute(ctx context.Context, id, action string, fn func(context.Context, string) error) error {
	if strings.TrimSpace(id) == "" {
		return ErrNotFound
	}
	// Enforce the scope on every execution path. Rules can outlive the moment a
	// container was allowed, so this check must not be skipped by the scheduler.
	ct, err := s.Find(ctx, id)
	if err != nil {
		return err
	}
	if !ct.Controllable {
		return ErrNotAllowed
	}

	timeout := s.actionTime
	if action != "start" {
		timeout += time.Duration(s.stopAfter) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	// Act on the full ID of the container that passed the scope check. Docker
	// resolves a reference by name before ID prefix, so forwarding the caller's
	// reference could hit a different (denied) container with a colliding name.
	err = fn(ctx, ct.ID)
	if err == nil {
		s.log.Info("container action succeeded", "container_id", ct.ID, "action", action,
			"duration_ms", time.Since(start).Milliseconds())
		return nil
	}
	mapped := mapError(err)
	s.log.Warn("container action failed", "container_id", ct.ID, "action", action,
		"error", mapped, "duration_ms", time.Since(start).Milliseconds())
	return mapped
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrUnavailable), errors.Is(err, ErrNotFound),
		errors.Is(err, ErrConflict), errors.Is(err, ErrNotAllowed), errors.Is(err, ErrTimeout):
		return err
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	case isConnectionFailed(err):
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	case isNotFound(err):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case isConflict(err):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	default:
		return fmt.Errorf("docker action failed: %w", err)
	}
}

func normalize(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(v, "/")))
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func matches(pattern, id, name string) bool {
	if pattern == "" {
		return false
	}
	if name != "" && pattern == name {
		return true
	}
	if id != "" && len(pattern) >= 6 && strings.HasPrefix(id, pattern) {
		return true
	}
	return false
}
