package middleware

import (
	"net/http"

	"github.com/atpanos/docktimer/internal/auth"
	"github.com/gin-gonic/gin"
)

// CSRF validates the double-submit token on every state-changing request and
// ensures a nonce cookie exists for form rendering.
func CSRF(m *auth.CSRF, secure bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		nonce, err := c.Cookie(auth.CSRFCookieName)
		if err != nil || nonce == "" {
			nonce = m.NewNonce()
			http.SetCookie(c.Writer, &http.Cookie{
				Name:     auth.CSRFCookieName,
				Value:    nonce,
				Path:     "/",
				MaxAge:   24 * 60 * 60,
				HttpOnly: true,
				Secure:   secure,
				SameSite: http.SameSiteLaxMode,
			})
		}
		c.Set(CtxCSRFNonce, nonce)

		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		token := c.PostForm("csrf_token")
		if token == "" {
			token = c.GetHeader("X-CSRF-Token")
		}
		if !m.Validate(token, nonce, SessionID(c)) {
			if IsHTMX(c) {
				c.Header("HX-Reswap", "none")
				c.Header("HX-Trigger", `{"toast":{"message":"Session expired. Please reload the page.","type":"error"}}`)
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}
