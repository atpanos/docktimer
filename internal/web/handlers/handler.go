// Package handlers implements the HTTP endpoints of Docktimer.
package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/atpanos/docktimer/internal/auth"
	"github.com/atpanos/docktimer/internal/config"
	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/docker"
	"github.com/atpanos/docktimer/internal/logging"
	"github.com/atpanos/docktimer/internal/rules"
	"github.com/atpanos/docktimer/internal/scheduler"
	"github.com/atpanos/docktimer/internal/web/middleware"
	"github.com/atpanos/docktimer/internal/web/view"
	"github.com/gin-gonic/gin"
)

// Deps bundles the handler dependencies.
type Deps struct {
	Cfg       *config.Config
	Auth      *auth.Service
	Store     *database.Store
	Docker    *docker.Service
	Rules     *rules.Service
	Logs      *logging.Service
	Scheduler *scheduler.Scheduler
	Renderer  *view.Renderer
	Log       *slog.Logger
	Version   string
}

// Handler holds all HTTP handlers.
type Handler struct {
	deps Deps
}

// New creates a handler set.
func New(d Deps) *Handler {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Handler{deps: d}
}

// page renders a full page with the shared layout.
func (h *Handler) page(c *gin.Context, status int, name, title, nav string, data any) {
	u, _ := middleware.CurrentUser(c)
	nonce := middleware.CSRFNonce(c)
	sessionID := middleware.SessionID(c)

	pd := view.PageData{
		Title:   title,
		Nav:     nav,
		User:    u,
		CSRF:    h.deps.Auth.CSRF().Token(nonce, sessionID),
		Flash:   view.FlashText(c.Query("flash")),
		TZ:      h.deps.Cfg.DefaultTimezone,
		Version: h.deps.Version,
		Data:    data,
	}
	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.deps.Renderer.Page(c.Writer, name, pd); err != nil {
		h.deps.Log.Error("render page failed", "page", name, "error", err)
		if !c.Writer.Written() {
			c.String(http.StatusInternalServerError, "Template error")
		}
	}
}

// fragment renders an HTMX partial.
func (h *Handler) fragment(c *gin.Context, status int, name string, data any) {
	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.deps.Renderer.Fragment(c.Writer, name, data); err != nil {
		h.deps.Log.Error("render fragment failed", "fragment", name, "error", err)
		c.String(http.StatusInternalServerError, "Template error")
	}
}

// errorPage renders the error page.
func (h *Handler) errorPage(c *gin.Context, status int, message string) {
	h.page(c, status, "error", "Error", "", map[string]any{
		"Status":  status,
		"Message": message,
	})
	c.Abort()
}

// requireAdmin enforces the ADMIN role server-side.
func (h *Handler) requireAdmin(c *gin.Context) bool {
	u, ok := middleware.CurrentUser(c)
	if !ok || !u.IsAdmin() {
		if isHTMX(c) {
			h.toast(c, "error", "This action requires an admin account.")
			c.Header("HX-Reswap", "none")
			c.Status(http.StatusForbidden)
			c.Abort()
			return false
		}
		h.errorPage(c, http.StatusForbidden, "This action requires an admin account.")
		return false
	}
	return true
}

// toast sends a transient message to the UI via HTMX.
func (h *Handler) toast(c *gin.Context, typ, message string) {
	payload, _ := json.Marshal(map[string]any{
		"toast": map[string]string{"type": typ, "message": message},
	})
	c.Header("HX-Trigger", string(payload))
}

// isHTMX reports whether the request came from HTMX.
func isHTMX(c *gin.Context) bool { return middleware.IsHTMX(c) }

func paramID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func redirectWithFlash(c *gin.Context, path, code string) {
	c.Redirect(http.StatusSeeOther, path+"?flash="+code)
}

// dockerErrorText maps Docker errors to German UI messages.
func dockerErrorText(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, docker.ErrUnavailable):
		return "Docker daemon is not reachable. Check the socket or the socket proxy."
	case errors.Is(err, docker.ErrNotFound):
		return "Container was not found."
	case errors.Is(err, docker.ErrConflict):
		return "The container is in a state that does not allow this action."
	case errors.Is(err, docker.ErrNotAllowed):
		return "This container is not managed by Docktimer."
	case errors.Is(err, docker.ErrTimeout):
		return "The Docker action took too long."
	default:
		return "Docker error: " + err.Error()
	}
}

// actionText returns the German verb for an action.
func actionText(a database.Action) string {
	switch a {
	case database.ActionStart:
		return "started"
	case database.ActionStop:
		return "stopped"
	case database.ActionRestart:
		return "restarted"
	default:
		return "updated"
	}
}
