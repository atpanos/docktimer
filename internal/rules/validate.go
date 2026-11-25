package rules

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/atpanos/docktimer/internal/database"
	"github.com/robfig/cron/v3"
)

// Input is a validated rule form.
type Input struct {
	ContainerID   string
	ContainerName string
	Action        database.Action
	Cron          string
	Timezone      string
	Enabled       bool
}

// ValidationError carries per-field German messages for inline form errors.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed: %v", e.Fields)
}

// FieldsOrNil returns the field map or nil.
func (e *ValidationError) FieldsOrNil() map[string]string {
	if e == nil {
		return nil
	}
	return e.Fields
}

var containerRefRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// Validate checks all rule fields and returns a ValidationError on failure.
func Validate(in *Input) error {
	fields := map[string]string{}

	in.ContainerID = strings.TrimSpace(in.ContainerID)
	in.ContainerName = strings.TrimSpace(in.ContainerName)
	in.Cron = strings.TrimSpace(in.Cron)
	in.Timezone = strings.TrimSpace(in.Timezone)

	if !containerRefRe.MatchString(in.ContainerID) {
		fields["container_id"] = "Please select a container."
	}
	if in.ContainerName == "" {
		fields["container_name"] = "The container name is missing."
	}
	if !in.Action.Valid() {
		fields["action"] = "Invalid action."
	}

	if err := ValidateCron(in.Cron); err != nil {
		fields["cron_expression"] = err.Error()
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil || in.Timezone == "" {
		fields["timezone"] = "Unknown time zone. Example: Europe/Berlin."
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// ValidateCron accepts exactly five cron fields (minute granularity). The
// @descriptor syntax and six-field (seconds) expressions are rejected so that
// the UI and the scheduler agree on one format.
func ValidateCron(expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return errors.New("Cron expression is missing.")
	}
	if strings.HasPrefix(expr, "@") {
		return errors.New("Please use a 5-field expression, e.g. 0 8 * * 1-5.")
	}
	if n := len(strings.Fields(expr)); n != 5 {
		return fmt.Errorf("The cron expression needs exactly 5 fields (minute hour day month weekday), got: %d.", n)
	}
	if _, err := cron.ParseStandard(expr); err != nil {
		return fmt.Errorf("Invalid cron expression: %v", err)
	}
	return nil
}

// NextRun computes the next execution time in the rule's timezone.
func NextRun(expr, timezone string, from time.Time) (time.Time, error) {
	schedule, err := cron.ParseStandard(strings.TrimSpace(expr))
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(from.In(loc)), nil
}

// CronPreview validates an expression against a timezone and returns the next
// three runs. Used by the rule form.
func CronPreview(expr, timezone string, from time.Time) ([]time.Time, error) {
	schedule, err := cron.ParseStandard(strings.TrimSpace(expr))
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, err
	}
	next := []time.Time{}
	t := from.In(loc)
	for i := 0; i < 3; i++ {
		t = schedule.Next(t)
		next = append(next, t)
	}
	return next, nil
}
