// Command server starts Docktimer.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embed the IANA database so alpine/distroless images work

	"github.com/atpanos/docktimer/internal/auth"
	"github.com/atpanos/docktimer/internal/config"
	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/docker"
	"github.com/atpanos/docktimer/internal/logging"
	"github.com/atpanos/docktimer/internal/rules"
	"github.com/atpanos/docktimer/internal/scheduler"
	"github.com/atpanos/docktimer/internal/web"
	"github.com/atpanos/docktimer/internal/web/handlers"
	"github.com/atpanos/docktimer/internal/web/templates"
	"github.com/atpanos/docktimer/internal/web/view"
	webassets "github.com/atpanos/docktimer/web"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("docktimer stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("close database", "error", err)
		}
	}()

	api, err := docker.NewEngine(cfg.DockerHost)
	if err != nil {
		return err
	}
	dockerSvc := docker.NewService(api, cfg.StopTimeout, cfg.ContainerAllowlist,
		cfg.ContainerDenylist, selfHostname(), logger)

	csrf := auth.NewCSRF(cfg.SessionSecret)
	limiter := auth.NewLimiter(5, 15*time.Minute)
	authSvc := auth.NewService(store, csrf, limiter, cfg.SessionTTL, cfg.SessionIdleTTL)
	rulesSvc := rules.NewService(store)
	logSvc := logging.NewService(store, cfg.LogRetention, cfg.PerContainerLogCap, logger)

	sched := scheduler.New(dockerSvc, store, logSvc, logger)
	defer sched.Stop()
	if err := sched.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}

	renderer, err := view.New(templates.FS, cfg.DevMode)
	if err != nil {
		return err
	}

	h := handlers.New(handlers.Deps{
		Cfg:       cfg,
		Auth:      authSvc,
		Store:     store,
		Docker:    dockerSvc,
		Rules:     rulesSvc,
		Logs:      logSvc,
		Scheduler: sched,
		Renderer:  renderer,
		Log:       logger,
		Version:   version,
	})

	srv, err := web.New(cfg, logger, h, authSvc, webassets.FS)
	if err != nil {
		return err
	}

	go cleanupLoop(ctx, authSvc, store, logger)

	logger.Info("docktimer starting", "version", version, "docker_host", cfg.DockerHost,
		"database", cfg.DatabasePath, "timezone", cfg.DefaultTimezone)

	if err := srv.Run(ctx); err != nil {
		return err
	}
	return nil
}

func cleanupLoop(ctx context.Context, authSvc *auth.Service, store *database.Store, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			authSvc.Cleanup(ctx)
			if _, err := store.DB().ExecContext(ctx, "PRAGMA optimize"); err != nil {
				log.Warn("database optimize failed", "error", err)
			}
		}
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch strings.ToLower(a.Key) {
			case "password", "token", "secret", "session", "csrf":
				return slog.String(a.Key, "[redacted]")
			}
			return a
		},
	}
	if cfg.DevMode {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func selfHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
