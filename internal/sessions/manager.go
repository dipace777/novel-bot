package sessions

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type Options struct {
	MaxSessions    int
	TTL            time.Duration
	StartupTimeout time.Duration
	OnStop         func(Session)
}

type managedSession struct {
	Session
	browser Browser
}

// Manager owns local browser processes; the directory coordinates their location.
type Manager struct {
	launcher  Launcher
	options   Options
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	sessions  map[string]*managedSession
	starting  map[string]struct{}
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
	return &Manager{launcher: launcher, options: options, ctx: ctx, cancel: cancel, sessions: make(map[string]*managedSession), starting: make(map[string]struct{}), done: make(chan struct{})}, nil
}

func (m *Manager) Create(ctx context.Context, clientID string) (Session, error) {
	if clientID == "" {
		return Session{}, fmt.Errorf("browser session requires a client ID")
	}
	id, err := NewID()
	if err != nil {
		return Session{}, err
	}
	return m.CreateWithID(ctx, clientID, id)
}

// CreateWithID is used for a session already reserved in the shared directory.
func (m *Manager) CreateWithID(ctx context.Context, clientID, id string) (Session, error) {
	if len(id) != 32 || clientID == "" {
		return Session{}, fmt.Errorf("invalid session identity")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return Session{}, fmt.Errorf("invalid session identity")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Session{}, ErrClosed
	}
	_, active := m.sessions[id]
	_, pending := m.starting[id]
	if active || pending {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("session already exists")
	}
	if m.reserved >= m.options.MaxSessions {
		m.mu.Unlock()
		return Session{}, ErrCapacity
	}
	m.reserved++
	m.starting[id] = struct{}{}
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	published := false
	defer func() {
		m.mu.Lock()
		delete(m.starting, id)
		if !published {
			m.reserved--
		}
		m.mu.Unlock()
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
	removed := m.sessions[s.ID] == s
	if removed {
		delete(m.sessions, s.ID)
		m.reserved--
	}
	m.mu.Unlock()
	if removed && m.options.OnStop != nil {
		m.options.OnStop(s.Session)
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
