package redis

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"novel-bot/internal/limits"
	"novel-bot/internal/sessions"
)

func TestDrainSurvivesRenewAndPreservesExistingReservations(t *testing.T) {
	d := testDirectory(t)
	ctx := context.Background()
	w := sessions.Worker{ID: "a", Token: "incarnation-a", URL: "http://localhost:8090", Capacity: 2}
	if err := d.Register(ctx, w, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	id, _ := sessions.NewID()
	r, err := d.Reserve(ctx, "owner", id, time.Second, limits.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Drain(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := d.Drain(ctx, w); err != nil {
		t.Fatal("duplicate drain", err)
	}
	if err := d.Renew(ctx, w, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(ctx, w, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	data, err := d.client.Get(ctx, d.workerKey(w)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var stored sessions.Worker
	if err := json.Unmarshal(data, &stored); err != nil || stored.State != sessions.WorkerDraining {
		t.Fatal("lease reopened admission", stored, err)
	}
	late, _ := sessions.NewID()
	if _, err := d.Reserve(ctx, "owner", late, time.Second, limits.DefaultPolicy()); !errors.Is(err, sessions.ErrCapacity) {
		t.Fatal("draining worker selected", err)
	}
	if _, err := d.Lookup(ctx, "owner", id, "starting"); err != nil {
		t.Fatal("reservation lost", err)
	}
	now := time.Now()
	if err := d.Publish(ctx, r, sessions.Session{ID: id, ClientID: "owner", CreatedAt: now, ExpiresAt: now.Add(time.Second)}); err != nil {
		t.Fatal("pre-drain launch cannot publish", err)
	}
	if _, err := d.Lookup(ctx, "owner", id, "ready"); err != nil {
		t.Fatal("session not routable", err)
	}
	if err := d.Release(ctx, r); err != nil {
		t.Fatal(err)
	}
	if count := d.client.ZCard(ctx, d.tenantKey("owner")).Val(); count != 0 {
		t.Fatal("tenant quota leaked", count)
	}
	stale := w
	stale.Token = "replaced"
	if err := d.Drain(ctx, stale); !errors.Is(err, sessions.ErrLeaseLost) {
		t.Fatal("stale incarnation drained worker", err)
	}
}

func TestReservationsExcludeDrainingWorkerAcrossConcurrentCalls(t *testing.T) {
	d := testDirectory(t)
	ctx := context.Background()
	a := sessions.Worker{ID: "a", Token: "a", URL: "http://localhost:8090", Capacity: 100}
	b := sessions.Worker{ID: "b", Token: "b", URL: "http://localhost:8091", Capacity: 100}
	for _, w := range []sessions.Worker{a, b} {
		if err := d.Register(ctx, w, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	before := d.client.PTTL(ctx, d.workerKey(a)).Val()
	if err := d.Drain(ctx, a); err != nil {
		t.Fatal(err)
	}
	if after := d.client.PTTL(ctx, d.workerKey(a)).Val(); after > before {
		t.Fatal("drain extended lease", before, after)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, _ := sessions.NewID()
			r, err := d.Reserve(ctx, "tenant", id, time.Second, limits.Policy{MaxConcurrentSessions: 100, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 1000})
			if err != nil || r.WorkerID != "b" {
				t.Errorf("invalid placement: %s %v", r.WorkerID, err)
			}
		}()
	}
	wg.Wait()
}
