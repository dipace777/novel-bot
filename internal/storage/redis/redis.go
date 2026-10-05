// Package redis implements the browser session directory and worker leases.
package redis

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

func Open(ctx context.Context, rawURL string) (*goredis.Client, error) {
	options, err := goredis.ParseURL(rawURL)
	if err != nil {
		return nil, errors.New("invalid REDIS_URL")
	}
	options.DialTimeout, options.ReadTimeout, options.WriteTimeout = 2*time.Second, 2*time.Second, 2*time.Second
	options.ContextTimeoutEnabled = true
	// Retrying non-idempotent reservations after an ambiguous response could
	// allocate a second slot. Callers handle failures explicitly instead.
	options.MaxRetries = -1
	options.PoolSize = 20
	client := goredis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect to Redis: %w", err)
	}
	return client, nil
}

type Directory struct {
	client *goredis.Client
	prefix string
}

func NewDirectory(client *goredis.Client, prefix string) (*Directory, error) {
	if client == nil || !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(prefix) {
		return nil, errors.New("Redis directory requires a client and a simple namespace")
	}
	return &Directory{client: client, prefix: prefix + ":{sessions}:"}, nil
}

func (d *Directory) Ping(ctx context.Context) error { return d.client.Ping(ctx).Err() }
