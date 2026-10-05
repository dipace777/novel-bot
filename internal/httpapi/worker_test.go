package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

func TestWorkerHealthAndLeaseReadiness(t *testing.T) {
	ready := false
	router := NewWorkerRouter(metricsWorkerStub{}, testWorkerCredential, slog.New(slog.NewTextHandler(io.Discard, nil)), WorkerRouterOptions{Ready: func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("worker probe has no deadline")
		}
		if !ready {
			return errors.New("lease unavailable")
		}
		return nil
	}})
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/healthz", "", 200}, {"POST", "/healthz", "", 405}, {"GET", "/readyz", "", 401}, {"GET", "/readyz", "application-key", 401}, {"GET", "/readyz", testWorkerCredential, 503}, {"POST", "/readyz", testWorkerCredential, 405},
	} {
		if res := apiRequest(router, tc.method, tc.path, "", tc.token); res.Code != tc.status {
			t.Fatalf("%s %s got %d want %d", tc.method, tc.path, res.Code, tc.status)
		}
	}
	ready = true
	if res := apiRequest(router, "GET", "/readyz", "", testWorkerCredential); res.Code != 200 {
		t.Fatal("ready worker rejected", res.Code)
	}
}
