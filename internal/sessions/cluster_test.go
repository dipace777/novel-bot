package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"novel-bot/internal/limits"
)

type clusterDirectoryStub struct {
	Directory
	releases int
	reserves int
	policy   limits.Policy
}

func (d *clusterDirectoryStub) Reserve(_ context.Context, clientID, id string, _ time.Duration, p limits.Policy) (Record, error) {
	d.reserves++
	d.policy = p
	return Record{ID: id, ClientID: clientID, MaxDurationMS: p.MaxSessionTTL.Milliseconds()}, nil
}
func (d *clusterDirectoryStub) Release(context.Context, Record) error { d.releases++; return nil }

type clusterClientStub struct{ deleteErr error }

func (clusterClientStub) Create(context.Context, Record) (Session, error) {
	return Session{}, errors.New("ambiguous worker response")
}
func (c clusterClientStub) Delete(context.Context, Record) error { return c.deleteErr }

func TestAmbiguousWorkerFailureRetainsQuotaUntilConfirmedCleanup(t *testing.T) {
	for _, deleteErr := range []error{nil, ErrNotFound, errors.New("worker unreachable")} {
		d := &clusterDirectoryStub{}
		c := NewCluster(d, clusterClientStub{deleteErr}, time.Second, limits.ProviderFunc(func(context.Context, string) (limits.Policy, error) { return limits.DefaultPolicy(), nil }))
		if _, err := c.Create(context.Background(), "tenant"); err == nil {
			t.Fatal("failed worker returned success")
		}
		expected := 0
		if deleteErr == nil {
			expected = 1
		}
		if d.releases != expected {
			t.Fatalf("cleanup %v released %d reservations, want %d", deleteErr, d.releases, expected)
		}
		if d.policy != limits.DefaultPolicy() {
			t.Fatal("tenant policy not passed to reservation")
		}
	}
}
func TestTenantPolicyFailurePreventsReservation(t *testing.T) {
	d := &clusterDirectoryStub{}
	c := NewCluster(d, clusterClientStub{}, time.Second, limits.ProviderFunc(func(context.Context, string) (limits.Policy, error) {
		return limits.Policy{}, errors.New("policy database unavailable")
	}))
	if _, err := c.Create(context.Background(), "tenant"); err == nil || d.reserves != 0 {
		t.Fatal("creation did not fail closed on policy lookup")
	}
}
