package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"novel-bot/internal/sessions"
)

type directoryStub struct {
	sessions.Directory
	mu         sync.Mutex
	w          sessions.Worker
	renewErr   error
	publishErr error
}

func (d *directoryStub) Register(_ context.Context, w sessions.Worker, _ time.Duration) error {
	d.w = w
	return nil
}
func (d *directoryStub) Renew(context.Context, sessions.Worker, time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.renewErr
}
func (*directoryStub) Unregister(context.Context, sessions.Worker) error { return nil }
func (d *directoryStub) Lookup(_ context.Context, clientID, id, state string) (sessions.Record, error) {
	return sessions.Record{ID: id, ClientID: clientID, State: state, WorkerID: d.w.ID, WorkerToken: d.w.Token}, nil
}
func (d *directoryStub) Publish(context.Context, sessions.Record, sessions.Session) error {
	return d.publishErr
}
func (*directoryStub) Release(context.Context, sessions.Record) error { return nil }

type browserStub struct {
	done chan struct{}
	once sync.Once
}

func (b *browserStub) Endpoint() string           { return "ws://127.0.0.1:9222/devtools/browser/test" }
func (b *browserStub) Done() <-chan struct{}      { return b.done }
func (b *browserStub) Stop(context.Context) error { b.once.Do(func() { close(b.done) }); return nil }

type launcherStub struct{ b *browserStub }

func (l launcherStub) Launch(context.Context) (sessions.Browser, error) { return l.b, nil }
func testAgent(t *testing.T, d *directoryStub) (*Agent, *browserStub) {
	t.Helper()
	b := &browserStub{done: make(chan struct{})}
	a, err := NewAgent(context.Background(), d, launcherStub{b}, sessions.Worker{ID: "worker", URL: "http://127.0.0.1:8090"}, sessions.Options{MaxSessions: 1, TTL: time.Minute, StartupTimeout: time.Second}, 150*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return a, b
}
func TestLeaseLossAndHeartbeatOutageStopBrowsers(t *testing.T) {
	for _, failure := range []error{sessions.ErrLeaseLost, errors.New("Redis unavailable")} {
		t.Run(failure.Error(), func(t *testing.T) {
			d := &directoryStub{}
			a, b := testAgent(t, d)
			id, _ := sessions.NewID()
			if _, err := a.CreateReserved(context.Background(), "owner", id); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			d.renewErr = failure
			d.mu.Unlock()
			select {
			case <-a.Done():
			case <-time.After(time.Second):
				t.Fatal("worker did not stop after lease loss")
			}
			select {
			case <-b.done:
			default:
				t.Fatal("browser survived lease loss")
			}
			if !errors.Is(a.Ready(), sessions.ErrLeaseLost) {
				t.Fatal("fenced worker remained ready")
			}
		})
	}
}
func TestPublicationFailureStopsUntrackedBrowser(t *testing.T) {
	d := &directoryStub{publishErr: errors.New("Redis write failed")}
	a, b := testAgent(t, d)
	id, _ := sessions.NewID()
	if _, err := a.CreateReserved(context.Background(), "owner", id); err == nil {
		t.Fatal("unpublished session returned successfully")
	}
	select {
	case <-b.done:
	default:
		t.Fatal("unpublished browser leaked")
	}
}
