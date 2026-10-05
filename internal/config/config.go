// Package config loads only the environment settings needed by each process role.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

const maxDuration = time.Duration(1<<63 - 1)

func value(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func duration(name string, fallback, min, max time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(value(name, fallback.String()))
	if err != nil || d < min || d > max {
		return 0, fmt.Errorf("%s must be between %s and %s", name, min, max)
	}
	return d, nil
}
func integer(name string, fallback, max int) (int, error) {
	n, err := strconv.Atoi(value(name, strconv.Itoa(fallback)))
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("%s must be between 1 and %d", name, max)
	}
	return n, nil
}
func origin(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s must be an http(s) origin without credentials, path, query, or fragment", name)
	}
	return nil
}

// WorkerCredential validates a separate shared secret, never derived from account secrets.
func WorkerCredential(token string) (string, error) {
	if len(token) < 32 {
		return "", fmt.Errorf("WORKER_AUTH_TOKEN is required and must contain at least 32 characters")
	}
	return token, nil
}
