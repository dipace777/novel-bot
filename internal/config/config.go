package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
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
}

func Load() (Config, error) {
	cfg := Config{HTTPAddr: ":8080", DBMaxConns: 20, AuthTimeout: 2 * time.Second, SessionTTL: 24 * time.Hour, BrowserMaxSessions: 10, BrowserSessionTTL: 15 * time.Minute, BrowserStartupTimeout: 10 * time.Second}
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
	return cfg, nil
}
