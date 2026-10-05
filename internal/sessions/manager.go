package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type Options struct {
	MaxSessions    int
	TTL            time.Duration
	StartupTimeout time.Duration
}

type managedSession struct {
	Session
	browser Browser
}

// Manager owns local processes. A future distributed implementation can store
// session -> worker leases in Redis while leaving process ownership on workers.
type Manager struct {
	launcher  Launcher
	options   Options
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	sessions  map[string]*managedSession
	reserved  int // Includes launches and browsers still being stopped.
	closed    bool
	wg        sync.WaitGroup
	closeOnce sync.Once
	done      chan struct{}
}

func NewManager(launcher Launcher, options Options) (*Manager, error) {
	if launcher == nil || options.MaxSessions <= 0 || options.TTL <= 0 || options.StartupTimeout <= 0 {
		return nil, fmt.Errorf("browser manager requires a launcher, positive capacity, TTL, and startup timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{launcher: launcher, options: options, ctx: ctx, cancel: cancel, sessions: make(map[string]*managedSession), done: make(chan struct{})}, nil
}

func (m *Manager) Create(ctx context.Context, clientID string) (Session, error) {
	if clientID == "" {
		return Session{}, fmt.Errorf("browser session requires a client ID")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Session{}, err
	}
	id := hex.EncodeToString(random[:])
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Session{}, ErrClosed
	}
	if m.reserved >= m.options.MaxSessions {
		m.mu.Unlock()
		return Session{}, ErrCapacity
	}
	m.reserved++
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	published := false
	defer func() {
		if !published {
			m.mu.Lock()
			m.reserved--
			m.mu.Unlock()
		}
	}()

	launchCtx, cancel := context.WithTimeout(ctx, m.options.StartupTimeout)
	stopShutdown := context.AfterFunc(m.ctx, cancel)
	defer stopShutdown()
	defer cancel()
	browser, err := m.launcher.Launch(launchCtx)
	if err != nil {
		return Session{}, fmt.Errorf("launch browser: %w", err)
	}
	m.mu.Lock()
	if m.closed || launchCtx.Err() != nil {
		m.mu.Unlock()
		stopBrowser(browser)
		if err := launchCtx.Err(); err != nil {
			return Session{}, err
		}
		return Session{}, ErrClosed
	}
	select {
	case <-browser.Done():
		m.mu.Unlock()
		return Session{}, fmt.Errorf("browser exited during startup")
	default:
	}
	now := time.Now().UTC()
	s := &managedSession{Session: Session{ID: id, ClientID: clientID, CreatedAt: now, ExpiresAt: now.Add(m.options.TTL), Endpoint: browser.Endpoint(), Done: browser.Done()}, browser: browser}
	m.sessions[id] = s
	published = true
	m.wg.Add(1)
	m.mu.Unlock()
	go m.watch(s)
	return s.Session, nil
}

func (m *Manager) Get(ctx context.Context, clientID, id string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if m.closed || !ok || s.ClientID != clientID || !time.Now().Before(s.ExpiresAt) {
		return Session{}, ErrNotFound
	}
	select {
	case <-s.Done:
		return Session{}, ErrNotFound
	default:
		return s.Session, nil
	}
}

func (m *Manager) Delete(ctx context.Context, clientID, id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok || s.ClientID != clientID {
		return ErrNotFound
	}
	if err := s.browser.Stop(ctx); err != nil {
		return err
	}
	m.remove(s)
	return nil
}

func (m *Manager) watch(s *managedSession) {
	defer m.wg.Done()
	timer := time.NewTimer(time.Until(s.ExpiresAt))
	defer timer.Stop()
	select {
	case <-s.Done:
	case <-timer.C:
		stopBrowser(s.browser)
	case <-m.ctx.Done():
		stopBrowser(s.browser)
	}
	// Keep the capacity reservation until the process has actually exited.
	<-s.Done
	m.remove(s)
}

func (m *Manager) remove(s *managedSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.ID] == s {
		delete(m.sessions, s.ID)
		m.reserved--
	}
}

func (m *Manager) Close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		m.cancel()
		go func() { m.wg.Wait(); close(m.done) }()
	})
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func stopBrowser(browser Browser) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = browser.Stop(ctx)
}
