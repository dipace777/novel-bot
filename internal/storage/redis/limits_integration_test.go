package redis

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novel-bot/internal/limits"
	"novel-bot/internal/sessions"
)

func admissionPolicy() limits.Policy {
	return limits.Policy{MaxConcurrentSessions: 3, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 1000}
}
func reserveTenant(t *testing.T, d *Directory, tenant string, ttl time.Duration, p limits.Policy) sessions.Record {
	t.Helper()
	id, _ := sessions.NewID()
	r, err := d.Reserve(context.Background(), tenant, id, ttl, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func registerCapacity(t *testing.T, d *Directory, id string, capacity int) sessions.Worker {
	t.Helper()
	w := sessions.Worker{ID: id, Token: id, URL: "http://127.0.0.1:8090", Capacity: capacity}
	if err := d.Register(context.Background(), w, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestTenantQuotaAtomicAcrossGatewaysAndWorkers(t *testing.T) {
	d := testDirectory(t)
	registerCapacity(t, d, "a", 10)
	registerCapacity(t, d, "b", 10)
	replica := &Directory{client: d.client, prefix: d.prefix}
	var owner, other atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tenant, counter := "owner", &owner
			directory := d
			if i%2 == 1 {
				tenant, counter, directory = "other", &other, replica
			}
			id, _ := sessions.NewID()
			_, err := directory.Reserve(context.Background(), tenant, id, time.Second, admissionPolicy())
			if err == nil {
				counter.Add(1)
			} else if !errors.Is(err, limits.ErrConcurrency) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if owner.Load() != 3 || other.Load() != 3 {
		t.Fatalf("cross-replica quota: owner=%d other=%d", owner.Load(), other.Load())
	}
}

func TestTenantQuotaCleanupAndPublicationDuration(t *testing.T) {
	d := testDirectory(t)
	ctx := context.Background()
	w := registerCapacity(t, d, "a", 10)
	p := admissionPolicy()
	p.MaxConcurrentSessions = 1
	p.MaxSessionTTL = 200 * time.Millisecond
	r := reserveTenant(t, d, "owner", time.Second, p)
	now := time.Now()
	if err := d.Publish(ctx, r, sessions.Session{ID: r.ID, ClientID: r.ClientID, CreatedAt: now, ExpiresAt: now.Add(time.Second)}); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("worker exceeded reserved lifetime", err)
	}
	if err := d.Release(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := d.Release(ctx, r); err != nil {
		t.Fatal("release not idempotent", err)
	}
	// Publishing extends tenant accounting from the startup reservation to browser expiry.
	r = reserveTenant(t, d, "owner", 100*time.Millisecond, p)
	now = time.Now()
	if err := d.Publish(ctx, r, sessions.Session{ID: r.ID, ClientID: r.ClientID, CreatedAt: now, ExpiresAt: now.Add(200 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	id, _ := sessions.NewID()
	if _, err := d.Reserve(ctx, "owner", id, time.Second, p); !errors.Is(err, limits.ErrConcurrency) {
		t.Fatal("ready browser lost its quota reservation", err)
	}
	time.Sleep(100 * time.Millisecond)
	r = reserveTenant(t, d, "owner", 30*time.Millisecond, p)
	time.Sleep(50 * time.Millisecond)
	r = reserveTenant(t, d, "owner", time.Second, p) // Abandoned startup no longer blocks admission.
	// Missing and replacement worker incarnations must free old tenant quota.
	if err := d.Unregister(ctx, w); err != nil {
		t.Fatal(err)
	}
	w.Token = "replacement"
	if err := d.Register(ctx, w, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	replacement := reserveTenant(t, d, "owner", time.Second, p)
	if err := d.Release(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lookup(ctx, "owner", replacement.ID, "starting"); err != nil {
		t.Fatal("old cleanup removed new tenant reservation", err)
	}
	// A physically expired lease also releases quota, without a shutdown callback.
	if err := d.Unregister(ctx, w); err != nil {
		t.Fatal(err)
	}
	w.Token = "short-lived"
	if err := d.Register(ctx, w, 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	reserveTenant(t, d, "owner", time.Second, p)
	time.Sleep(50 * time.Millisecond)
	registerCapacity(t, d, "b", 10)
	reserveTenant(t, d, "owner", time.Second, p)
}

func TestSharedRateLimitAtomicAndWindowExpiry(t *testing.T) {
	d := testDirectory(t)
	replica := &Directory{client: d.client, prefix: d.prefix}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			directory := d
			if i%2 == 0 {
				directory = replica
			}
			delay, err := directory.Allow(context.Background(), "auth", "ip", 7, time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			if delay == 0 {
				accepted.Add(1)
			} else if delay <= 0 || delay > time.Second {
				t.Errorf("invalid retry delay %v", delay)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 7 {
		t.Fatalf("expected exactly 7 requests across replicas, got %d", accepted.Load())
	}
	if delay, err := d.Allow(context.Background(), "auth", "different-ip", 7, time.Second); err != nil || delay != 0 {
		t.Fatal("IP buckets not isolated", err)
	}
	if delay, err := d.Allow(context.Background(), "different-scope", "ip", 7, time.Second); err != nil || delay != 0 {
		t.Fatal("scopes not isolated", err)
	}
	if delay, err := d.Allow(context.Background(), "short", "ip", 1, 30*time.Millisecond); err != nil || delay != 0 {
		t.Fatal(err)
	}
	if delay, err := d.Allow(context.Background(), "short", "ip", 1, 30*time.Millisecond); err != nil || delay <= 0 {
		t.Fatal("over-limit request accepted", err)
	}
	time.Sleep(50 * time.Millisecond)
	if delay, err := d.Allow(context.Background(), "short", "ip", 1, 30*time.Millisecond); err != nil || delay != 0 {
		t.Fatal("expired window not reset", err)
	}
}

func TestSessionRateCountsRejectedAttemptsWithoutLeakingQuota(t *testing.T) {
	d := testDirectory(t)
	registerCapacity(t, d, "a", 1)
	p := admissionPolicy()
	p.SessionRequestsPerMinute = 2
	r := reserveTenant(t, d, "owner", time.Second, p)
	if _, err := d.Reserve(context.Background(), "owner", r.ID, time.Second, p); err != nil {
		t.Fatal("idempotent reserve consumed rate budget", err)
	}
	id, _ := sessions.NewID()
	if _, err := d.Reserve(context.Background(), "owner", id, time.Second, p); !errors.Is(err, sessions.ErrCapacity) {
		t.Fatal("wrong worker capacity error", err)
	}
	if err := d.Release(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	id, _ = sessions.NewID()
	_, err := d.Reserve(context.Background(), "owner", id, time.Second, p)
	var denied *limits.Rejection
	if !errors.Is(err, limits.ErrRate) || !errors.As(err, &denied) || denied.RetryAfter <= 0 {
		t.Fatal("session rate not enforced", err)
	}
	if count := d.client.ZCard(context.Background(), d.tenantKey("owner")).Val(); count != 0 {
		t.Fatal("rate rejection allocated tenant quota")
	}
	reserveTenant(t, d, "other", time.Second, p)
}
