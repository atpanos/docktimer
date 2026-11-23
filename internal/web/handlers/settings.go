package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/atpanos/docktimer/internal/auth"
	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/web/middleware"
	"github.com/gin-gonic/gin"
)

type settingsData struct {
	Users        []database.User
	DockerOK     bool
	DockerErr    string
	LogCount     int64
	RuleCount    int
	Version      string
	AppURL       string
	DockerHost   string
	DefaultTZ    string
	StopTimeout  int
	Retention    int
	SessionHours int
	CurrentID    int64
	Error        string
	Notice       string
}

// SettingsPage renders account and instance settings.
func (h *Handler) SettingsPage(c *gin.Context) {
	data := h.settingsData(c)
	h.page(c, http.StatusOK, "settings", "Settings", "settings", data)
}

func (h *Handler) settingsData(c *gin.Context) settingsData {
	ctx := c.Request.Context()
	user, _ := middleware.CurrentUser(c)

	data := settingsData{
		Version:      h.deps.Version,
		AppURL:       h.deps.Cfg.AppURL,
		DockerHost:   h.deps.Cfg.DockerHost,
		DefaultTZ:    h.deps.Cfg.DefaultTimezone,
		StopTimeout:  int(h.deps.Cfg.StopTimeout.Seconds()),
		Retention:    h.deps.Cfg.LogRetention,
		SessionHours: int(h.deps.Cfg.SessionTTL.Hours()),
	}
	if user != nil {
		data.CurrentID = user.ID
	}

	if err := h.deps.Docker.Ping(ctx); err != nil {
		data.DockerErr = dockerErrorText(err)
	} else {
		data.DockerOK = true
	}
	data.LogCount, _ = h.deps.Logs.Count(ctx)
	if list, err := h.deps.Rules.List(ctx); err == nil {
		data.RuleCount = len(list)
	}
	if user != nil && user.IsAdmin() {
		data.Users, _ = h.deps.Store.ListUsers(ctx)
	}
	return data
}

// ChangePassword updates the current user's password.
func (h *Handler) ChangePassword(c *gin.Context) {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		h.errorPage(c, http.StatusUnauthorized, "Not signed in.")
		return
	}
	current := c.PostForm("current_password")
	next := c.PostForm("new_password")
	confirm := c.PostForm("confirm_password")

	rerender := func(status int, msg string) {
		data := h.settingsData(c)
		data.Error = msg
		h.page(c, status, "settings", "Settings", "settings", data)
	}

	switch {
	case next != confirm:
		rerender(http.StatusUnprocessableEntity, "The new passwords do not match.")
		return
	case len(next) < auth.MinPasswordLength:
		rerender(http.StatusUnprocessableEntity, "The new password must be at least 10 characters long.")
		return
	}

	if err := h.deps.Auth.ChangePassword(c.Request.Context(), user.ID, current, next); err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			rerender(http.StatusUnauthorized, "The current password is incorrect.")
		case errors.Is(err, auth.ErrWeakPassword):
			rerender(http.StatusUnprocessableEntity, "Password too weak: "+err.Error())
		default:
			h.deps.Log.Error("change password", "error", err)
			rerender(http.StatusInternalServerError, "Could not change the password.")
		}
		return
	}

	// Invalidate all other sessions of this user.
	_ = h.deps.Store.DeleteOtherSessions(c.Request.Context(), user.ID, middleware.SessionID(c))
	redirectWithFlash(c, "/settings", "password_changed")
}

// CreateUser adds a viewer or admin account.
func (h *Handler) CreateUser(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")
	role := database.Role(strings.ToUpper(strings.TrimSpace(c.PostForm("role"))))

	_, err := h.deps.Auth.CreateUser(c.Request.Context(), username, password, role)
	if err != nil {
		data := h.settingsData(c)
		switch {
		case errors.Is(err, database.ErrUsernameTaken):
			data.Error = "This username is already taken."
		case errors.Is(err, auth.ErrWeakPassword):
			data.Error = "Password too weak: " + err.Error()
		case errors.Is(err, auth.ErrInvalidUsername):
			data.Error = "Invalid username: " + err.Error()
		default:
			data.Error = "Could not create the user."
			h.deps.Log.Error("create user", "error", err)
		}
		h.page(c, http.StatusUnprocessableEntity, "settings", "Settings", "settings", data)
		return
	}
	redirectWithFlash(c, "/settings", "user_created")
}

// DeleteUser removes an account.
func (h *Handler) DeleteUser(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	id, ok := paramID(c)
	if !ok {
		h.errorPage(c, http.StatusNotFound, "User not found.")
		return
	}
	current, _ := middleware.CurrentUser(c)
	if current != nil && current.ID == id {
		h.errorPage(c, http.StatusBadRequest, "You cannot delete your own account.")
		return
	}
	if err := h.deps.Auth.DeleteUser(c.Request.Context(), id); err != nil {
		if errors.Is(err, auth.ErrLastAdmin) {
			h.errorPage(c, http.StatusConflict, "The last admin cannot be deleted.")
			return
		}
		if errors.Is(err, auth.ErrUserNotFound) {
			h.errorPage(c, http.StatusNotFound, "User not found.")
			return
		}
		h.deps.Log.Error("delete user", "error", err)
		h.errorPage(c, http.StatusInternalServerError, "Could not delete the user.")
		return
	}
	redirectWithFlash(c, "/settings", "user_deleted")
}

// PurgeLogs deletes all execution logs.
func (h *Handler) PurgeLogs(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	if _, err := h.deps.Logs.Purge(c.Request.Context()); err != nil {
		h.deps.Log.Error("purge logs", "error", err)
		h.errorPage(c, http.StatusInternalServerError, "Could not purge the logs.")
		return
	}
	redirectWithFlash(c, "/settings", "logs_purged")
}
