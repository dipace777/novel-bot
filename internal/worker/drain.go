package worker

import (
	"context"
	"time"

	"novel-bot/internal/sessions"
)

// BeginDrain closes local admission immediately, then atomically marks Redis.
// Retries do not extend the deadline, including when the first Redis write fails.
func (a *Agent) BeginDrain(ctx context.Context) error {
	a.mu.Lock()
	if a.stopped || !time.Now().Before(a.deadline) {
		a.mu.Unlock()
		return sessions.ErrLeaseLost
	}
	first := a.drainStarted.IsZero()
	if first {
		a.drainStarted = time.Now().UTC()
		a.drainDeadline = a.drainStarted.Add(a.drainTimeout)
	}
	a.drainOps++
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.drainOps--; a.mu.Unlock() }()
	if first {
		a.event("drain_started")
	}
	err := a.directory.Drain(ctx, a.worker)
	if err != nil {
		a.event("drain_publish_failure")
	}
	return err
}

// Accepting is for new placement/readiness only. Ready retains lease-health
// semantics so existing CDP connections remain usable during a healthy drain.
func (a *Agent) Accepting() error {
	if err := a.Ready(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.drainStarted.IsZero() {
		return sessions.ErrDraining
	}
	return nil
}

func (a *Agent) Status() sessions.WorkerStatus {
	a.mu.Lock()
	s := sessions.WorkerStatus{WorkerID: a.worker.ID, State: sessions.WorkerReady,
		LeaseValid: !a.stopped && time.Now().Before(a.deadline), InFlightCreates: a.inFlight,
		PendingCleanup: len(a.pendingCleanup), Forced: a.forced, DrainPublishing: a.drainOps > 0}
	if !a.drainStarted.IsZero() {
		s.State = sessions.WorkerDraining
		started, deadline := a.drainStarted, a.drainDeadline
		s.DrainStartedAt, s.DrainDeadline = &started, &deadline
	}
	if !s.LeaseValid {
		s.State = sessions.WorkerUnavailable
	}
	s.Accepting = s.LeaseValid && s.State == sessions.WorkerReady
	a.mu.Unlock()
	s.Sessions = a.local.Stats()
	return s
}

func (a *Agent) finishDrain() bool {
	s := a.Status()
	// Lease expiry always wins over a drain deadline or an empty worker.
	if !s.LeaseValid {
		a.fail(sessions.ErrLeaseLost)
		a.event("lease_lost")
		return true
	}
	if s.DrainDeadline == nil {
		return false
	}
	if s.Sessions.Reserved == 0 && s.InFlightCreates == 0 && s.PendingCleanup == 0 && !s.DrainPublishing {
		a.event("drain_completed")
		return true
	}
	if !time.Now().Before(*s.DrainDeadline) {
		a.mu.Lock()
		a.forced = true
		a.mu.Unlock()
		a.event("drain_forced")
		return true
	}
	return false
}

// Err is the final shutdown cause after Done closes. Planned drains return nil.
func (a *Agent) Err() error { a.mu.Lock(); defer a.mu.Unlock(); return a.stopErr }
func (a *Agent) fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopErr == nil {
		a.stopErr = err
	}
}

func (a *Agent) release(r sessions.Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.directory.Release(ctx, r); err != nil {
		a.event("cleanup_failure")
		a.logger.Error("browser directory cleanup failed; queued for retry", "session_id", r.ID)
		a.mu.Lock()
		a.pendingCleanup[r.ID] = r
		a.mu.Unlock()
	}
}
