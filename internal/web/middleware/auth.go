package middleware

import (
	"errors"
	"net/http"

	"github.com/atpanos/docktimer/internal/auth"
	"github.com/gin-gonic/gin"
)

// SessionCookie is the name of the session cookie.
const SessionCookie = "docktimer_session"

// Auth resolves the session cookie and stores the user in the context.
func Auth(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(SessionCookie)
		if err != nil || token == "" {
			RequireLogin(c)
			return
		}
		user, sessionID, err := svc.SessionFromToken(c.Request.Context(), token)
		if err != nil {
			if errors.Is(err, auth.ErrSessionInvalid) {
				clearSessionCookie(c)
				RequireLogin(c)
				return
			}
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		u := user
		c.Set(CtxUser, &u)
		c.Set(CtxSessionID, sessionID)
		c.Set(CtxSessionToken, token)
		c.Next()
	}
}

func clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
