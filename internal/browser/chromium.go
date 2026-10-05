// Package browser launches worker-local Chromium processes with isolated profiles.
package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"novel-bot/internal/sessions"
)

type Chromium struct {
	path       string
	profileDir string
	mu         sync.Mutex
	groups     map[int]struct{}
}

// NewChromium accepts an executable or discovers Chromium/Chrome on PATH/macOS.
// profileDir is a parent directory; each launch creates its own temporary profile.
func NewChromium(path, profileDir string) (*Chromium, error) {
	candidates := []string{path}
	if path == "" {
		candidates = []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "/Applications/Chromium.app/Contents/MacOS/Chromium", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	}
	for _, candidate := range candidates {
		resolved, err := exec.LookPath(candidate)
		if err == nil {
			return &Chromium{path: resolved, profileDir: profileDir, groups: make(map[int]struct{})}, nil
		}
	}
	return nil, errors.New("Chromium executable not found; install Chromium/Chrome or set CHROMIUM_PATH")
}

var _ sessions.Launcher = (*Chromium)(nil)

func (c *Chromium) Launch(ctx context.Context) (sessions.Browser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	profile, err := os.MkdirTemp(c.profileDir, "novel-bot-browser-")
	if err != nil {
		return nil, fmt.Errorf("create browser profile: %w", err)
	}
	// Port 0 asks Chromium to bind an available port itself, avoiding a
	// reserve-close-launch race. DevToolsActivePort contains the resulting endpoint.
	cmd := exec.Command(c.path, "--headless", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "about:blank")
	configureProcess(cmd)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("start Chromium: %w", err)
	}
	c.mu.Lock()
	c.groups[cmd.Process.Pid] = struct{}{}
	c.mu.Unlock()
	p := &process{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		p.killOnce.Do(func() { p.killErr = killProcess(cmd) })
		p.cleanupErr = os.RemoveAll(profile)
		c.mu.Lock()
		delete(c.groups, cmd.Process.Pid)
		c.mu.Unlock()
		close(p.done)
	}()
	ready := false
	defer func() {
		if !ready {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = p.Stop(cleanup)
		}
	}()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err == nil {
			endpoint, err := parseEndpoint(data)
			if err == nil {
				p.endpoint = endpoint
				ready = true
				return p, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for Chromium debugging endpoint: %w", ctx.Err())
		case <-p.done:
			return nil, errors.New("Chromium exited before its debugging endpoint became available")
		case <-ticker.C:
		}
	}
}

func parseEndpoint(data []byte) (string, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		return "", errors.New("invalid Chromium debugging endpoint")
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	path := strings.TrimSpace(lines[1])
	if err != nil || port < 1 || port > 65535 || !strings.HasPrefix(path, "/devtools/browser/") || strings.ContainsAny(path, "?# \t\r") {
		return "", errors.New("invalid Chromium debugging endpoint")
	}
	return (&url.URL{Scheme: "ws", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: path}).String(), nil
}

type process struct {
	cmd        *exec.Cmd
	endpoint   string
	done       chan struct{}
	killOnce   sync.Once
	killErr    error
	cleanupErr error
}

func (p *process) Endpoint() string      { return p.endpoint }
func (p *process) Done() <-chan struct{} { return p.done }

func (p *process) Stop(ctx context.Context) error {
	p.killOnce.Do(func() { p.killErr = killProcess(p.cmd) })
	if p.killErr != nil {
		return fmt.Errorf("terminate Chromium: %w", p.killErr)
	}
	select {
	case <-p.done:
		return p.cleanupErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ProcessGroups includes launching and stopping browsers until they are reaped.
// On Unix, each browser is launched with its PID as the process-group ID.
func (c *Chromium) ProcessGroups() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]int, 0, len(c.groups))
	for id := range c.groups {
		ids = append(ids, id)
	}
	return ids
}
