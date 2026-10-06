package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"novel-bot/internal/sessions"
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

type drainWorkerStub struct {
	metricsWorkerStub
	draining bool
	drainErr error
}

func (*drainWorkerStub) Ready() error                       { return nil }
func (s *drainWorkerStub) BeginDrain(context.Context) error { s.draining = true; return s.drainErr }
func (s *drainWorkerStub) Status() sessions.WorkerStatus {
	state := sessions.WorkerReady
	if s.draining {
		state = sessions.WorkerDraining
	}
	return sessions.WorkerStatus{WorkerID: "worker", State: state, Accepting: !s.draining, LeaseValid: true}
}

func TestPrivateDrainAuthenticationAndReadiness(t *testing.T) {
	s := &drainWorkerStub{}
	router := NewWorkerRouter(s, testWorkerCredential, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, tc := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/internal/status", "", 401}, {"POST", "/internal/drain", "application-key", 401},
		{"GET", "/internal/drain", testWorkerCredential, 405}, {"POST", "/internal/status", testWorkerCredential, 405},
		{"GET", "/readyz", testWorkerCredential, 200}, {"POST", "/internal/drain", testWorkerCredential, 202},
		{"POST", "/internal/drain", testWorkerCredential, 202}, {"GET", "/readyz", testWorkerCredential, 503},
		{"GET", "/healthz", "", 200}, {"GET", "/internal/status", testWorkerCredential, 200},
	} {
		res := apiRequest(router, tc.method, tc.path, "", tc.token)
		if res.Code != tc.want {
			t.Fatalf("%s %s got %d want %d", tc.method, tc.path, res.Code, tc.want)
		}
		if strings.Contains(res.Body.String(), "incarnation") {
			t.Fatal("status exposed credential")
		}
	}
	s.drainErr = errors.New("Redis unavailable")
	if res := apiRequest(router, "POST", "/internal/drain", "", testWorkerCredential); res.Code != 503 {
		t.Fatal("drain failure hidden")
	}
}
