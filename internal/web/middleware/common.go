package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders sets conservative headers for a self-hosted app. The CSP
// allows only same-origin assets; Docktimer uses no inline scripts or styles.
func SecurityHeaders(secure bool) gin.HandlerFunc {
	policy := "default-src 'self'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data:; connect-src 'self'; font-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if secure {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// RequestID assigns a request ID and echoes it back.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err == nil {
			c.Set("dt_request_id", hex.EncodeToString(b))
			c.Header("X-Request-ID", hex.EncodeToString(b))
		}
		c.Next()
	}
}

// Logger logs one structured line per request, without query strings.
func Logger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		attrs := []any{
			"request_id", c.GetString("dt_request_id"),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_ip", c.ClientIP(),
		}
		if u, ok := CurrentUser(c); ok {
			attrs = append(attrs, "user_id", u.ID)
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "error", c.Errors.Last().Error())
		}
		log.Info("http request", attrs...)
	}
}

// Recover converts panics into a 500 response without leaking details.
func Recover(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic in handler", "panic", fmt.Sprint(rec), "path", c.Request.URL.Path)
				if !c.Writer.Written() {
					c.Header("Content-Type", "text/html; charset=utf-8")
					c.String(http.StatusInternalServerError, "<h1>500 — Internal Server Error</h1>")
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

// BodyLimit rejects request bodies larger than n bytes.
func BodyLimit(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		}
		c.Next()
	}
}
