package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"novel-bot/internal/observability"
	"novel-bot/internal/sessions"
)

type metricsWorkerStub struct{}

func (metricsWorkerStub) Worker() sessions.Worker {
	return sessions.Worker{ID: "worker", Token: "incarnation"}
}
func (metricsWorkerStub) Ready() error {
	panic("metrics must remain accessible when the worker is fenced")
}
func (metricsWorkerStub) CreateReserved(context.Context, string, string) (sessions.Session, error) {
	panic("unexpected session creation")
}
func (metricsWorkerStub) Get(context.Context, string, string) (sessions.Session, error) {
	panic("unexpected session lookup")
}
func (metricsWorkerStub) Delete(context.Context, string, string) error {
	panic("unexpected session deletion")
}
func TestWorkerMetricsRequirePrivateCredentialOnly(t *testing.T) {
	m := observability.New("worker")
	router := NewWorkerRouter(metricsWorkerStub{}, testWorkerCredential, slog.New(slog.NewTextHandler(io.Discard, nil)), WorkerRouterOptions{Metrics: m.Handler()})
	for _, token := range []string{"", "application-api-key", testWorkerCredential} {
		response := apiRequest(router, "GET", "/metrics", "", token)
		want := 401
		if token == testWorkerCredential {
			want = 200
		}
		if response.Code != want {
			t.Fatalf("metrics auth status %d want %d", response.Code, want)
		}
	}
	if response := apiRequest(router, "POST", "/metrics", "", testWorkerCredential); response.Code != http.StatusMethodNotAllowed {
		t.Fatal("unsupported scrape method accepted")
	}
	public := accountRouter(&keySpy{}, &accountStub{})
	if response := apiRequest(public, "GET", "/metrics", "", ""); response.Code != 404 {
		t.Fatal("worker metrics exposed publicly")
	}
}
