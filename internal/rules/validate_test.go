package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/atpanos/docktimer/internal/database"
)

func validInput() Input {
	return Input{
		TargetType: database.TargetContainer,
		TargetRef:  "0123456789ab",
		TargetName: "/web",
		Action:     database.ActionStart,
		Cron:       "0 8 * * 1-5",
		Timezone:   "Europe/Berlin",
		Enabled:    true,
	}
}

func TestValidateAcceptsValidRule(t *testing.T) {
	in := validInput()
	if err := Validate(&in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateNormalizesInput(t *testing.T) {
	in := validInput()
	in.TargetType = database.TargetType("  stack  ")
	in.TargetRef = "  media  "
	in.TargetName = "  media  "
	in.Cron = "  0 8 * * 1-5  "
	in.Timezone = "  Europe/Berlin  "
	if err := Validate(&in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if in.TargetType != database.TargetStack || in.TargetRef != "media" || in.TargetName != "media" ||
		in.Cron != "0 8 * * 1-5" || in.Timezone != "Europe/Berlin" {
		t.Errorf("input was not normalized: %+v", in)
	}
}

func TestValidateRejectsBadTarget(t *testing.T) {
	in := validInput()
	in.TargetType = "SERVICE"
	in.TargetRef = "bad ref!"
	in.TargetName = ""
	err := Validate(&in)
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve := err.(*ValidationError)
	if ve.Fields["target_type"] == "" || ve.Fields["target"] == "" || ve.Fields["target_name"] == "" {
		t.Fatalf("missing fields: %#v", ve.Fields)
	}
}

func TestValidateRejectsBadCron(t *testing.T) {
	cases := []string{"", "0 8 * *", "0 8 * * 1-5 30", "@daily", "99 8 * * *", "a b c d e"}
	for _, expr := range cases {
		in := validInput()
		in.Cron = expr
		err := Validate(&in)
		if err == nil {
			t.Fatalf("expected error for cron %q", expr)
		}
		ve, ok := err.(*ValidationError)
		if !ok || ve.Fields["cron_expression"] == "" {
			t.Fatalf("expected cron field error for %q, got %v", expr, err)
		}
	}
}

func TestValidateRejectsBadTimezoneAndAction(t *testing.T) {
	in := validInput()
	in.Timezone = "Europe/Nowhere"
	in.Action = "KILL"
	err := Validate(&in)
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve := err.(*ValidationError)
	if ve.Fields["timezone"] == "" || ve.Fields["action"] == "" {
		t.Fatalf("missing fields: %#v", ve.Fields)
	}
}

func TestNextRunWeekdayMorning(t *testing.T) {
	// Friday 2026-11-20 07:00 Berlin → next weekday 08:00 is the same day.
	loc, _ := time.LoadLocation("Europe/Berlin")
	from := time.Date(2026, 11, 20, 7, 0, 0, 0, loc)
	next, err := NextRun("0 8 * * 1-5", "Europe/Berlin", from)
	if err != nil {
		t.Fatal(err)
	}
	if got := next.Format("2006-01-02 15:04"); got != "2026-11-20 08:00" {
		t.Errorf("next run = %s", got)
	}
}

func TestNextRunSkipsWeekend(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	from := time.Date(2026, 11, 20, 9, 0, 0, 0, loc) // Friday after 08:00
	next, err := NextRun("0 8 * * 1-5", "Europe/Berlin", from)
	if err != nil {
		t.Fatal(err)
	}
	if next.Weekday() != time.Monday {
		t.Errorf("expected Monday, got %s", next.Weekday())
	}
}

func TestNextRunRespectsTimezone(t *testing.T) {
	from := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	next, err := NextRun("0 8 * * *", "Europe/Berlin", from)
	if err != nil {
		t.Fatal(err)
	}
	if next.UTC().Hour() != 7 {
		t.Errorf("08:00 Berlin should be 07:00 UTC, got %v", next.UTC())
	}
}

func TestCronPreview(t *testing.T) {
	runs, err := CronPreview("0 */6 * * *", "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
	for i := 1; i < len(runs); i++ {
		if !runs[i].After(runs[i-1]) {
			t.Error("preview must be strictly increasing")
		}
	}
}

func TestValidationErrorMessageIsHelpful(t *testing.T) {
	in := validInput()
	in.Cron = "nope"
	err := Validate(&in).(*ValidationError)
	if !strings.Contains(strings.ToLower(err.Fields["cron_expression"]), "cron") {
		t.Errorf("message not helpful: %s", err.Fields["cron_expression"])
	}
}
