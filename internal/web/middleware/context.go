// Package middleware contains the Gin middleware used by Docktimer.
package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/atpanos/docktimer/internal/database"
	"github.com/gin-gonic/gin"
)

const (
	// CtxUser is the context key for the authenticated user.
	CtxUser = "dt_user"
	// CtxSessionID is the context key for the hashed session ID.
	CtxSessionID = "dt_session_id"
	// CtxSessionToken is the context key for the raw session token.
	CtxSessionToken = "dt_session_token"
	// CtxCSRFNonce is the context key for the CSRF nonce.
	CtxCSRFNonce = "dt_csrf_nonce"
)

// IsHTMX reports whether the request was issued by HTMX.
func IsHTMX(c *gin.Context) bool {
	return strings.EqualFold(c.GetHeader("HX-Request"), "true")
}

// CurrentUser returns the authenticated user.
func CurrentUser(c *gin.Context) (*database.User, bool) {
	v, ok := c.Get(CtxUser)
	if !ok {
		return nil, false
	}
	u, ok := v.(*database.User)
	return u, ok
}

// SessionID returns the hashed session ID.
func SessionID(c *gin.Context) string {
	return c.GetString(CtxSessionID)
}

// SessionToken returns the raw cookie token.
func SessionToken(c *gin.Context) string {
	return c.GetString(CtxSessionToken)
}

// CSRFNonce returns the request's CSRF nonce.
func CSRFNonce(c *gin.Context) string {
	return c.GetString(CtxCSRFNonce)
}

// RequireLogin aborts the request for unauthenticated clients. HTMX requests
// receive a 401 with HX-Redirect instead of a full page.
func RequireLogin(c *gin.Context) {
	if IsHTMX(c) {
		c.Header("HX-Redirect", "/login")
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	next := c.Request.URL.RequestURI()
	c.Redirect(http.StatusSeeOther, "/login?next="+url.QueryEscape(next))
	c.Abort()
}
