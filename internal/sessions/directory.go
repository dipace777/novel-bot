package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"novel-bot/internal/limits"
)

var ErrLeaseLost = errors.New("worker lease unavailable or owned by another incarnation")

type Worker struct {
	ID       string `json:"id"`
	Token    string `json:"token"` // Unique for each process incarnation.
	URL      string `json:"url"`
	Capacity int    `json:"capacity"`
}

type Record struct {
	ID            string `json:"id"`
	ClientID      string `json:"client_id"`
	WorkerID      string `json:"worker_id"`
	WorkerToken   string `json:"worker_token"`
	WorkerURL     string `json:"worker_url"`
	State         string `json:"state"`
	CreatedMS     int64  `json:"created_at_ms"`
	MaxDurationMS int64  `json:"max_duration_ms"`
	ExpiresMS     int64  `json:"expires_at_ms"`
}

func (r Record) Session() Session {
	return Session{ID: r.ID, ClientID: r.ClientID, CreatedAt: time.UnixMilli(r.CreatedMS).UTC(), ExpiresAt: time.UnixMilli(r.ExpiresMS).UTC(), WorkerURL: r.WorkerURL, WorkerToken: r.WorkerToken}
}

// Directory coordinates metadata; browser processes remain owned by local managers.
type Directory interface {
	Register(context.Context, Worker, time.Duration) error
	Renew(context.Context, Worker, time.Duration) error
	Unregister(context.Context, Worker) error
	Reserve(context.Context, string, string, time.Duration, limits.Policy) (Record, error)
	Lookup(context.Context, string, string, string) (Record, error)
	Publish(context.Context, Record, Session) error
	Release(context.Context, Record) error
}

func NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
