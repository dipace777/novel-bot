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
	directory sessions.Directory
	local     *sessions.Manager
	worker    sessions.Worker
	leaseTTL  time.Duration
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	deadline  time.Time
	stopped   bool
	logger    *slog.Logger
}

func NewAgent(ctx context.Context, directory sessions.Directory, launcher sessions.Launcher, w sessions.Worker, options sessions.Options, leaseTTL time.Duration, logger *slog.Logger) (*Agent, error) {
	if leaseTTL < 3*time.Millisecond || w.ID == "" || w.URL == "" || directory == nil || logger == nil {
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
	life, cancel := context.WithCancel(context.Background())
	a := &Agent{directory: directory, worker: w, leaseTTL: leaseTTL, cancel: cancel, done: make(chan struct{}), logger: logger}
	options.OnStop = func(s sessions.Session) {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := directory.Release(cleanup, sessions.Record{ID: s.ID, ClientID: s.ClientID, WorkerID: w.ID, WorkerToken: w.Token}); err != nil {
			logger.Error("browser directory cleanup failed", "session_id", s.ID)
		}
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
	s, err := a.local.CreateWithID(ctx, clientID, id)
	if err != nil {
		_ = a.directory.Release(context.Background(), r)
		return sessions.Session{}, err
	}
	err = a.Ready()
	if err == nil {
		err = a.directory.Publish(ctx, r, s)
	}
	if err != nil {
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
	return a.local.Delete(ctx, clientID, id)
}

func (a *Agent) run(ctx context.Context) {
	defer close(a.done)
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
			a.logger.Error("worker browser cleanup failed")
		}
		<-retired
	}()
	ticker := time.NewTicker(a.leaseTTL / 3)
	defer ticker.Stop()
	timer := time.NewTimer(time.Until(a.deadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.logger.Error("worker lease expired; stopping browsers")
			return
		case <-ticker.C:
			a.mu.Lock()
			deadline := a.deadline
			a.mu.Unlock()
			started := time.Now()
			renew, cancel := context.WithDeadline(ctx, deadline)
			err := a.directory.Renew(renew, a.worker, a.leaseTTL)
			cancel()
			if errors.Is(err, sessions.ErrLeaseLost) || !time.Now().Before(deadline) {
				a.logger.Error("worker lease lost; stopping browsers")
				return
			}
			if err != nil {
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
