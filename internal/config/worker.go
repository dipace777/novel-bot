package config

import (
	"fmt"
	"os"
	"time"
)

type WorkerConfig struct {
	RedisConfig
	WorkerID              string
	WorkerHTTPAddr        string
	WorkerURL             string
	WorkerAuthToken       string
	WorkerLeaseTTL        time.Duration
	ChromiumPath          string
	BrowserProfileDir     string
	BrowserMaxSessions    int
	BrowserSessionTTL     time.Duration
	BrowserStartupTimeout time.Duration
	MetricsSampleInterval time.Duration
}

// LoadWorker deliberately does not read PostgreSQL credentials or API_KEY_PEPPER.
func LoadWorker() (WorkerConfig, error) {
	var cfg WorkerConfig
	var err error
	if cfg.RedisConfig, err = loadRedis(); err != nil {
		return WorkerConfig{}, err
	}
	if cfg.WorkerAuthToken, err = WorkerCredential(os.Getenv("WORKER_AUTH_TOKEN")); err != nil {
		return WorkerConfig{}, err
	}
	cfg.WorkerID = os.Getenv("WORKER_ID")
	if cfg.WorkerID != "" && !identifier.MatchString(cfg.WorkerID) {
		return WorkerConfig{}, fmt.Errorf("WORKER_ID must contain 1 to 64 letters, digits, underscores, or hyphens")
	}
	cfg.WorkerHTTPAddr = value("WORKER_HTTP_ADDR", "127.0.0.1:8090")
	cfg.WorkerURL = value("WORKER_URL", "http://127.0.0.1:8090")
	if err := origin("WORKER_URL", cfg.WorkerURL); err != nil {
		return WorkerConfig{}, err
	}
	if cfg.WorkerLeaseTTL, err = duration("WORKER_LEASE_TTL", 15*time.Second, 3*time.Second, 5*time.Minute); err != nil {
		return WorkerConfig{}, err
	}
	cfg.ChromiumPath = os.Getenv("CHROMIUM_PATH")
	cfg.BrowserProfileDir = os.Getenv("BROWSER_PROFILE_DIR")
	if cfg.BrowserMaxSessions, err = integer("BROWSER_MAX_SESSIONS", 10, 10000); err != nil {
		return WorkerConfig{}, err
	}
	if cfg.BrowserSessionTTL, err = duration("BROWSER_SESSION_TTL", 15*time.Minute, time.Nanosecond, 24*time.Hour); err != nil {
		return WorkerConfig{}, err
	}
	if cfg.BrowserStartupTimeout, err = duration("BROWSER_STARTUP_TIMEOUT", 10*time.Second, time.Nanosecond, 24*time.Hour); err != nil {
		return WorkerConfig{}, err
	}
	if cfg.MetricsSampleInterval, err = duration("METRICS_SAMPLE_INTERVAL", 5*time.Second, 250*time.Millisecond, time.Minute); err != nil {
		return WorkerConfig{}, err
	}
	return cfg, nil
}
