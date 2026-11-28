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

	// Compose metadata. These are empty/false for containers that were not
	// started by Docker Compose.
	Project string
	Service string
	OneOff  bool
}

// Running reports whether the container is currently running.
func (c Container) Running() bool { return c.State == "running" }

// Stack is a Compose project with its containers.
type Stack struct {
	Project    string
	Containers []Container
}

// TotalCount returns the number of containers in the stack.
func (s Stack) TotalCount() int { return len(s.Containers) }

// RunningCount returns the number of running containers in the stack.
func (s Stack) RunningCount() int {
	n := 0
	for _, c := range s.Containers {
		if c.Running() {
			n++
		}
	}
	return n
}

// Actionable reports whether Docktimer may control at least one member.
func (s Stack) Actionable() bool {
	for _, c := range s.Containers {
		if c.Controllable {
			return true
		}
	}
	return false
}

// Result is the outcome for one container of a target.
type Result struct {
	ContainerID   string
	ContainerName string
	Message       string // e.g. "already running", "already stopped"
	Duration      time.Duration
	Err           error
}

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

// Stacks groups all Compose containers by project.
func (s *Service) Stacks(ctx context.Context) ([]Stack, error) {
	containers, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	return GroupStacks(containers), nil
}

// GroupStacks groups containers by their Compose project. Containers without a
// project label and one-off containers are ignored. Stacks and their members
// are sorted by project and by Compose service (then container name), which is
// also the start order.
func GroupStacks(containers []Container) []Stack {
	byProject := map[string][]Container{}
	for _, c := range containers {
		if strings.TrimSpace(c.Project) == "" || c.OneOff {
			continue
		}
		byProject[c.Project] = append(byProject[c.Project], c)
	}

	stacks := make([]Stack, 0, len(byProject))
	for project, members := range byProject {
		sort.SliceStable(members, func(i, j int) bool {
			si, ni := stackSortKey(members[i])
			sj, nj := stackSortKey(members[j])
			if si != sj {
				return si < sj
			}
			return ni < nj
		})
		stacks = append(stacks, Stack{Project: project, Containers: members})
	}
	sort.Slice(stacks, func(i, j int) bool {
		return strings.ToLower(stacks[i].Project) < strings.ToLower(stacks[j].Project)
	})
	return stacks
}

func stackSortKey(c Container) (service, name string) {
	service = strings.ToLower(strings.TrimSpace(c.Service))
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c.Name), "/"))
	return service, name
}

// FindStack returns the Compose project with the given name, or ErrNotFound.
func (s *Service) FindStack(ctx context.Context, project string) (Stack, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return Stack{}, ErrNotFound
	}
	stacks, err := s.Stacks(ctx)
	if err != nil {
		return Stack{}, err
	}
	for _, st := range stacks {
		if st.Project == project {
			return st, nil
		}
	}
	return Stack{}, ErrNotFound
}

