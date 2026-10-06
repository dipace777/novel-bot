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
	releaseErr error
	releases   int
	drains     int
	drainErr   error
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
func (d *directoryStub) Drain(context.Context, sessions.Worker) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drains++
	return d.drainErr
}
func (d *directoryStub) Lookup(_ context.Context, clientID, id, state string) (sessions.Record, error) {
	return sessions.Record{ID: id, ClientID: clientID, State: state, MaxDurationMS: 60000, WorkerID: d.w.ID, WorkerToken: d.w.Token}, nil
}
func (d *directoryStub) Publish(context.Context, sessions.Record, sessions.Session) error {
	return d.publishErr
}
func (d *directoryStub) Release(context.Context, sessions.Record) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.releases++
	return d.releaseErr
}

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
	a, err := NewAgent(context.Background(), d, launcherStub{b}, sessions.Worker{ID: "worker", URL: "http://127.0.0.1:8090"}, sessions.Options{MaxSessions: 1, TTL: time.Minute, StartupTimeout: time.Second}, 150*time.Millisecond, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func TestStoppedBrowserCleanupRetriesAfterTransientFailure(t *testing.T) {
	d := &directoryStub{releaseErr: errors.New("temporary Redis outage")}
	a, _ := testAgent(t, d)
	id, _ := sessions.NewID()
	if _, err := a.CreateReserved(context.Background(), "owner", id); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(context.Background(), "owner", id); err == nil {
		t.Fatal("cleanup outage not reported")
	}
	// Wait for the watcher callback as well as the explicit DELETE release.
	// Either goroutine may win removal of the local session.
	queuedDeadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		queued := len(a.pendingCleanup) > 0
		a.mu.Unlock()
		d.mu.Lock()
		attempts := d.releases
		d.mu.Unlock()
		if queued && attempts >= 2 {
			break
		}
		if time.Now().After(queuedDeadline) {
			t.Fatal("failed cleanup was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	// The initial cleanup failed, but the live lease can continue to renew.
	d.mu.Lock()
	d.releaseErr = nil
	d.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		pending := len(a.pendingCleanup)
		a.mu.Unlock()
		d.mu.Lock()
		attempts := d.releases
		d.mu.Unlock()
		if pending == 0 && attempts >= 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stopped browser quota cleanup was not retried")
}
