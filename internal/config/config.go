// Package config loads and validates the application configuration from
// environment variables. It never reads files and never logs secrets.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the immutable configuration of a Docktimer instance.
type Config struct {
	AppPort            string
	DatabasePath       string
	SessionSecret      string
	DefaultTimezone    string
	DockerHost         string
	StopTimeout        time.Duration
	LogRetention       int
	PerContainerLogCap int
	AppURL             string
	CookieSecure       string // "auto", "true", "false"
	SessionTTL         time.Duration
	SessionIdleTTL     time.Duration
	TrustedProxies     []string
	ContainerAllowlist []string
	ContainerDenylist  []string
	LogLevel           string
	DevMode            bool
}

// insecureSecret is the placeholder shipped in .env.example.
const insecureSecret = "change-me"

// Load reads the configuration from the process environment.
func Load() (*Config, error) {
	return load(os.Getenv)
}

func load(get func(string) string) (*Config, error) {
	cfg := &Config{
		AppPort:            env(get, "APP_PORT", "8080"),
		DatabasePath:       env(get, "DATABASE_PATH", "/data/docktimer.db"),
		SessionSecret:      get("SESSION_SECRET"),
		DefaultTimezone:    env(get, "DEFAULT_TIMEZONE", "Europe/Berlin"),
		DockerHost:         env(get, "DOCKER_HOST", "unix:///var/run/docker.sock"),
		AppURL:             env(get, "APP_URL", "http://localhost:8080"),
		CookieSecure:       strings.ToLower(env(get, "COOKIE_SECURE", "auto")),
		TrustedProxies:     csv(get("TRUSTED_PROXIES")),
		ContainerAllowlist: csv(get("CONTAINER_ALLOWLIST")),
		ContainerDenylist:  csv(get("CONTAINER_DENYLIST")),
		LogLevel:           strings.ToLower(env(get, "LOG_LEVEL", "info")),
		PerContainerLogCap: envInt(get, "LOG_RETENTION_PER_CONTAINER", 0),
		DevMode:            envBool(get, "DEV_MODE", false),
	}

	var errs []error

	if p := cfg.AppPort; p == "" {
		errs = append(errs, errors.New("APP_PORT must not be empty"))
	} else if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
		errs = append(errs, fmt.Errorf("APP_PORT %q is not a valid TCP port", p))
	}

	if cfg.DatabasePath == "" {
		errs = append(errs, errors.New("DATABASE_PATH must not be empty"))
	}

	switch {
	case len(cfg.SessionSecret) < 32:
		if !cfg.DevMode {
			errs = append(errs, errors.New("SESSION_SECRET must be at least 32 characters; generate one with: openssl rand -base64 48"))
		}
	case cfg.SessionSecret == insecureSecret:
		if !cfg.DevMode {
			errs = append(errs, errors.New("SESSION_SECRET is still the insecure placeholder; set a real secret or enable DEV_MODE=1"))
		}
	}

	cfg.StopTimeout = time.Duration(envInt(get, "CONTAINER_STOP_TIMEOUT", 30)) * time.Second
	if cfg.StopTimeout < time.Second || cfg.StopTimeout > 10*time.Minute {
		errs = append(errs, errors.New("CONTAINER_STOP_TIMEOUT must be between 1 and 600 seconds"))
	}

	cfg.LogRetention = envInt(get, "LOG_RETENTION", 10000)
	if cfg.LogRetention < 100 {
		cfg.LogRetention = 1000
	}
	if cfg.PerContainerLogCap < 0 {
		cfg.PerContainerLogCap = 0
	}

	cfg.SessionTTL = time.Duration(envInt(get, "SESSION_TTL_HOURS", 12)) * time.Hour
	if cfg.SessionTTL < time.Hour {
		errs = append(errs, errors.New("SESSION_TTL_HOURS must be at least 1"))
	}
	cfg.SessionIdleTTL = time.Duration(envInt(get, "SESSION_IDLE_MINUTES", 60)) * time.Minute
	if cfg.SessionIdleTTL < 5*time.Minute {
		errs = append(errs, errors.New("SESSION_IDLE_MINUTES must be at least 5"))
	}

	if _, err := time.LoadLocation(cfg.DefaultTimezone); err != nil {
		errs = append(errs, fmt.Errorf("DEFAULT_TIMEZONE %q is not a valid IANA timezone", cfg.DefaultTimezone))
	}

	switch cfg.CookieSecure {
	case "auto", "true", "false":
	default:
		errs = append(errs, fmt.Errorf("COOKIE_SECURE must be auto, true or false, got %q", cfg.CookieSecure))
	}

	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", cfg.LogLevel))
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SecureCookies reports whether the session and CSRF cookies must use the
// Secure attribute. In "auto" mode the scheme of APP_URL decides.
func (c *Config) SecureCookies() bool {
	switch c.CookieSecure {
	case "true":
		return true
	case "false":
		return false
	default:
		return strings.HasPrefix(strings.ToLower(c.AppURL), "https://")
	}
}

func env(get func(string) string, key, def string) string {
	if v := strings.TrimSpace(get(key)); v != "" {
		return v
	}
	return def
}

func envInt(get func(string) string, key string, def int) int {
	raw := strings.TrimSpace(get(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func envBool(get func(string) string, key string, def bool) bool {
	raw := strings.ToLower(strings.TrimSpace(get(key)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func csv(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
