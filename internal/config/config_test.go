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
	for _, name := range []string{"AUTH_REQUESTS_PER_MINUTE", "TRUSTED_PROXY_CIDRS", "CHROMIUM_PATH", "BROWSER_PROFILE_DIR", "BROWSER_MAX_SESSIONS", "BROWSER_SESSION_TTL", "BROWSER_STARTUP_TIMEOUT", "PUBLIC_API_URL", "REDIS_URL", "REDIS_NAMESPACE", "WORKER_ID", "WORKER_HTTP_ADDR", "WORKER_URL", "WORKER_AUTH_TOKEN", "WORKER_LEASE_TTL"} {
		t.Setenv(name, "")
	}
}

func TestLoadDefaultsAndOverrides(t *testing.T) {
	setValidEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthRequestsPerMinute != 30 || len(cfg.TrustedProxies) != 0 || cfg.HTTPAddr != ":8080" || cfg.DBMaxConns != 20 || cfg.AuthTimeout != 2*time.Second || cfg.SessionTTL != 24*time.Hour || cfg.BrowserMaxSessions != 10 || cfg.BrowserSessionTTL != 15*time.Minute || cfg.BrowserStartupTimeout != 10*time.Second || cfg.RedisURL != "redis://localhost:6930/0" || cfg.WorkerLeaseTTL != 15*time.Second || len(cfg.WorkerAuthToken) < 32 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	t.Setenv("HTTP_ADDR", "127.0.0.1:9000")
	t.Setenv("DB_MAX_CONNS", "50")
	t.Setenv("AUTH_TIMEOUT", "500ms")
	t.Setenv("SESSION_TTL", "12h")
	t.Setenv("BROWSER_MAX_SESSIONS", "3")
	t.Setenv("BROWSER_SESSION_TTL", "30m")
	t.Setenv("BROWSER_STARTUP_TIMEOUT", "20s")
	t.Setenv("CHROMIUM_PATH", "/bin/chromium")
	t.Setenv("BROWSER_PROFILE_DIR", "/tmp/profiles")
	t.Setenv("PUBLIC_API_URL", "https://browsers.example.com")
	cfg, err = Load()
	if err != nil || cfg.HTTPAddr != "127.0.0.1:9000" || cfg.DBMaxConns != 50 || cfg.AuthTimeout != 500*time.Millisecond || cfg.SessionTTL != 12*time.Hour || cfg.BrowserMaxSessions != 3 || cfg.BrowserSessionTTL != 30*time.Minute || cfg.BrowserStartupTimeout != 20*time.Second || cfg.ChromiumPath != "/bin/chromium" || cfg.BrowserProfileDir != "/tmp/profiles" || cfg.PublicAPIURL != "https://browsers.example.com" {
		t.Fatalf("overrides failed: %v", err)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DATABASE_URL", ""}, {"API_KEY_PEPPER", ""}, {"API_KEY_PEPPER", "not-base64"},
		{"API_KEY_PEPPER", base64.StdEncoding.EncodeToString([]byte("short"))},
		{"DB_MAX_CONNS", "0"}, {"DB_MAX_CONNS", "-2"}, {"DB_MAX_CONNS", "2147483648"},
		{"AUTH_REQUESTS_PER_MINUTE", "0"}, {"AUTH_REQUESTS_PER_MINUTE", "bad"}, {"AUTH_REQUESTS_PER_MINUTE", "1000001"}, {"TRUSTED_PROXY_CIDRS", "127.0.0.1"}, {"TRUSTED_PROXY_CIDRS", "bad"},
		{"AUTH_TIMEOUT", "0s"}, {"AUTH_TIMEOUT", "-1s"}, {"AUTH_TIMEOUT", "bad"},
		{"BROWSER_MAX_SESSIONS", "0"}, {"BROWSER_MAX_SESSIONS", "bad"},
		{"BROWSER_SESSION_TTL", "-1s"}, {"BROWSER_SESSION_TTL", "25h"},
		{"BROWSER_STARTUP_TIMEOUT", "bad"}, {"BROWSER_STARTUP_TIMEOUT", "0s"},
		{"PUBLIC_API_URL", "ws://localhost"}, {"PUBLIC_API_URL", "https://user:pass@example.com"},
		{"PUBLIC_API_URL", "https://example.com/path"}, {"PUBLIC_API_URL", "https://example.com?key=secret"},
		{"PUBLIC_API_URL", "https://example.com#fragment"}, {"PUBLIC_API_URL", "https://"},
		{"REDIS_URL", "http://localhost:6379"}, {"REDIS_URL", "redis://"},
		{"REDIS_NAMESPACE", "unsafe:namespace"}, {"WORKER_ID", "invalid/worker"},
		{"WORKER_URL", "http://user:pass@localhost"}, {"WORKER_URL", "http://localhost/path"},
		{"WORKER_URL", "ws://localhost"}, {"WORKER_AUTH_TOKEN", "short"},
		{"WORKER_LEASE_TTL", "1s"}, {"WORKER_LEASE_TTL", "6m"},
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

func TestWorkerCredentialDerivationAndOverrides(t *testing.T) {
	setValidEnv(t)
	first, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load()
	if err != nil || first.WorkerAuthToken != second.WorkerAuthToken {
		t.Fatal("worker credential is not stable")
	}
	t.Setenv("API_KEY_PEPPER", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("y", 32))))
	changed, err := Load()
	if err != nil || changed.WorkerAuthToken == first.WorkerAuthToken {
		t.Fatal("worker credential ignored pepper")
	}
	t.Setenv("WORKER_AUTH_TOKEN", strings.Repeat("s", 32))
	t.Setenv("WORKER_ID", "replica-2")
	t.Setenv("WORKER_URL", "http://127.0.0.1:8091")
	t.Setenv("WORKER_HTTP_ADDR", "127.0.0.1:8091")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:7000/1")
	t.Setenv("WORKER_LEASE_TTL", "30s")
	cfg, err := Load()
	if err != nil || cfg.WorkerAuthToken != strings.Repeat("s", 32) || cfg.WorkerID != "replica-2" || cfg.WorkerURL != "http://127.0.0.1:8091" || cfg.WorkerHTTPAddr != "127.0.0.1:8091" || cfg.RedisURL != "redis://127.0.0.1:7000/1" || cfg.WorkerLeaseTTL != 30*time.Second {
		t.Fatal("worker overrides failed", err)
	}
}

func TestAdmissionConfigOverrides(t *testing.T) {
	setValidEnv(t)
	t.Setenv("AUTH_REQUESTS_PER_MINUTE", "60")
	t.Setenv("TRUSTED_PROXY_CIDRS", "127.0.0.0/8, 10.0.0.0/24")
	cfg, err := Load()
	if err != nil || cfg.AuthRequestsPerMinute != 60 || len(cfg.TrustedProxies) != 2 {
		t.Fatalf("admission overrides failed: %v", err)
	}
}
