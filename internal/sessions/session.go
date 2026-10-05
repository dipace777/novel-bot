// Package sessions manages the lifecycle and ownership of browser sessions.
package sessions

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("browser session not found")
	ErrCapacity = errors.New("browser session capacity reached")
	ErrClosed   = errors.New("browser session manager closed")
)

// Browser is a running browser owned by a worker, independent of the launch request.
type Browser interface {
	Endpoint() string
	Done() <-chan struct{}
	Stop(context.Context) error
}

type Launcher interface {
	Launch(context.Context) (Browser, error)
}

// Session contains worker-local connection details. HTTP handlers expose only
// metadata and the authenticated proxy URL, never the browser's debugging port.
type Session struct {
	ID          string
	ClientID    string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	WorkerURL   string
	WorkerToken string
	Endpoint    string
	Done        <-chan struct{}
}
