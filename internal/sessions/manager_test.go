package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type launchFunc func(context.Context) (Browser, error)

func (f launchFunc) Launch(ctx context.Context) (Browser, error) { return f(ctx) }

type fakeBrowser struct {
	done chan struct{}
	once sync.Once
}

func newBrowser() *fakeBrowser                    { return &fakeBrowser{done: make(chan struct{})} }
func (b *fakeBrowser) Endpoint() string           { return "ws://127.0.0.1:9222/devtools/browser/test" }
func (b *fakeBrowser) Done() <-chan struct{}      { return b.done }
func (b *fakeBrowser) Stop(context.Context) error { b.once.Do(func() { close(b.done) }); return nil }

func managerForTest(t *testing.T, launch Launcher, max int, ttl time.Duration) *Manager {
	t.Helper()
	m, err := NewManager(launch, Options{MaxSessions: max, TTL: ttl, StartupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}

func TestLifecycleOwnershipAndCapacity(t *testing.T) {
	m := managerForTest(t, launchFunc(func(context.Context) (Browser, error) { return newBrowser(), nil }), 1, time.Minute)
	request, cancel := context.WithCancel(context.Background())
	s, err := m.Create(request, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cancel() // HTTP request completion must not terminate the browser.
	if _, err := m.Get(context.Background(), "owner", s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(context.Background(), "other", s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant lookup accepted")
	}
	if err := m.Delete(context.Background(), "other", s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant deletion accepted")
	}
	if _, err := m.Create(context.Background(), "owner"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	if err := m.Delete(context.Background(), "owner", s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(context.Background(), "owner", s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted session found")
	}
	if _, err := m.Create(context.Background(), "owner"); err != nil {
		t.Fatalf("capacity not released: %v", err)
	}
}

func TestConcurrentLaunchesReserveCapacity(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	m := managerForTest(t, launchFunc(func(ctx context.Context) (Browser, error) {
		close(entered)
		select {
		case <-release:
			return newBrowser(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}), 1, time.Minute)
	result := make(chan error, 1)
	go func() { _, err := m.Create(context.Background(), "owner"); result <- err }()
	<-entered
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Create(context.Background(), "owner"); !errors.Is(err, ErrCapacity) {
				t.Errorf("pending launch did not reserve capacity: %v", err)
			}
		}()
	}
	wg.Wait()
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestExpiryAndProcessExitReleaseSessions(t *testing.T) {
	for _, reason := range []string{"expiry", "exit"} {
		t.Run(reason, func(t *testing.T) {
			b := newBrowser()
			m := managerForTest(t, launchFunc(func(context.Context) (Browser, error) { return b, nil }), 1, 25*time.Millisecond)
			s, err := m.Create(context.Background(), "owner")
			if err != nil {
				t.Fatal(err)
			}
			if reason == "exit" {
				_ = b.Stop(context.Background())
			}
			select {
			case <-s.Done:
			case <-time.After(time.Second):
				t.Fatal("browser not stopped")
			}
			deadline := time.Now().Add(time.Second)
			for {
				m.mu.Lock()
				count := m.reserved
				m.mu.Unlock()
				if count == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("capacity leaked")
				}
				time.Sleep(time.Millisecond)
			}
			if _, err := m.Get(context.Background(), "owner", s.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal("stopped session found")
			}
		})
	}
}

func TestShutdownCancelsPendingLaunchAndRejectsNewSessions(t *testing.T) {
	entered := make(chan struct{})
	m := managerForTest(t, launchFunc(func(ctx context.Context) (Browser, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }), 1, time.Minute)
	result := make(chan error, 1)
	go func() { _, err := m.Create(context.Background(), "owner"); result <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("launch not cancelled: %v", err)
	}
	if _, err := m.Create(context.Background(), "owner"); !errors.Is(err, ErrClosed) {
		t.Fatal("closed manager accepted session")
	}
}

func TestLaunchFailureReleasesCapacity(t *testing.T) {
	calls := 0
	m := managerForTest(t, launchFunc(func(context.Context) (Browser, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("failed launch")
		}
		return newBrowser(), nil
	}), 1, time.Minute)
	if _, err := m.Create(context.Background(), "owner"); err == nil {
		t.Fatal("failed launch accepted")
	}
	if _, err := m.Create(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
}
