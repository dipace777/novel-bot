package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novel-bot/internal/auth"
)

type authenticateFunc func(context.Context, string) (auth.Principal, error)

func (fn authenticateFunc) Authenticate(ctx context.Context, token string) (auth.Principal, error) {
	return fn(ctx, token)
}

func (fn authenticateFunc) Issue(context.Context, string, string, time.Duration) (auth.IssuedKey, error) {
	return auth.IssuedKey{}, errors.New("not configured")
}
func (fn authenticateFunc) List(context.Context, string) ([]auth.Key, error) {
	return nil, errors.New("not configured")
}
func (fn authenticateFunc) Revoke(context.Context, string, string) error {
	return errors.New("not configured")
}

func testRouter(fn authenticateFunc, ready func(context.Context) error) http.Handler {
	return NewRouter(fn, nil, ready, slog.New(slog.NewTextHandler(io.Discard, nil)), 20*time.Millisecond)
}

func TestAuthenticationHeaders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers http.Header
		status  int
	}{
		{"bearer", http.Header{"Authorization": {"Bearer valid"}}, 200},
		{"case insensitive scheme", http.Header{"Authorization": {"bearer valid"}}, 200},
		{"api key", http.Header{"X-Api-Key": {"valid"}}, 200},
		{"missing", http.Header{}, 401},
		{"empty", http.Header{"X-Api-Key": {""}}, 401},
		{"wrong key", http.Header{"Authorization": {"Bearer invalid"}}, 401},
		{"basic", http.Header{"Authorization": {"Basic valid"}}, 401},
		{"no value", http.Header{"Authorization": {"Bearer"}}, 401},
		{"extra value", http.Header{"Authorization": {"Bearer valid extra"}}, 401},
		{"duplicate", http.Header{"Authorization": {"Bearer valid", "Bearer valid"}}, 401},
		{"duplicate api key", http.Header{"X-Api-Key": {"valid", "valid"}}, 401},
		{"conflicting", http.Header{"Authorization": {"Bearer valid"}, "X-Api-Key": {"valid"}}, 401},
		{"comma joined", http.Header{"X-Api-Key": {"valid,valid"}}, 401},
		{"comma bearer", http.Header{"Authorization": {"Bearer valid,valid"}}, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := testRouter(func(ctx context.Context, token string) (auth.Principal, error) {
				if token != "valid" {
					return auth.Principal{}, auth.ErrUnauthorized
				}
				return auth.Principal{ClientID: "client", KeyID: "key"}, nil
			}, func(context.Context) error { return nil })
			req := httptest.NewRequest(http.MethodGet, "/v1/whoami", nil)
			req.Header = tc.headers
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("got %d: %s", response.Code, response.Body.String())
			}
			if response.Header().Get("X-Request-ID") == "" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing response metadata")
			}
			if tc.status == 200 {
				var principal auth.Principal
				if err := json.Unmarshal(response.Body.Bytes(), &principal); err != nil {
					t.Fatal(err)
				}
				if principal.ClientID != "client" || principal.KeyID != "key" {
					t.Fatalf("wrong identity: %+v", principal)
				}
			} else {
				if response.Header().Get("WWW-Authenticate") == "" {
					t.Fatal("missing authentication challenge")
				}
				if !json.Valid(response.Body.Bytes()) || strings.Contains(response.Body.String(), "Bearer valid") {
					t.Fatal("unsafe error response")
				}
			}
		})
	}
}

func TestStorageFailureAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   authenticateFunc
	}{
		{"failure", func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{}, errors.New("private database error")
		}},
		{"timeout", func(ctx context.Context, _ string) (auth.Principal, error) {
			<-ctx.Done()
			return auth.Principal{}, ctx.Err()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := testRouter(tc.fn, func(context.Context) error { return nil })
			req := httptest.NewRequest("GET", "/v1/whoami", nil)
			req.Header.Set("X-API-Key", "valid")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != 503 || strings.Contains(response.Body.String(), "private") {
				t.Fatalf("unsafe storage error: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRoutesAndReadiness(t *testing.T) {
	for _, tc := range []struct {
		path              string
		authorized, ready bool
		status            int
	}{
		{"/healthz", false, false, 200}, {"/readyz", false, true, 200}, {"/readyz", false, false, 503},
		{"/v1/future", false, true, 401}, {"/v1/future", true, true, 404}, {"/unknown", false, true, 404},
	} {
		t.Run(tc.path, func(t *testing.T) {
			router := testRouter(func(context.Context, string) (auth.Principal, error) { return auth.Principal{ClientID: "client"}, nil }, func(context.Context) error {
				if !tc.ready {
					return errors.New("unavailable")
				}
				return nil
			})
			req := httptest.NewRequest("GET", tc.path, nil)
			if tc.authorized {
				req.Header.Set("X-API-Key", "valid")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("got %d, want %d", response.Code, tc.status)
			}
		})
	}
}

func TestAuthenticationTimeoutDoesNotCancelHandler(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fn := authenticateFunc(func(context.Context, string) (auth.Principal, error) { return auth.Principal{ClientID: "client"}, nil })
	handler := RequireAPIKey(fn, logger, time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.Context().Err(); err != nil {
			t.Fatalf("handler context cancelled: %v", err)
		}
		if p, ok := PrincipalFromContext(r.Context()); !ok || p.ClientID != "client" {
			t.Fatal("principal missing")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "valid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 204 {
		t.Fatalf("got %d", response.Code)
	}
}

func TestUnsupportedMethods(t *testing.T) {
	router := testRouter(func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{ClientID: "client"}, nil
	}, func(context.Context) error { return nil })
	for _, path := range []string{"/healthz", "/readyz", "/v1/whoami"} {
		req := httptest.NewRequest("POST", path, nil)
		req.Header.Set("X-API-Key", "valid")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != 405 || response.Header().Get("Allow") != "GET, HEAD" || !json.Valid(response.Body.Bytes()) {
			t.Fatalf("invalid method response for %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}
