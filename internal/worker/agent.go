// Package worker owns browser execution and authenticated inter-worker transport.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"novel-bot/internal/sessions"
)

type Agent struct {
	directory      sessions.Directory
	local          *sessions.Manager
	worker         sessions.Worker
	leaseTTL       time.Duration
	drainTimeout   time.Duration
	drainStarted   time.Time
	drainDeadline  time.Time
	inFlight       int
	drainOps       int
	forced         bool
	stopErr        error
	cancel         context.CancelFunc
	done           chan struct{}
	mu             sync.Mutex
	deadline       time.Time
	stopped        bool
	logger         *slog.Logger
	pendingCleanup map[string]sessions.Record
	observer       sessions.Observer
}

func NewAgent(ctx context.Context, directory sessions.Directory, launcher sessions.Launcher, w sessions.Worker, options sessions.Options, leaseTTL, drainTimeout time.Duration, logger *slog.Logger) (*Agent, error) {
	if leaseTTL < 3*time.Millisecond || drainTimeout < time.Millisecond || w.ID == "" || w.URL == "" || directory == nil || logger == nil {
		return nil, errors.New("invalid worker configuration")
	}
	if _, err := Origin(w.URL); err != nil {
		return nil, err
	}
	token, err := sessions.NewID()
	if err != nil {
		return nil, err
	}
	w.Token = token
	w.Capacity = options.MaxSessions
	w.State = sessions.WorkerReady
	life, cancel := context.WithCancel(context.Background())
	a := &Agent{directory: directory, worker: w, leaseTTL: leaseTTL, drainTimeout: drainTimeout, cancel: cancel, done: make(chan struct{}), logger: logger, pendingCleanup: make(map[string]sessions.Record), observer: options.Observer}
	options.OnStop = func(s sessions.Session) {
		a.release(sessions.Record{ID: s.ID, ClientID: s.ClientID, WorkerID: w.ID, WorkerToken: w.Token})
	}
	a.local, err = sessions.NewManager(launcher, options)
	if err != nil {
		cancel()
		return nil, err
	}
	started := time.Now()
	if err := directory.Register(ctx, w, leaseTTL); err != nil {
		cancel()
		_ = a.local.Close(context.Background())
		return nil, err
	}
	a.deadline = started.Add(leaseTTL)
	if time.Now().After(a.deadline) {
		cancel()
		_ = directory.Unregister(ctx, w)
		_ = a.local.Close(context.Background())
		return nil, sessions.ErrLeaseLost
	}
	go a.retryCleanup(life)
	go a.run(life)
	return a, nil
}

func (a *Agent) Worker() sessions.Worker { return a.worker }
func (a *Agent) Done() <-chan struct{}   { return a.done }
func (a *Agent) Ready() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped || !time.Now().Before(a.deadline) {
		return sessions.ErrLeaseLost
	}
	return nil
}
func (a *Agent) CreateReserved(ctx context.Context, clientID, id string) (sessions.Session, error) {
	if err := a.Ready(); err != nil {
		return sessions.Session{}, err
	}
	r, err := a.directory.Lookup(ctx, clientID, id, "starting")
	if err != nil {
		return sessions.Session{}, err
	}
	if r.WorkerID != a.worker.ID || r.WorkerToken != a.worker.Token {
		return sessions.Session{}, sessions.ErrNotFound
	}
	// Local admission is the drain race boundary. Already admitted launches
	// may publish; late pre-drain reservations are rejected and released.
	a.mu.Lock()
	if a.stopped || !time.Now().Before(a.deadline) || !a.drainStarted.IsZero() {
		err := sessions.ErrDraining
		if a.stopped || !time.Now().Before(a.deadline) {
			err = sessions.ErrLeaseLost
		}
		a.mu.Unlock()
		a.release(r)
		return sessions.Session{}, err
	}
	a.inFlight++
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.inFlight--; a.mu.Unlock() }()
	s, err := a.local.CreateWithTTL(ctx, clientID, id, time.Duration(r.MaxDurationMS)*time.Millisecond)
	if err != nil {
		a.release(r)
		return sessions.Session{}, err
	}
	err = a.Ready()
	if err == nil {
		err = a.directory.Publish(ctx, r, s)
	}
	if err != nil {
		a.event("publish_failure")
		return a.failedPublish(s, err)
	}
	s.WorkerURL, s.WorkerToken = a.worker.URL, a.worker.Token
	// The browser endpoint remains private to the owning worker.
	s.Endpoint = ""
	s.Done = nil
	return s, nil
}
func (a *Agent) failedPublish(s sessions.Session, err error) (sessions.Session, error) {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.local.Delete(cleanup, s.ClientID, s.ID)
	return sessions.Session{}, err
}
func (a *Agent) Get(ctx context.Context, clientID, id string) (sessions.Session, error) {
	if err := a.Ready(); err != nil {
		return sessions.Session{}, err
	}
	r, err := a.directory.Lookup(ctx, clientID, id, "ready")
	if err != nil {
		return sessions.Session{}, err
	}
	if r.WorkerID != a.worker.ID || r.WorkerToken != a.worker.Token {
		return sessions.Session{}, sessions.ErrNotFound
	}
	return a.local.Get(ctx, clientID, id)
}
func (a *Agent) Delete(ctx context.Context, clientID, id string) error {
	if err := a.local.Delete(ctx, clientID, id); err != nil {
		return err
	}
	// The watcher may have begun its cleanup callback concurrently. Explicitly
	// release before acknowledging deletion; conditional release is idempotent.
	return a.directory.Release(ctx, sessions.Record{ID: id, ClientID: clientID, WorkerID: a.worker.ID, WorkerToken: a.worker.Token})
}

