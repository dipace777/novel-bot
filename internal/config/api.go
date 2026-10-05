package config

import (
	"encoding/base64"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"
)

type APIConfig struct {
	DatabaseConfig
	RedisConfig
	HTTPAddr              string
	APIKeyPepper          []byte
	AuthTimeout           time.Duration
	SessionTTL            time.Duration
	SessionStartupTimeout time.Duration
	PublicAPIURL          string
	WorkerAuthToken       string
	AuthRequestsPerMinute int
	TrustedProxies        []netip.Prefix
}

func LoadAPI() (APIConfig, error) {
	var cfg APIConfig
	var err error
	if cfg.DatabaseConfig, err = LoadDatabase(); err != nil {
		return APIConfig{}, err
	}
	if cfg.RedisConfig, err = loadRedis(); err != nil {
		return APIConfig{}, err
	}
	cfg.HTTPAddr = value("HTTP_ADDR", ":8080")
	pepper, err := base64.StdEncoding.Strict().DecodeString(os.Getenv("API_KEY_PEPPER"))
	if err != nil || len(pepper) < 32 {
		return APIConfig{}, fmt.Errorf("API_KEY_PEPPER must be base64 encoded and contain at least 32 random bytes")
	}
	cfg.APIKeyPepper = pepper
	if cfg.WorkerAuthToken, err = WorkerCredential(os.Getenv("WORKER_AUTH_TOKEN")); err != nil {
		return APIConfig{}, err
	}
	if cfg.AuthTimeout, err = duration("AUTH_TIMEOUT", 2*time.Second, time.Nanosecond, maxDuration); err != nil {
		return APIConfig{}, err
	}
	if cfg.SessionTTL, err = duration("SESSION_TTL", 24*time.Hour, time.Nanosecond, maxDuration); err != nil {
		return APIConfig{}, err
	}
	if cfg.SessionStartupTimeout, err = duration("SESSION_STARTUP_TIMEOUT", 10*time.Second, time.Nanosecond, 24*time.Hour); err != nil {
		return APIConfig{}, err
	}
	cfg.PublicAPIURL = os.Getenv("PUBLIC_API_URL")
	if cfg.PublicAPIURL != "" {
		if err := origin("PUBLIC_API_URL", cfg.PublicAPIURL); err != nil {
			return APIConfig{}, err
		}
	}
	if cfg.AuthRequestsPerMinute, err = integer("AUTH_REQUESTS_PER_MINUTE", 30, 1000000); err != nil {
		return APIConfig{}, err
	}
	if raw := os.Getenv("TRUSTED_PROXY_CIDRS"); raw != "" {
		for _, cidr := range strings.Split(raw, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
			if err != nil {
				return APIConfig{}, fmt.Errorf("TRUSTED_PROXY_CIDRS must be a comma-separated list of IP CIDRs")
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, prefix.Masked())
		}
	}
	return cfg, nil
}
