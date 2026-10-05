package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("API_KEY_PEPPER", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("DB_MAX_CONNS", "")
	t.Setenv("AUTH_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
}

func TestLoadDefaultsAndOverrides(t *testing.T) {
	setValidEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.DBMaxConns != 20 || cfg.AuthTimeout != 2*time.Second || cfg.SessionTTL != 24*time.Hour {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	t.Setenv("HTTP_ADDR", "127.0.0.1:9000")
	t.Setenv("DB_MAX_CONNS", "50")
	t.Setenv("AUTH_TIMEOUT", "500ms")
	t.Setenv("SESSION_TTL", "12h")
	cfg, err = Load()
	if err != nil || cfg.HTTPAddr != "127.0.0.1:9000" || cfg.DBMaxConns != 50 || cfg.AuthTimeout != 500*time.Millisecond || cfg.SessionTTL != 12*time.Hour {
		t.Fatalf("overrides failed: %v", err)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DATABASE_URL", ""}, {"API_KEY_PEPPER", ""}, {"API_KEY_PEPPER", "not-base64"},
		{"API_KEY_PEPPER", base64.StdEncoding.EncodeToString([]byte("short"))},
		{"DB_MAX_CONNS", "0"}, {"DB_MAX_CONNS", "-2"}, {"DB_MAX_CONNS", "2147483648"},
		{"AUTH_TIMEOUT", "0s"}, {"AUTH_TIMEOUT", "-1s"}, {"AUTH_TIMEOUT", "bad"},
		{"SESSION_TTL", "0s"}, {"SESSION_TTL", "-1s"}, {"SESSION_TTL", "bad"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			setValidEnv(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