func (a *Agent) run(ctx context.Context) {
	defer close(a.done)
	defer a.cancel()
	defer func() {
		a.mu.Lock()
		a.stopped = true
		a.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Retire routing and stop processes concurrently: a Redis outage must not
		// delay terminating browsers after the local lease deadline.
		retired := make(chan struct{})
		go func() { defer close(retired); _ = a.directory.Unregister(cleanup, a.worker) }()
		if err := a.local.Close(cleanup); err != nil {
			a.fail(err)
			a.logger.Error("worker browser cleanup failed")
		}
		<-retired
	}()
	drainTicker := time.NewTicker(min(250*time.Millisecond, a.drainTimeout/4))
	defer drainTicker.Stop()
	ticker := time.NewTicker(a.leaseTTL / 3)
	defer ticker.Stop()
	timer := time.NewTimer(time.Until(a.deadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-drainTicker.C:
			if a.finishDrain() {
				return
			}
		case <-timer.C:
			a.fail(sessions.ErrLeaseLost)
			a.event("lease_lost")
			a.logger.Error("worker lease expired; stopping browsers")
			return
		case <-ticker.C:
			a.mu.Lock()
			deadline := a.deadline
			w := a.worker
			if !a.drainStarted.IsZero() {
				w.State = sessions.WorkerDraining
			}
			a.mu.Unlock()
			started := time.Now()
			renew, cancel := context.WithDeadline(ctx, deadline)
			err := a.directory.Renew(renew, w, a.leaseTTL)
			cancel()
			if errors.Is(err, sessions.ErrLeaseLost) || !time.Now().Before(deadline) {
				a.fail(sessions.ErrLeaseLost)
				a.event("lease_lost")
				a.logger.Error("worker lease lost; stopping browsers")
				return
			}
			if err != nil {
				a.event("lease_renew_failure")
				a.logger.Warn("worker heartbeat failed")
				continue
			}
			a.mu.Lock()
			a.deadline = started.Add(a.leaseTTL)
			deadline = a.deadline
			a.mu.Unlock()
			timer.Reset(time.Until(deadline))
		}
	}
}
func (a *Agent) Close(ctx context.Context) error {
	a.cancel()
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Retry transient cleanup failures independently of heartbeat renewal. On lease
// loss, admission reconciles this incarnation's records and frees its quotas.
func (a *Agent) retryCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.mu.Lock()
			pending := make([]sessions.Record, 0, len(a.pendingCleanup))
			for _, record := range a.pendingCleanup {
				pending = append(pending, record)
			}
			a.mu.Unlock()
			for _, record := range pending {
				if ctx.Err() != nil {
					return
				}
				cleanup, cancel := context.WithTimeout(ctx, 2*time.Second)
				err := a.directory.Release(cleanup, record)
				cancel()
				if err == nil {
					a.mu.Lock()
					delete(a.pendingCleanup, record.ID)
					a.mu.Unlock()
				}
			}
		}
	}
}

func (a *Agent) Stats() sessions.Stats { return a.local.Stats() }
func (a *Agent) PendingCleanup() int   { a.mu.Lock(); defer a.mu.Unlock(); return len(a.pendingCleanup) }
func (a *Agent) event(name string) {
	if a.observer != nil {
		a.observer.Event(name)
	}
}
