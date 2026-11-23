package database

import "time"

// Role is a user role.
type Role string

const (
	RoleAdmin  Role = "ADMIN"
	RoleViewer Role = "VIEWER"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleAdmin || r == RoleViewer }

// User is an application account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// IsAdmin reports whether the user may mutate state.
func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

// Session is a server-side login session. ID is the SHA-256 hash of the cookie
// token, never the token itself.
type Session struct {
	ID         string
	UserID     int64
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	IP         string
}

// Action is a container operation.
type Action string

const (
	ActionStart   Action = "START"
	ActionStop    Action = "STOP"
	ActionRestart Action = "RESTART"
)

// Valid reports whether a is a supported action.
func (a Action) Valid() bool {
	return a == ActionStart || a == ActionStop || a == ActionRestart
}

// Trigger describes what caused an execution.
type Trigger string

const (
	TriggerSchedule Trigger = "SCHEDULE"
	TriggerManual   Trigger = "MANUAL"
)

// Status is the outcome of an execution.
type Status string

const (
	StatusSuccess Status = "SUCCESS"
	StatusFailed  Status = "FAILED"
)

// Rule is a scheduled container action.
type Rule struct {
	ID             int64
	ContainerID    string
	ContainerName  string
	Action         Action
	CronExpression string
	Timezone       string
	Enabled        bool
	CreatedBy      *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ExecutionLog is one executed (or attempted) container action.
type ExecutionLog struct {
	ID            int64
	RuleID        *int64
	ContainerID   string
	ContainerName string
	Action        Action
	Trigger       Trigger
	Status        Status
	Message       string
	DurationMS    int64
	CreatedAt     time.Time
}

// LogFilter selects a page of execution logs. The zero value of the string
// fields means "no filter". BeforeID enables cursor pagination.
type LogFilter struct {
	BeforeID    int64
	Status      Status
	Trigger     Trigger
	ContainerID string
	Limit       int
}
