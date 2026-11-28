package handlers

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/docker"
	"github.com/atpanos/docktimer/internal/rules"
	"github.com/atpanos/docktimer/internal/web/middleware"
	"github.com/gin-gonic/gin"
)

type ruleRow struct {
	Rule     database.Rule
	At       time.Time
	OK       bool
	TZ       string
	CanWrite bool
}

type rulesPageData struct {
	Rows    []ruleRow
	LoadErr string
}

type ruleFormData struct {
	IsNew        bool
	ID           int64
	Input        rules.Input
	Errors       map[string]string
	TargetHidden bool
	TargetValue  string
	Containers   []docker.Container
	Stacks       []docker.Stack
	DockerErr    string
	TZOptions    []string
	Preview      []time.Time
	PreviewErr   string
	TZ           string
	ActionValues []database.Action
}

// RulesPage lists all rules with their next run.
func (h *Handler) RulesPage(c *gin.Context) {
	user, _ := middleware.CurrentUser(c)
	data := rulesPageData{}

	list, err := h.deps.Rules.List(c.Request.Context())
	if err != nil {
		data.LoadErr = "Could not load rules."
		h.deps.Log.Error("list rules", "error", err)
	}
	for _, r := range list {
		at, ok := h.deps.Scheduler.Next(r.ID)
		data.Rows = append(data.Rows, ruleRow{
			Rule: r, At: at, OK: ok && r.Enabled,
			TZ: h.deps.Cfg.DefaultTimezone, CanWrite: user != nil && user.IsAdmin(),
		})
	}
	sort.SliceStable(data.Rows, func(i, j int) bool {
		return data.Rows[i].Rule.TargetName < data.Rows[j].Rule.TargetName
	})
	h.page(c, http.StatusOK, "rules", "Rules", "rules", data)
}

// NewRulePage renders the empty rule form.
func (h *Handler) NewRulePage(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	h.renderRuleForm(c, http.StatusOK, ruleFormData{
		IsNew:        true,
		Input:        rules.Input{Action: database.ActionStart, Timezone: h.deps.Cfg.DefaultTimezone, Enabled: true},
		TZOptions:    timezoneOptions(h.deps.Cfg.DefaultTimezone),
		ActionValues: []database.Action{database.ActionStart, database.ActionStop, database.ActionRestart},
		TZ:           h.deps.Cfg.DefaultTimezone,
	})
}

// CreateRule handles the creation form.
func (h *Handler) CreateRule(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	h.ruleMu.Lock()
	defer h.ruleMu.Unlock()
	in := parseRuleInput(c)
	if err := h.resolveTarget(c, &in); err != nil {
		h.renderRuleError(c, 0, in, map[string]string{"target": dockerErrorText(err)}, true)
		return
	}

	user, _ := middleware.CurrentUser(c)
	var createdBy *int64
	if user != nil {
		id := user.ID
		createdBy = &id
	}

	rule, err := h.deps.Rules.Create(c.Request.Context(), in, createdBy)
	if err != nil {
		var ve *rules.ValidationError
		if errors.As(err, &ve) {
			h.renderRuleError(c, 0, in, ve.Fields, true)
			return
		}
		h.deps.Log.Error("create rule", "error", err)
		h.errorPage(c, http.StatusInternalServerError, "Could not save the rule.")
		return
	}
	h.deps.Scheduler.Upsert(rule)
	redirectWithFlash(c, "/rules", "rule_created")
}

// EditRulePage renders the edit form.
func (h *Handler) EditRulePage(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	id, ok := paramID(c)
	if !ok {
		h.errorPage(c, http.StatusNotFound, "Rule not found.")
		return
	}
	rule, err := h.deps.Rules.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.errorPage(c, http.StatusNotFound, "Rule not found.")
			return
		}
		h.errorPage(c, http.StatusInternalServerError, "Could not load the rule.")
		return
	}
	in := rules.Input{
		TargetType: rule.TargetType,
		TargetRef:  rule.TargetRef,
		TargetName: rule.TargetName,
		Action:     rule.Action,
		Cron:       rule.CronExpression,
		Timezone:   rule.Timezone,
		Enabled:    rule.Enabled,
	}
	h.renderRuleForm(c, http.StatusOK, ruleFormData{
		IsNew:        false,
		ID:           rule.ID,
		Input:        in,
		TZOptions:    timezoneOptions(rule.Timezone),
		ActionValues: []database.Action{database.ActionStart, database.ActionStop, database.ActionRestart},
		TZ:           rule.Timezone,
	})
}

