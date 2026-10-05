package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"novel-bot/internal/auth"
)

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
