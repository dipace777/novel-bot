package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"novel-bot/internal/sessions"
)

func awaitStop(t *testing.T, a *Agent) {
	t.Helper()
	select {
	case <-a.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestDrainPreservesSessionsAndLeaseAndRejectsLateReservations(t *testing.T) {
	d := &directoryStub{}
	a, b := testAgent(t, d)
	id, _ := sessions.NewID()
	if _, err := a.CreateReserved(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	if err := a.BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := *a.Status().DrainDeadline
	if err := a.BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !deadline.Equal(*a.Status().DrainDeadline) {
		t.Fatal("repeat drain extended deadline")
	}
	late, _ := sessions.NewID()
	if _, err := a.CreateReserved(context.Background(), "tenant", late); !errors.Is(err, sessions.ErrDraining) {
		t.Fatal(err)
	}
	d.mu.Lock()
	released := d.releases
	d.mu.Unlock()
	if released == 0 {
		t.Fatal("rejected reservation not released")
	}
	// Beyond the original 150ms lease, draining still renews and serves sessions.
	time.Sleep(300 * time.Millisecond)
	if a.Ready() != nil || !errors.Is(a.Accepting(), sessions.ErrDraining) {
		t.Fatal("lease/admission conflated")
	}
	if _, err := a.Get(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.done:
		t.Fatal("drain killed active browser")
	default:
	}
	if err := a.Delete(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	awaitStop(t, a)
	if a.Err() != nil || a.Status().Forced {
		t.Fatal("clean drain failed", a.Err())
	}
}

func TestDrainDeadlineForcesCleanup(t *testing.T) {
	d := &directoryStub{}
	b := &browserStub{done: make(chan struct{})}
	a, err := NewAgent(context.Background(), d, launcherStub{b}, sessions.Worker{ID: "forced", URL: "http://localhost:8090"}, sessions.Options{MaxSessions: 1, TTL: time.Minute, StartupTimeout: time.Second}, time.Second, 60*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	id, _ := sessions.NewID()
	if _, err := a.CreateReserved(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	if err := a.BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitStop(t, a)
	if !a.Status().Forced || a.Err() != nil {
		t.Fatal("forced drain not recorded", a.Err())
	}
	select {
	case <-b.done:
	default:
		t.Fatal("deadline leaked browser")
	}
}

func TestDrainOutageStillFencesAtLeaseDeadline(t *testing.T) {
	d := &directoryStub{}
	a, b := testAgent(t, d)
	id, _ := sessions.NewID()
	if _, err := a.CreateReserved(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	if err := a.BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.renewErr = errors.New("Redis outage")
	d.mu.Unlock()
	awaitStop(t, a)
	if !errors.Is(a.Err(), sessions.ErrLeaseLost) || a.Status().Forced {
		t.Fatal("drain overrode fencing", a.Err())
	}
	select {
	case <-b.done:
	default:
		t.Fatal("browser survived lease loss")
	}
}

func TestFailedDrainPublicationKeepsLocalAdmissionClosed(t *testing.T) {
	d := &directoryStub{drainErr: errors.New("Redis write unavailable")}
	a, _ := testAgent(t, d)
	id, _ := sessions.NewID()
	if _, err := a.CreateReserved(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	if a.BeginDrain(context.Background()) == nil {
		t.Fatal("publication failure hidden")
	}
	deadline := *a.Status().DrainDeadline
	if !errors.Is(a.Accepting(), sessions.ErrDraining) {
		t.Fatal("failed drain reopened admission")
	}
	d.mu.Lock()
	d.drainErr = nil
	d.mu.Unlock()
	if err := a.BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !deadline.Equal(*a.Status().DrainDeadline) {
		t.Fatal("retry changed deadline")
	}
}

type blockedLauncher struct {
	entered, resume chan struct{}
	b               *browserStub
}

type blockedDrainDirectory struct {
	directoryStub
	entered, resume chan struct{}
}

func (d *blockedDrainDirectory) Drain(ctx context.Context, _ sessions.Worker) error {
	close(d.entered)
	select {
	case <-d.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestEmptyDrainWaitsForRedisConfirmation(t *testing.T) {
	d := &blockedDrainDirectory{entered: make(chan struct{}), resume: make(chan struct{})}
	b := &browserStub{done: make(chan struct{})}
	a, err := NewAgent(context.Background(), d, launcherStub{b}, sessions.Worker{ID: "confirmation", URL: "http://localhost:8090"}, sessions.Options{MaxSessions: 1, TTL: time.Minute, StartupTimeout: time.Second}, 150*time.Millisecond, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	confirmed := make(chan error, 1)
	go func() { confirmed <- a.BeginDrain(context.Background()) }()
	<-d.entered
	time.Sleep(300 * time.Millisecond)
	select {
	case <-a.Done():
		t.Fatal("empty worker retired before confirmation")
	default:
	}
	if !a.Status().DrainPublishing || a.Ready() != nil {
		t.Fatal("confirmation/lease state incorrect")
	}
	close(d.resume)
	if err := <-confirmed; err != nil {
		t.Fatal(err)
	}
	awaitStop(t, a)
}

func TestRejectedReservationReleaseRetriesDuringDrain(t *testing.T) {
	d := &directoryStub{}
	b := &browserStub{done: make(chan struct{})}
	a, err := NewAgent(context.Background(), d, launcherStub{b}, sessions.Worker{ID: "cleanup", URL: "http://localhost:8090"}, sessions.Options{MaxSessions: 1, TTL: time.Minute, StartupTimeout: time.Second}, time.Second, 3*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	id, _ := sessions.NewID()
	if _, err := a.CreateReserved(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	if err := a.BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.releaseErr = errors.New("temporary outage")
	d.mu.Unlock()
	late, _ := sessions.NewID()
	if _, err := a.CreateReserved(context.Background(), "tenant", late); !errors.Is(err, sessions.ErrDraining) {
		t.Fatal(err)
	}
	if a.PendingCleanup() != 1 {
		t.Fatal("rejected reservation release was not queued")
	}
	d.mu.Lock()
	d.releaseErr = nil
	d.mu.Unlock()
	until := time.Now().Add(2 * time.Second)
	for a.PendingCleanup() != 0 {
		if time.Now().After(until) {
			t.Fatal("cleanup did not retry during drain")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := a.Delete(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	awaitStop(t, a)
	if a.Err() != nil || a.Status().Forced {
		t.Fatal("cleanup caused forced exit", a.Err())
	}
}

func (l blockedLauncher) Launch(ctx context.Context) (sessions.Browser, error) {
	close(l.entered)
	select {
	case <-l.resume:
		return l.b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestAdmittedStartupCanPublishDuringDrain(t *testing.T) {
	d := &directoryStub{}
	l := blockedLauncher{entered: make(chan struct{}), resume: make(chan struct{}), b: &browserStub{done: make(chan struct{})}}
	a, err := NewAgent(context.Background(), d, l, sessions.Worker{ID: "startup", URL: "http://localhost:8090"}, sessions.Options{MaxSessions: 1, TTL: time.Minute, StartupTimeout: time.Second}, time.Second, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	id, _ := sessions.NewID()
	created := make(chan error, 1)
	go func() { _, err := a.CreateReserved(context.Background(), "tenant", id); created <- err }()
	select {
	case <-l.entered:
	case <-time.After(time.Second):
		t.Fatal("startup did not enter launcher")
	}
	if err := a.BeginDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Status().InFlightCreates != 1 {
		t.Fatal("startup not counted")
	}
	close(l.resume)
	if err := <-created; err != nil {
		t.Fatal("admitted startup rejected", err)
	}
	if _, err := a.Get(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(context.Background(), "tenant", id); err != nil {
		t.Fatal(err)
	}
	awaitStop(t, a)
}
