package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AuthRequestsPerMinute int
	TrustedProxies        []netip.Prefix
	HTTPAddr              string
	DatabaseURL           string
	APIKeyPepper          []byte
	DBMaxConns            int32
	AuthTimeout           time.Duration
	SessionTTL            time.Duration
	ChromiumPath          string
	BrowserProfileDir     string
	BrowserMaxSessions    int
	BrowserSessionTTL     time.Duration
	BrowserStartupTimeout time.Duration
	PublicAPIURL          string
	RedisURL              string
	RedisNamespace        string
	WorkerID              string
	WorkerHTTPAddr        string
	WorkerURL             string
	WorkerAuthToken       string
	WorkerLeaseTTL        time.Duration
}

func Load() (Config, error) {
	cfg := Config{AuthRequestsPerMinute: 30, HTTPAddr: ":8080", DBMaxConns: 20, AuthTimeout: 2 * time.Second, SessionTTL: 24 * time.Hour, BrowserMaxSessions: 10, BrowserSessionTTL: 15 * time.Minute, BrowserStartupTimeout: 10 * time.Second, RedisURL: "redis://localhost:6930/0", RedisNamespace: "novelbot", WorkerHTTPAddr: "127.0.0.1:8090", WorkerURL: "http://127.0.0.1:8090", WorkerLeaseTTL: 15 * time.Second}
	if addr := os.Getenv("HTTP_ADDR"); addr != "" {
		cfg.HTTPAddr = addr
	}
	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	pepper, err := base64.StdEncoding.Strict().DecodeString(os.Getenv("API_KEY_PEPPER"))
	if err != nil || len(pepper) < 32 {
		return Config{}, fmt.Errorf("API_KEY_PEPPER must be base64 encoded and contain at least 32 random bytes")
	}
	cfg.APIKeyPepper = pepper
	if value := os.Getenv("DB_MAX_CONNS"); value != "" {
		n, err := strconv.ParseInt(value, 10, 32)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("DB_MAX_CONNS must be a positive integer")
		}
		cfg.DBMaxConns = int32(n)
	}
	if value := os.Getenv("AUTH_TIMEOUT"); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("AUTH_TIMEOUT must be a positive duration")
		}
		cfg.AuthTimeout = d
	}
	if value := os.Getenv("SESSION_TTL"); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("SESSION_TTL must be a positive duration")
		}
		cfg.SessionTTL = d
	}
	cfg.ChromiumPath = os.Getenv("CHROMIUM_PATH")
	cfg.BrowserProfileDir = os.Getenv("BROWSER_PROFILE_DIR")
	cfg.PublicAPIURL = os.Getenv("PUBLIC_API_URL")
	if cfg.PublicAPIURL != "" {
		u, err := url.Parse(cfg.PublicAPIURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, fmt.Errorf("PUBLIC_API_URL must be an http(s) origin without credentials, path, query, or fragment")
		}
	}
	if value := os.Getenv("BROWSER_MAX_SESSIONS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("BROWSER_MAX_SESSIONS must be a positive integer")
		}
		cfg.BrowserMaxSessions = n
	}
	for _, setting := range []struct {
		name   string
		target *time.Duration
	}{
		{"BROWSER_SESSION_TTL", &cfg.BrowserSessionTTL},
		{"BROWSER_STARTUP_TIMEOUT", &cfg.BrowserStartupTimeout},
	} {
		if value := os.Getenv(setting.name); value != "" {
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 || d > 24*time.Hour {
				return Config{}, fmt.Errorf("%s must be a positive duration up to 24h", setting.name)
			}
			*setting.target = d
		}
	}
	if value := os.Getenv("REDIS_URL"); value != "" {
		cfg.RedisURL = value
	}
	u, err := url.Parse(cfg.RedisURL)
	if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Hostname() == "" {
		return Config{}, fmt.Errorf("REDIS_URL must be a Redis connection URL")
	}
	if value := os.Getenv("REDIS_NAMESPACE"); value != "" {
		cfg.RedisNamespace = value
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(cfg.RedisNamespace) {
		return Config{}, fmt.Errorf("REDIS_NAMESPACE must contain 1 to 64 letters, digits, underscores, or hyphens")
	}
	cfg.WorkerID = os.Getenv("WORKER_ID")
	if cfg.WorkerID != "" && !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(cfg.WorkerID) {
		return Config{}, fmt.Errorf("WORKER_ID must contain 1 to 64 letters, digits, underscores, or hyphens")
	}
	if value := os.Getenv("WORKER_HTTP_ADDR"); value != "" {
		cfg.WorkerHTTPAddr = value
	}
	if value := os.Getenv("WORKER_URL"); value != "" {
		cfg.WorkerURL = value
	}
	u, err = url.Parse(cfg.WorkerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, fmt.Errorf("WORKER_URL must be an http(s) origin reachable by other workers")
	}
	if value := os.Getenv("WORKER_LEASE_TTL"); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil || d < 3*time.Second || d > 5*time.Minute {
			return Config{}, fmt.Errorf("WORKER_LEASE_TTL must be between 3s and 5m")
		}
		cfg.WorkerLeaseTTL = d
	}
	cfg.WorkerAuthToken = os.Getenv("WORKER_AUTH_TOKEN")
	if cfg.WorkerAuthToken == "" {
		mac := hmac.New(sha256.New, cfg.APIKeyPepper)
		mac.Write([]byte("novel-bot/worker-auth/v1"))
		cfg.WorkerAuthToken = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	} else if len(cfg.WorkerAuthToken) < 32 {
		return Config{}, fmt.Errorf("WORKER_AUTH_TOKEN must contain at least 32 characters")
	}
	if value := os.Getenv("AUTH_REQUESTS_PER_MINUTE"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 1000000 {
			return Config{}, fmt.Errorf("AUTH_REQUESTS_PER_MINUTE must be between 1 and 1000000")
		}
		cfg.AuthRequestsPerMinute = n
	}
	if value := os.Getenv("TRUSTED_PROXY_CIDRS"); value != "" {
		for _, cidr := range strings.Split(value, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
			if err != nil {
				return Config{}, fmt.Errorf("TRUSTED_PROXY_CIDRS must be a comma-separated list of IP CIDRs")
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, prefix.Masked())
		}
	}
	return cfg, nil
}
