package config

import (
	"fmt"
	"net/url"
	"regexp"
)

type RedisConfig struct{ RedisURL, RedisNamespace string }

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func loadRedis() (RedisConfig, error) {
	cfg := RedisConfig{RedisURL: value("REDIS_URL", "redis://localhost:6930/0"), RedisNamespace: value("REDIS_NAMESPACE", "novelbot")}
	u, err := url.Parse(cfg.RedisURL)
	if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Hostname() == "" {
		return RedisConfig{}, fmt.Errorf("REDIS_URL must be a Redis connection URL")
	}
	if !identifier.MatchString(cfg.RedisNamespace) {
		return RedisConfig{}, fmt.Errorf("REDIS_NAMESPACE must contain 1 to 64 letters, digits, underscores, or hyphens")
	}
	return cfg, nil
}
