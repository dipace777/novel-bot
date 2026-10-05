package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"novel-bot/internal/limits"
)

type rateLimiterFunc func(context.Context, string, string, int, time.Duration) (time.Duration, error)

func (f rateLimiterFunc) Allow(ctx context.Context, scope, key string, count int, window time.Duration) (time.Duration, error) {
	return f(ctx, scope, key, count, window)
}

func TestAuthenticationRateLimitBeforeCredentialsAndService(t *testing.T) {
	for _, tc := range []struct {
		name   string
		delay  time.Duration
		err    error
		status int
	}{
		{"rate", 1500 * time.Millisecond, nil, 429}, {"outage", 0, errors.New("Redis unavailable"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			limiter := rateLimiterFunc(func(_ context.Context, scope, key string, count int, window time.Duration) (time.Duration, error) {
				calls++
				if scope != "authentication" || key != "192.0.2.1" || count != 10 || window != time.Minute {
					t.Error("incorrect rate identity or configuration")
				}
				return tc.delay, tc.err
			})
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			router := NewRouter(&keySpy{}, &accountStub{}, func(context.Context) error { return nil }, logger, time.Second, RouterOptions{RateLimiter: limiter, AuthRequestsPerMinute: 10})
			for _, route := range []string{"/v1/auth/login", "/v1/auth/register", "/v1/auth/me", "/v1/api-keys"} {
				response := apiRequest(router, "POST", route, `{}`, "")
				if response.Code != tc.status {
					t.Fatalf("%s: %d %s", route, response.Code, response.Body.String())
				}
				if tc.status == 429 && response.Header().Get("Retry-After") != "2" {
					t.Fatal("Retry-After not rounded up")
				}
			}
			apiRequest(router, "GET", "/healthz", "", "")
			apiRequest(router, "GET", "/docs/", "", "")
			if calls != 4 {
				t.Fatal("public docs/health consumed auth budget")
			}
		})
	}
}

func TestSessionAdmissionResponses(t *testing.T) {
	h := sessionHandlers{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, tc := range []struct {
		reason error
		code   string
	}{{limits.ErrConcurrency, "tenant_session_limit_reached"}, {limits.ErrRate, "session_rate_limit_exceeded"}} {
		response := httptest.NewRecorder()
		h.sessionError(response, httptest.NewRequest("POST", "/sessions", nil), &limits.Rejection{Reason: tc.reason, RetryAfter: 1200 * time.Millisecond})
		if response.Code != 429 || response.Header().Get("Retry-After") != "2" || !strings.Contains(response.Body.String(), tc.code) {
			t.Fatalf("wrong rejection: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestRequestIPOnlyTrustsExplicitProxyChain(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	for _, tc := range []struct{ peer, forwarded, want string }{
		{"192.0.2.1:123", "198.51.100.5", "192.0.2.1"},
		{"10.0.0.1:123", "198.51.100.5, 10.0.0.2", "198.51.100.5"},
		{"10.0.0.1:123", "203.0.113.9, 198.51.100.5", "198.51.100.5"},
		{"10.0.0.1:123", "invalid", "10.0.0.1"},
		{"[::ffff:192.0.2.1]:123", "", "192.0.2.1"},
		{"[2001:db8:abcd:1234::1]:123", "", "2001:db8:abcd:1234::/64"},
		{"[2001:db8:abcd:1234::2]:456", "", "2001:db8:abcd:1234::/64"},
	} {
		req := httptest.NewRequest("POST", "/v1/auth/login", nil)
		req.RemoteAddr = tc.peer
		req.Header.Set("X-Forwarded-For", tc.forwarded)
		if got := requestIP(req, trusted); got != tc.want {
			t.Errorf("%s via %s: got %s want %s", tc.peer, tc.forwarded, got, tc.want)
		}
	}
}
