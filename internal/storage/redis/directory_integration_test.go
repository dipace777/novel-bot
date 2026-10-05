package redis

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novel-bot/internal/limits"
	"novel-bot/internal/sessions"
)

func testDirectory(t *testing.T) *Directory {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("set TEST_REDIS_URL to run Redis integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	id, err := sessions.NewID()
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDirectory(client, "test_"+id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var cursor uint64
		for {
			keys, next, err := client.Scan(cleanup, cursor, d.prefix+"*", 100).Result()
			if err != nil {
				t.Error(err)
				break
			}
			if len(keys) > 0 {
				if err := client.Del(cleanup, keys...).Err(); err != nil {
					t.Error(err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
		_ = client.Close()
	})
	return d
}

func TestAtomicCapacityAcrossConcurrentReservations(t *testing.T) {
	d := testDirectory(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := d.Register(ctx, sessions.Worker{ID: id, Token: id, URL: "http://127.0.0.1:8090", Capacity: 2}, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	var successful atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := sessions.NewID()
			if err != nil {
				t.Error(err)
				return
			}
			_, err = d.Reserve(ctx, "owner", id, 3*time.Second, limits.Policy{MaxConcurrentSessions: 100, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 1000})
			if err == nil {
				successful.Add(1)
			} else if !errors.Is(err, sessions.ErrCapacity) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if count := successful.Load(); count != 4 {
		t.Fatalf("expected exactly four reserved slots, got %d", count)
	}
}

func TestOwnershipPublicationAndWorkerIncarnationFencing(t *testing.T) {
	d := testDirectory(t)
	ctx := context.Background()
	old := sessions.Worker{ID: "worker", Token: "old", URL: "http://127.0.0.1:8090", Capacity: 1}
	if err := d.Register(ctx, old, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	newWorker := old
	newWorker.Token = "new"
	if err := d.Register(ctx, newWorker, 5*time.Second); !errors.Is(err, sessions.ErrLeaseLost) {
		t.Fatalf("duplicate live worker claimed identity: %v", err)
	}
	id, _ := sessions.NewID()
	record, err := d.Reserve(ctx, "owner", id, time.Second, limits.Policy{MaxConcurrentSessions: 100, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := d.Reserve(ctx, "owner", id, time.Second, limits.Policy{MaxConcurrentSessions: 100, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 1000}); err != nil || again.WorkerToken != record.WorkerToken {
		t.Fatal("reservation not idempotent")
	}
	if _, err := d.Lookup(ctx, "other", id, "starting"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("cross-tenant reservation visible")
	}
	if _, err := d.Lookup(ctx, "owner", id, "ready"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("starting session published early")
	}
	now := time.Now()
	if err := d.Publish(ctx, record, sessions.Session{ID: id, ClientID: "owner", CreatedAt: now, ExpiresAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if r, err := d.Lookup(ctx, "owner", id, "ready"); err != nil || r.State != "ready" {
		t.Fatal(err)
	}
	if err := d.Unregister(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(ctx, newWorker, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lookup(ctx, "owner", id, "ready"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("old session routed to a replacement worker")
	}
	if err := d.Renew(ctx, old, 5*time.Second); !errors.Is(err, sessions.ErrLeaseLost) {
		t.Fatal("old incarnation renewed replacement lease")
	}
	if err := d.Unregister(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := d.CheckWorker(ctx, old); !errors.Is(err, sessions.ErrLeaseLost) {
		t.Fatal("old incarnation passed readiness")
	}
	if err := d.CheckWorker(ctx, newWorker); err != nil {
		t.Fatal("current incarnation failed readiness", err)
	}
	if err := d.Renew(ctx, newWorker, 5*time.Second); err != nil {
		t.Fatal("old shutdown removed new worker")
	}
	newID, _ := sessions.NewID()
	newRecord, err := d.Reserve(ctx, "owner", newID, time.Second, limits.Policy{MaxConcurrentSessions: 100, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 1000})
	if err != nil {
		t.Fatal("old slots consumed replacement capacity")
	}
	if err := d.Release(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lookup(ctx, "owner", newID, "starting"); err != nil {
		t.Fatal("old cleanup removed new reservation")
	}
	if err := d.Release(ctx, newRecord); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lookup(ctx, "owner", newID, "starting"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("release did not delete session")
	}
}

func TestReservationsAndWorkerLeasesExpire(t *testing.T) {
	d := testDirectory(t)
	ctx := context.Background()
	w := sessions.Worker{ID: "worker", Token: "token", URL: "http://127.0.0.1:8090", Capacity: 1}
	if err := d.Register(ctx, w, time.Second); err != nil {
		t.Fatal(err)
	}
	id, _ := sessions.NewID()
	r, err := d.Reserve(ctx, "owner", id, 50*time.Millisecond, limits.Policy{MaxConcurrentSessions: 100, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 1000})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(70 * time.Millisecond)
	if _, err := d.Lookup(ctx, "owner", id, "starting"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("reservation did not expire")
	}
	if err := d.Publish(ctx, r, sessions.Session{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Second)}); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("expired reservation published")
	}
	id, _ = sessions.NewID()
	if _, err := d.Reserve(ctx, "owner", id, time.Second, limits.Policy{MaxConcurrentSessions: 100, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 1000}); err != nil {
		t.Fatal("expired capacity not released")
	}
	if err := d.Unregister(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(ctx, w, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(70 * time.Millisecond)
	if err := d.Renew(ctx, w, time.Second); !errors.Is(err, sessions.ErrLeaseLost) {
		t.Fatal("expired worker lease resurrected")
	}
	id, _ = sessions.NewID()
	if _, err := d.Reserve(ctx, "owner", id, time.Second, limits.Policy{MaxConcurrentSessions: 100, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 1000}); !errors.Is(err, sessions.ErrCapacity) {
		t.Fatal("expired worker selected")
	}
}