// UpdateRule handles the edit form.
func (h *Handler) UpdateRule(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	h.ruleMu.Lock()
	defer h.ruleMu.Unlock()
	id, ok := paramID(c)
	if !ok {
		h.errorPage(c, http.StatusNotFound, "Rule not found.")
		return
	}
	in := parseRuleInput(c)
	existing, err := h.deps.Rules.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.errorPage(c, http.StatusNotFound, "Rule not found.")
			return
		}
		h.deps.Log.Error("load rule", "error", err)
		h.errorPage(c, http.StatusInternalServerError, "Could not load the rule.")
		return
	}
	if in.TargetType == existing.TargetType && strings.TrimSpace(in.TargetRef) == existing.TargetRef {
		// The target is unchanged. Allow saving other fields even when the
		// container or stack is currently locked or no longer visible.
		in.TargetRef = existing.TargetRef
		in.TargetName = existing.TargetName
	} else {
		if err := h.resolveTarget(c, &in); err != nil {
			h.renderRuleError(c, id, in, map[string]string{"target": dockerErrorText(err)}, false)
			return
		}
	}

	rule, err := h.deps.Rules.Update(c.Request.Context(), id, in)
	if err != nil {
		var ve *rules.ValidationError
		if errors.As(err, &ve) {
			h.renderRuleError(c, id, in, ve.Fields, false)
			return
		}
		if errors.Is(err, database.ErrNotFound) {
			h.errorPage(c, http.StatusNotFound, "Rule not found.")
			return
		}
		h.deps.Log.Error("update rule", "error", err)
		h.errorPage(c, http.StatusInternalServerError, "Could not save the rule.")
		return
	}
	h.deps.Scheduler.Upsert(rule)
	redirectWithFlash(c, "/rules", "rule_updated")
}

// ToggleRule enables or disables a rule.
func (h *Handler) ToggleRule(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	h.ruleMu.Lock()
	defer h.ruleMu.Unlock()
	id, ok := paramID(c)
	if !ok {
		h.errorPage(c, http.StatusNotFound, "Rule not found.")
		return
	}
	enabled := c.PostForm("enabled") == "1"
	rule, err := h.deps.Rules.Toggle(c.Request.Context(), id, enabled)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.errorPage(c, http.StatusNotFound, "Rule not found.")
			return
		}
		h.errorPage(c, http.StatusInternalServerError, "Could not change the rule status.")
		return
	}
	h.deps.Scheduler.Upsert(rule)

	if isHTMX(c) {
		at, ok := h.deps.Scheduler.Next(rule.ID)
		h.toast(c, "success", "Rule is now "+map[bool]string{true: "active", false: "inactive"}[rule.Enabled]+".")
		h.fragment(c, http.StatusOK, "rule_row", ruleRow{
			Rule: rule, At: at, OK: ok && rule.Enabled,
			TZ: h.deps.Cfg.DefaultTimezone, CanWrite: true,
		})
		return
	}
	redirectWithFlash(c, "/rules", "rule_toggled")
}

// DeleteRule removes a rule.
func (h *Handler) DeleteRule(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	h.ruleMu.Lock()
	defer h.ruleMu.Unlock()
	id, ok := paramID(c)
	if !ok {
		h.errorPage(c, http.StatusNotFound, "Rule not found.")
		return
	}
	if err := h.deps.Rules.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, database.ErrNotFound) {
			h.errorPage(c, http.StatusNotFound, "Rule not found.")
			return
		}
		h.deps.Log.Error("delete rule", "error", err)
		h.errorPage(c, http.StatusInternalServerError, "Could not delete the rule.")
		return
	}
	h.deps.Scheduler.Remove(id)

	if isHTMX(c) {
		c.Header("HX-Refresh", "true")
		h.toast(c, "success", "Rule deleted.")
		c.Status(http.StatusNoContent)
		return
	}
	redirectWithFlash(c, "/rules", "rule_deleted")
}

func (h *Handler) renderRuleForm(c *gin.Context, status int, data ruleFormData) {
	containers, err := h.deps.Docker.List(c.Request.Context())
	if err != nil {
		data.DockerErr = dockerErrorText(err)
	}
	data.Containers = containers
	data.Stacks = docker.GroupStacks(containers)
	data.TargetValue = targetValue(data.Input)
	if data.Input.TargetRef != "" {
		found := false
		if data.Input.TargetType == database.TargetStack {
			for _, st := range data.Stacks {
				if st.Project == data.Input.TargetRef {
					found = true
					if data.Input.TargetName == "" {
						data.Input.TargetName = st.Project
					}
					break
				}
			}
		} else {
			for _, ct := range containers {
				if ct.ID == data.Input.TargetRef {
					found = true
					if data.Input.TargetName == "" {
						data.Input.TargetName = ct.Name
					}
					break
				}
			}
		}
		data.TargetHidden = !found
	}
	if len(data.TZOptions) == 0 {
		data.TZOptions = timezoneOptions(h.deps.Cfg.DefaultTimezone)
	}
	if data.TZ == "" {
		data.TZ = h.deps.Cfg.DefaultTimezone
	}
	if len(data.ActionValues) == 0 {
		data.ActionValues = []database.Action{database.ActionStart, database.ActionStop, database.ActionRestart}
	}
	if data.Errors == nil {
		data.Errors = map[string]string{}
	}
	if data.Input.Cron != "" && data.Errors["cron_expression"] == "" && data.Errors["timezone"] == "" {
		preview, err := rules.CronPreview(data.Input.Cron, data.Input.Timezone, time.Now())
		if err != nil {
			data.PreviewErr = err.Error()
		} else {
			data.Preview = preview
		}
	}
	title := "New rule"
	if !data.IsNew {
		title = "Edit rule"
	}
	h.page(c, status, "rule_form", title, "rules", data)
}

