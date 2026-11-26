package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	env := map[string]string{
		"SESSION_SECRET": strings.Repeat("s", 40),
	}
	cfg, err := load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppPort != "8080" {
		t.Errorf("AppPort = %q", cfg.AppPort)
	}
	if cfg.StopTimeout != 30*time.Second {
		t.Errorf("StopTimeout = %v", cfg.StopTimeout)
	}
	if cfg.LogRetention != 10000 {
		t.Errorf("LogRetention = %d", cfg.LogRetention)
	}
	if cfg.SecureCookies() {
		t.Error("http URL must not force secure cookies")
	}
}

func TestLoadRejectsPlaceholderSecret(t *testing.T) {
	_, err := load(func(k string) string {
		if k == "SESSION_SECRET" {
			return "change-me"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected error for placeholder secret")
	}
}

func TestLoadAllowsDevModeFallback(t *testing.T) {
	cfg, err := load(func(k string) string {
		if k == "DEV_MODE" {
			return "1"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.DevMode {
		t.Error("DevMode should be true")
	}
}

func TestLoadInvalidTimezoneAndPort(t *testing.T) {
	env := map[string]string{
		"SESSION_SECRET":   strings.Repeat("s", 40),
		"APP_PORT":         "70000",
		"DEFAULT_TIMEZONE": "Mars/Olympus",
		"COOKIE_SECURE":    "maybe",
		"LOG_LEVEL":        "verbose",
	}
	_, err := load(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"APP_PORT", "DEFAULT_TIMEZONE", "COOKIE_SECURE", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestSecureCookiesFromURL(t *testing.T) {
	env := map[string]string{
		"SESSION_SECRET": strings.Repeat("s", 40),
		"APP_URL":        "https://docktimer.example",
	}
	cfg, err := load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SecureCookies() {
		t.Error("https APP_URL must enable secure cookies")
	}
}

func TestCSV(t *testing.T) {
	got := csv(" docking, ,proxy ")
	if len(got) != 2 || got[0] != "docking" || got[1] != "proxy" {
		t.Fatalf("unexpected csv result: %#v", got)
	}
}
