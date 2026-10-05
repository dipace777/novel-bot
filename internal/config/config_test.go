package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func setValidEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"DATABASE_URL", "DB_MAX_CONNS", "API_KEY_PEPPER", "HTTP_ADDR", "AUTH_TIMEOUT", "SESSION_TTL", "SESSION_STARTUP_TIMEOUT", "METRICS_SAMPLE_INTERVAL", "AUTH_REQUESTS_PER_MINUTE", "TRUSTED_PROXY_CIDRS", "CHROMIUM_PATH", "BROWSER_PROFILE_DIR", "BROWSER_MAX_SESSIONS", "BROWSER_SESSION_TTL", "BROWSER_STARTUP_TIMEOUT", "PUBLIC_API_URL", "REDIS_URL", "REDIS_NAMESPACE", "WORKER_ID", "WORKER_HTTP_ADDR", "WORKER_URL", "WORKER_AUTH_TOKEN", "WORKER_LEASE_TTL"} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("API_KEY_PEPPER", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
	t.Setenv("WORKER_AUTH_TOKEN", strings.Repeat("s", 32))
}
func TestRoleDefaultsAndOverrides(t *testing.T) {
	setValidEnv(t)
	api, err := LoadAPI()
	if err != nil {
		t.Fatal(err)
	}
	if api.HTTPAddr != ":8080" || api.DBMaxConns != 20 || api.AuthTimeout != 2*time.Second || api.SessionTTL != 24*time.Hour || api.SessionStartupTimeout != 10*time.Second || api.AuthRequestsPerMinute != 30 || len(api.TrustedProxies) != 0 {
		t.Fatal("unexpected API defaults")
	}
	worker, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if worker.BrowserMaxSessions != 6 || worker.BrowserSessionTTL != 15*time.Minute || worker.BrowserStartupTimeout != 10*time.Second || worker.WorkerLeaseTTL != 15*time.Second || worker.RedisURL != "redis://localhost:6930/0" || worker.MetricsSampleInterval != 5*time.Second {
		t.Fatal("unexpected worker defaults")
	}
	for name, v := range map[string]string{"HTTP_ADDR": "127.0.0.1:9000", "DB_MAX_CONNS": "50", "AUTH_TIMEOUT": "500ms", "SESSION_TTL": "12h", "SESSION_STARTUP_TIMEOUT": "20s", "PUBLIC_API_URL": "https://browsers.example.com", "AUTH_REQUESTS_PER_MINUTE": "60", "TRUSTED_PROXY_CIDRS": "127.0.0.0/8, 10.0.0.0/24", "WORKER_ID": "worker-2", "WORKER_URL": "http://127.0.0.1:8091", "WORKER_HTTP_ADDR": "127.0.0.1:8091", "REDIS_URL": "redis://localhost:7000/1", "WORKER_LEASE_TTL": "30s", "BROWSER_MAX_SESSIONS": "3", "BROWSER_SESSION_TTL": "30m", "BROWSER_STARTUP_TIMEOUT": "20s", "CHROMIUM_PATH": "/bin/chromium", "BROWSER_PROFILE_DIR": "/tmp/profiles", "METRICS_SAMPLE_INTERVAL": "1s"} {
		t.Setenv(name, v)
	}
	api, err = LoadAPI()
	if err != nil || api.HTTPAddr != "127.0.0.1:9000" || api.DBMaxConns != 50 || api.AuthTimeout != 500*time.Millisecond || api.SessionTTL != 12*time.Hour || api.SessionStartupTimeout != 20*time.Second || api.PublicAPIURL != "https://browsers.example.com" || api.AuthRequestsPerMinute != 60 || len(api.TrustedProxies) != 2 {
		t.Fatal("API overrides failed", err)
	}
	worker, err = LoadWorker()
	if err != nil || worker.WorkerID != "worker-2" || worker.WorkerHTTPAddr != "127.0.0.1:8091" || worker.WorkerURL != "http://127.0.0.1:8091" || worker.WorkerLeaseTTL != 30*time.Second || worker.BrowserMaxSessions != 3 || worker.BrowserSessionTTL != 30*time.Minute || worker.BrowserStartupTimeout != 20*time.Second || worker.ChromiumPath != "/bin/chromium" || worker.BrowserProfileDir != "/tmp/profiles" || worker.MetricsSampleInterval != time.Second {
		t.Fatal("worker overrides failed", err)
	}
}
func TestRolesIgnoreUnrelatedConfiguration(t *testing.T) {
	setValidEnv(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("API_KEY_PEPPER", "invalid")
	t.Setenv("DB_MAX_CONNS", "invalid")
	t.Setenv("AUTH_TIMEOUT", "invalid")
	t.Setenv("PUBLIC_API_URL", "invalid")
	if _, err := LoadWorker(); err != nil {
		t.Fatal("worker requires API settings", err)
	}
	setValidEnv(t)
	for _, name := range []string{"BROWSER_MAX_SESSIONS", "BROWSER_STARTUP_TIMEOUT", "WORKER_LEASE_TTL", "WORKER_URL", "WORKER_ID", "METRICS_SAMPLE_INTERVAL"} {
		t.Setenv(name, "invalid")
	}
	if _, err := LoadAPI(); err != nil {
		t.Fatal("API requires worker settings", err)
	}
	t.Setenv("API_KEY_PEPPER", "")
	t.Setenv("WORKER_AUTH_TOKEN", "")
	t.Setenv("REDIS_URL", "invalid")
	if _, err := LoadDatabase(); err != nil {
		t.Fatal("migrations require non-database settings", err)
	}
}
func TestRolesRejectInvalidConfiguration(t *testing.T) {
	apiOnly := map[string][]string{
		"DATABASE_URL": {""}, "API_KEY_PEPPER": {"", "not-base64", base64.StdEncoding.EncodeToString([]byte("short"))},
		"DB_MAX_CONNS": {"0", "-2", "2147483648"}, "AUTH_TIMEOUT": {"0s", "-1s", "bad"}, "SESSION_TTL": {"0s", "-1s", "bad"},
		"SESSION_STARTUP_TIMEOUT": {"0s", "bad", "25h"}, "PUBLIC_API_URL": {"ws://localhost", "https://user:pass@example.com", "https://example.com/path", "https://example.com?key=secret", "https://example.com#fragment", "https://"},
		"AUTH_REQUESTS_PER_MINUTE": {"0", "bad", "1000001"}, "TRUSTED_PROXY_CIDRS": {"127.0.0.1", "bad"},
	}
	workerOnly := map[string][]string{
		"METRICS_SAMPLE_INTERVAL": {"0s", "100ms", "2m", "bad"}, "BROWSER_MAX_SESSIONS": {"0", "bad", "10001"}, "BROWSER_SESSION_TTL": {"-1s", "25h"}, "BROWSER_STARTUP_TIMEOUT": {"bad", "0s"}, "WORKER_ID": {"invalid/worker"}, "WORKER_URL": {"http://user:pass@localhost", "http://localhost/path", "ws://localhost"}, "WORKER_LEASE_TTL": {"1s", "6m"},
	}
	common := map[string][]string{"REDIS_URL": {"http://localhost:6379", "redis://"}, "REDIS_NAMESPACE": {"unsafe:namespace"}, "WORKER_AUTH_TOKEN": {"", "short"}}
	check := func(settings map[string][]string, load func() error) {
		for name, values := range settings {
			for _, v := range values {
				t.Run(name+"="+v, func(t *testing.T) {
					setValidEnv(t)
					t.Setenv(name, v)
					if err := load(); err == nil {
						t.Fatal("invalid setting accepted")
					}
				})
			}
		}
	}
	loadAPI := func() error { _, err := LoadAPI(); return err }
	loadWorker := func() error { _, err := LoadWorker(); return err }
	check(apiOnly, loadAPI)
	check(workerOnly, loadWorker)
	check(common, loadAPI)
	check(common, loadWorker)
}