func (h *Handler) renderRuleError(c *gin.Context, id int64, in rules.Input, fields map[string]string, isNew bool) {
	h.renderRuleForm(c, http.StatusUnprocessableEntity, ruleFormData{
		IsNew:  isNew,
		ID:     id,
		Input:  in,
		Errors: fields,
		TZ:     in.Timezone,
	})
}

func parseRuleInput(c *gin.Context) rules.Input {
	targetType, targetRef := parseTargetValue(c.PostForm("target"))
	return rules.Input{
		TargetType: targetType,
		TargetRef:  targetRef,
		Action:     database.Action(strings.ToUpper(strings.TrimSpace(c.PostForm("action")))),
		Cron:       c.PostForm("cron_expression"),
		Timezone:   c.PostForm("timezone"),
		Enabled:    c.PostForm("enabled") != "",
	}
}

// parseTargetValue decodes the composite picker value. Container IDs cannot
// contain a colon, so the "stack:" prefix is unambiguous.
func parseTargetValue(v string) (database.TargetType, string) {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "stack:") {
		return database.TargetStack, strings.TrimSpace(strings.TrimPrefix(v, "stack:"))
	}
	return database.TargetContainer, v
}

func targetValue(in rules.Input) string {
	if in.TargetType == database.TargetStack && in.TargetRef != "" {
		return "stack:" + in.TargetRef
	}
	return in.TargetRef
}

// resolveTarget maps a submitted target reference to exactly one container or
// Compose stack and stores the canonical reference and display name.
func (h *Handler) resolveTarget(c *gin.Context, in *rules.Input) error {
	switch in.TargetType {
	case database.TargetStack:
		stack, err := h.deps.Docker.FindStack(c.Request.Context(), in.TargetRef)
		if err != nil {
			return err
		}
		if !stack.Actionable() {
			return docker.ErrNotAllowed
		}
		in.TargetRef = stack.Project
		in.TargetName = stack.Project
		return nil
	case database.TargetContainer:
		ct, err := h.resolveContainer(c, in.TargetRef)
		if err != nil {
			return err
		}
		in.TargetRef = ct.ID
		in.TargetName = ct.Name
		return nil
	default:
		return docker.ErrNotFound
	}
}

// resolveContainer maps a submitted reference to exactly one container. Exact
// IDs and names win; ambiguous ID prefixes are rejected instead of picking the
// first match.
func (h *Handler) resolveContainer(c *gin.Context, ref string) (docker.Container, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return docker.Container{}, docker.ErrNotFound
	}
	list, err := h.deps.Docker.List(c.Request.Context())
	if err != nil {
		return docker.Container{}, err
	}

	var prefixMatches []docker.Container
	for _, ct := range list {
		if ct.ID == ref || ct.Name == ref {
			if !ct.Controllable {
				return docker.Container{}, docker.ErrNotAllowed
			}
			return ct, nil
		}
		if strings.HasPrefix(ct.ID, ref) {
			prefixMatches = append(prefixMatches, ct)
		}
	}

	switch len(prefixMatches) {
	case 0:
		return docker.Container{}, docker.ErrNotFound
	case 1:
		ct := prefixMatches[0]
		if !ct.Controllable {
			return docker.Container{}, docker.ErrNotAllowed
		}
		return ct, nil
	default:
		return docker.Container{}, docker.ErrAmbiguous
	}
}

func timezoneOptions(current string) []string {
	base := []string{
		"Europe/Berlin", "Europe/Vienna", "Europe/Zurich", "Europe/London",
		"UTC", "America/New_York", "America/Chicago", "America/Los_Angeles",
		"Asia/Tokyo", "Asia/Singapore", "Australia/Sydney",
	}
	for _, tz := range base {
		if tz == current {
			return base
		}
	}
	return append([]string{current}, base...)
}
