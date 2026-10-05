// Package limits defines tenant policies and shared admission contracts.
package limits

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Policy struct {
	MaxConcurrentSessions    int
	MaxSessionTTL            time.Duration
	SessionRequestsPerMinute int
}

func DefaultPolicy() Policy { return Policy{5, 15 * time.Minute, 30} }
func (p Policy) Validate() error {
	if p.MaxConcurrentSessions < 1 || p.MaxConcurrentSessions > 10000 || p.MaxSessionTTL < time.Millisecond || p.MaxSessionTTL > 24*time.Hour || p.SessionRequestsPerMinute < 1 || p.SessionRequestsPerMinute > 1000000 {
		return errors.New("invalid tenant admission policy")
	}
	return nil
}

type Provider interface {
	Policy(context.Context, string) (Policy, error)
}
type ProviderFunc func(context.Context, string) (Policy, error)

func (f ProviderFunc) Policy(ctx context.Context, id string) (Policy, error) { return f(ctx, id) }

// Allow returns zero when admitted, or a positive delay when rejected.
type RateLimiter interface {
	Allow(context.Context, string, string, int, time.Duration) (time.Duration, error)
}

var (
	ErrConcurrency = errors.New("tenant concurrent session limit reached")
	ErrRate        = errors.New("request rate limit reached")
)

type Rejection struct {
	Reason     error
	RetryAfter time.Duration
}

func (e *Rejection) Error() string { return fmt.Sprint(e.Reason) }
func (e *Rejection) Unwrap() error { return e.Reason }
