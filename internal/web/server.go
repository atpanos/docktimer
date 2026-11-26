// Package web wires the HTTP router, middleware and handlers together.
package web

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/atpanos/docktimer/internal/auth"
	"github.com/atpanos/docktimer/internal/config"
	"github.com/atpanos/docktimer/internal/web/handlers"
	"github.com/atpanos/docktimer/internal/web/middleware"
	"github.com/gin-gonic/gin"
)

// Server owns the HTTP server.
type Server struct {
	cfg  *config.Config
	log  *slog.Logger
	http *http.Server
}

// New builds the router and the HTTP server.
func New(cfg *config.Config, log *slog.Logger, h *handlers.Handler, authSvc *auth.Service, assets fs.FS) (*Server, error) {
	if cfg.DevMode {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.RedirectTrailingSlash = false

	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return nil, err
	}

	r.Use(
		middleware.Recover(log),
		middleware.RequestID(),
		middleware.SecurityHeaders(cfg.SecureCookies()),
		middleware.Logger(log),
		middleware.BodyLimit(1<<20),
	)

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	static := r.Group("/static", cacheControl)
	static.StaticFS("/", http.FS(staticFS))

	r.GET("/healthz", h.Healthz)
	r.GET("/readyz", h.Readyz)

	csrf := authSvc.CSRF()

	public := r.Group("/")
	public.Use(middleware.CSRF(csrf, cfg.SecureCookies()))
	{
		public.GET("/login", h.LoginPage)
		public.POST("/login", h.LoginPost)
		public.GET("/setup", h.SetupPage)
		public.POST("/setup", h.SetupPost)
	}

	protected := r.Group("/")
	protected.Use(middleware.Auth(authSvc), middleware.CSRF(csrf, cfg.SecureCookies()))
	{
		protected.GET("/", h.Dashboard)
		protected.POST("/logout", h.LogoutPage)

		protected.GET("/containers", h.ContainersPage)
		protected.GET("/containers/table", h.ContainersTable)
		protected.POST("/containers/:id/:action", h.ContainerAction)

		protected.GET("/rules", h.RulesPage)
		protected.GET("/rules/new", h.NewRulePage)
		protected.POST("/rules", h.CreateRule)
		protected.GET("/rules/:id/edit", h.EditRulePage)
		protected.POST("/rules/:id", h.UpdateRule)
		protected.POST("/rules/:id/toggle", h.ToggleRule)
		protected.POST("/rules/:id/delete", h.DeleteRule)

		protected.GET("/logs", h.LogsPage)

		protected.GET("/settings", h.SettingsPage)
		protected.POST("/settings/password", h.ChangePassword)
		protected.POST("/settings/users", h.CreateUser)
		protected.POST("/settings/users/:id/delete", h.DeleteUser)
		protected.POST("/settings/logs/purge", h.PurgeLogs)
	}

	r.NoRoute(h.NotFound)
	r.NoMethod(h.NotFound)

	srv := &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return &Server{cfg: cfg, log: log, http: srv}, nil
}

// Handler exposes the router (used by tests).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run starts the server and shuts it down gracefully when ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s.log.Info("shutting down http server")
	return s.http.Shutdown(shutdownCtx)
}

func cacheControl(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Next()
}
