package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/atpanos/docktimer/internal/auth"
	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/web/middleware"
	"github.com/gin-gonic/gin"
)

type loginData struct {
	Username string
	Error    string
	Next     string
}

// LoginPage renders the login form.
func (h *Handler) LoginPage(c *gin.Context) {
	needsSetup, err := h.deps.Auth.NeedsSetup(c.Request.Context())
	if err != nil {
		h.errorPage(c, http.StatusInternalServerError, "Datenbankfehler")
		return
	}
	if needsSetup {
		c.Redirect(http.StatusSeeOther, "/setup")
		return
	}
	if token, err := c.Cookie(middleware.SessionCookie); err == nil && token != "" {
		if _, _, err := h.deps.Auth.SessionFromToken(c.Request.Context(), token); err == nil {
			c.Redirect(http.StatusSeeOther, "/")
			return
		}
	}
	h.page(c, http.StatusOK, "login", "Sign in", "", loginData{Next: safeNext(c.Query("next"))})
}

// LoginPost authenticates a user.
func (h *Handler) LoginPost(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")
	next := safeNext(c.PostForm("next"))

	token, user, err := h.deps.Auth.Login(c.Request.Context(), username, password,
		c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		var rl *auth.RateLimitError
		if errors.As(err, &rl) {
			seconds := int(rl.RetryAfter.Seconds()) + 1
			c.Header("Retry-After", strconv.Itoa(seconds))
			h.page(c, http.StatusTooManyRequests, "login", "Sign in", "", loginData{
				Username: username,
				Error:    "Too many attempts. Try again in " + rl.RetryAfter.Round(time.Second).String() + ".",
				Next:     next,
			})
			return
		}
		if errors.Is(err, auth.ErrInvalidCredentials) {
			h.page(c, http.StatusUnauthorized, "login", "Sign in", "", loginData{
				Username: username,
				Error:    "Invalid username or password.",
				Next:     next,
			})
			return
		}
		h.deps.Log.Error("login failed", "error", err)
		h.errorPage(c, http.StatusInternalServerError, "Sign-in failed.")
		return
	}

	h.setSessionCookie(c, token)
	_ = user
	target := next
	if target == "" {
		target = "/"
	}
	c.Redirect(http.StatusSeeOther, target)
}

// LogoutPage logs the current user out.
func (h *Handler) LogoutPage(c *gin.Context) {
	h.deps.Auth.Logout(c.Request.Context(), middleware.SessionToken(c))
	h.clearSessionCookie(c)
	c.Redirect(http.StatusSeeOther, "/login?flash=logged_out")
}

// SetupPage renders the first-run wizard.
func (h *Handler) SetupPage(c *gin.Context) {
	needsSetup, err := h.deps.Auth.NeedsSetup(c.Request.Context())
	if err != nil {
		h.errorPage(c, http.StatusInternalServerError, "Datenbankfehler")
		return
	}
	if !needsSetup {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	h.page(c, http.StatusOK, "setup", "Initial setup", "", map[string]any{})
}

// SetupPost creates the initial admin account.
func (h *Handler) SetupPost(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")
	confirm := c.PostForm("confirm")

	if password != confirm {
		h.page(c, http.StatusUnprocessableEntity, "setup", "Initial setup", "", map[string]any{
			"Username": username,
			"Error":    "The passwords do not match.",
		})
		return
	}

	token, _, err := h.deps.Auth.Setup(c.Request.Context(), username, password,
		c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		msg := "Setup failed."
		switch {
		case errors.Is(err, auth.ErrWeakPassword):
			msg = "Password too weak: " + err.Error()
		case errors.Is(err, auth.ErrInvalidUsername):
			msg = "Invalid username: " + err.Error()
		case errors.Is(err, auth.ErrSetupAlreadyDone):
			msg = "Setup has already been completed."
		case errors.Is(err, database.ErrUsernameTaken):
			msg = "This username is already taken."
		}
		h.page(c, http.StatusUnprocessableEntity, "setup", "Initial setup", "", map[string]any{
			"Username": username,
			"Error":    msg,
		})
		return
	}

	h.setSessionCookie(c, token)
	redirectWithFlash(c, "/", "setup_done")
}

func (h *Handler) setSessionCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     middleware.SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(h.deps.Cfg.SessionTTL.Seconds()),
		Expires:  time.Now().Add(h.deps.Cfg.SessionTTL),
		HttpOnly: true,
		Secure:   h.deps.Cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     middleware.SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.deps.Cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

// safeNext only allows local absolute paths to prevent open redirects.
func safeNext(next string) string {
	if next == "" || next[0] != '/' || (len(next) > 1 && next[1] == '/') {
		return ""
	}
	return next
}