// Allowed reports whether a container is inside the configured scope. A pattern
// may match the container name/ID or the Compose project name.
func (s *Service) Allowed(c Container) bool {
	id := strings.ToLower(strings.TrimSpace(c.ID))
	name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(c.Name)), "/")
	project := strings.ToLower(strings.TrimSpace(c.Project))

	if s.selfID != "" {
		self := strings.ToLower(s.selfID)
		if id != "" && (strings.HasPrefix(id, self) || strings.HasPrefix(self, id)) {
			return false
		}
	}
	for _, d := range s.deny {
		if matches(d, id, name) || matchesProject(d, project) {
			return false
		}
	}
	if len(s.allow) == 0 {
		return true
	}
	for _, a := range s.allow {
		if matches(a, id, name) || matchesProject(a, project) {
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
	return s.runOne(ctx, database.ActionStart, id)
}

// Stop stops a container using the configured graceful timeout.
func (s *Service) Stop(ctx context.Context, id string) error {
	return s.runOne(ctx, database.ActionStop, id)
}

// Restart restarts a container using the configured graceful timeout.
func (s *Service) Restart(ctx context.Context, id string) error {
	return s.runOne(ctx, database.ActionRestart, id)
}

// Execute runs an action by name. It is used by manual handlers.
func (s *Service) Execute(ctx context.Context, action database.Action, id string) error {
	return s.runOne(ctx, action, id)
}

// ExecuteTarget runs an action against a single container or a whole Compose
// stack. The returned error only reports that the target itself could not be
// resolved; per-container failures are carried in Result.Err so callers can
// log every member. For stacks, members that already have the desired state
// are reported with a Message instead of being an error. Non-controllable
// members (allow/deny list, self-protection) are reported as skipped and never
// touched. If no member may be controlled, ErrNotAllowed is returned.
func (s *Service) ExecuteTarget(ctx context.Context, action database.Action, targetType database.TargetType, ref string) ([]Result, error) {
	if !action.Valid() {
		return nil, fmt.Errorf("unsupported action %q", action)
	}
	switch targetType {
	case database.TargetContainer:
		res, err := s.executeOne(ctx, action, ref, false)
		if err != nil {
			return nil, err
		}
		return []Result{res}, nil
	case database.TargetStack:
		return s.executeStack(ctx, action, ref)
	default:
		return nil, fmt.Errorf("unsupported target type %q", targetType)
	}
}

func (s *Service) runOne(ctx context.Context, action database.Action, id string) error {
	if !action.Valid() {
		return fmt.Errorf("unsupported action %q", action)
	}
	res, err := s.executeOne(ctx, action, id, false)
	if err != nil {
		return err
	}
	return res.Err
}

func (s *Service) executeStack(ctx context.Context, action database.Action, project string) ([]Result, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, ErrNotFound
	}
	containers, err := s.List(ctx)
	if err != nil {
		return nil, err
	}

	found := false
	var members []Container
	for _, st := range GroupStacks(containers) {
		if st.Project != project {
			continue
		}
		found = true
		members = st.Containers
		break
	}
	if !found {
		return nil, ErrNotFound
	}
	actionable := 0
	for _, c := range members {
		if c.Controllable {
			actionable++
		}
	}
	if actionable == 0 {
		return nil, ErrNotAllowed
	}
	// GroupStacks sorts members by service name. Start/restart keep that order;
	// stop runs in reverse so dependents are stopped first.
	if action == database.ActionStop {
		for i, j := 0, len(members)-1; i < j; i, j = i+1, j-1 {
			members[i], members[j] = members[j], members[i]
		}
	}

	results := make([]Result, 0, len(members))
	for _, c := range members {
		if !c.Controllable {
			// Keep one row per member without touching locked containers.
			results = append(results, Result{
				ContainerID: c.ID, ContainerName: c.Name,
				Message: "skipped (not managed by Docktimer)",
			})
			continue
		}
		res, err := s.executeOne(ctx, action, c.ID, true)
		if err != nil {
			// The container disappeared between listing and execution.
			res = Result{ContainerID: c.ID, ContainerName: c.Name, Err: err}
		}
		results = append(results, res)
	}
	return results, nil
}

func (s *Service) executeOne(ctx context.Context, action database.Action, id string, skipSatisfied bool) (Result, error) {
	if strings.TrimSpace(id) == "" {
		return Result{}, ErrNotFound
	}
	// Enforce the scope on every execution path. Rules can outlive the moment a
	// container was allowed, so this check must not be skipped by the scheduler.
	ct, err := s.Find(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if !ct.Controllable {
		return Result{}, ErrNotAllowed
	}

	res := Result{ContainerID: ct.ID, ContainerName: ct.Name}
	if skipSatisfied {
		switch action {
		case database.ActionStart:
			if ct.Running() {
				res.Message = "already running"
				return res, nil
			}
		case database.ActionStop:
			if !ct.Running() {
				res.Message = "already stopped"
				return res, nil
			}
		}
	}

	timeout := s.actionTime
	if action != database.ActionStart {
		timeout += time.Duration(s.stopAfter) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	switch action {
	case database.ActionStart:
		err = s.api.Start(ctx, ct.ID)
	case database.ActionStop:
		err = s.api.Stop(ctx, ct.ID, s.stopAfter)
	case database.ActionRestart:
		err = s.api.Restart(ctx, ct.ID, s.stopAfter)
	}
	res.Duration = time.Since(start)
	if err == nil {
		s.log.Info("container action succeeded", "container_id", ct.ID, "action", action,
			"duration_ms", res.Duration.Milliseconds())
		return res, nil
	}
	mapped := mapError(err)
	res.Err = mapped
	s.log.Warn("container action failed", "container_id", ct.ID, "action", action,
		"error", mapped, "duration_ms", res.Duration.Milliseconds())
	return res, nil
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

func matchesProject(pattern, project string) bool {
	return project != "" && pattern == project
}
