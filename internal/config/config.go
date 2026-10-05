package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr     string
	DatabaseURL  string
	APIKeyPepper []byte
	DBMaxConns   int32
	AuthTimeout  time.Duration
	SessionTTL   time.Duration
}

func Load() (Config, error) {
	cfg := Config{HTTPAddr: ":8080", DBMaxConns: 20, AuthTimeout: 2 * time.Second, SessionTTL: 24 * time.Hour}
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
	return cfg, nil
}
