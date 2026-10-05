package sessions

import (
	"context"
	"time"
)

type WorkerClient interface {
	Create(context.Context, Record) (Session, error)
	Delete(context.Context, Record) error
}

// Cluster routes launches and termination to workers selected by the directory.
type Cluster struct {
	directory      Directory
	client         WorkerClient
	startupTimeout time.Duration
}

func NewCluster(directory Directory, client WorkerClient, startupTimeout time.Duration) *Cluster {
	return &Cluster{directory: directory, client: client, startupTimeout: startupTimeout}
}

func (c *Cluster) Create(ctx context.Context, clientID string) (Session, error) {
	id, err := NewID()
	if err != nil {
		return Session{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.startupTimeout+5*time.Second)
	defer cancel()
	r, err := c.directory.Reserve(ctx, clientID, id, c.startupTimeout+10*time.Second)
	if err != nil {
		return Session{}, err
	}
	s, err := c.client.Create(ctx, r)
	if err == nil {
		return s, nil
	}
	// An HTTP failure can mean the browser launched but its response was lost.
	// Attempt worker cleanup before releasing metadata; TTL remains the fallback.
	cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = c.client.Delete(cleanup, r)
	_ = c.directory.Release(cleanup, r)
	return Session{}, err
}
func (c *Cluster) Get(ctx context.Context, clientID, id string) (Session, error) {
	r, err := c.directory.Lookup(ctx, clientID, id, "ready")
	if err != nil {
		return Session{}, err
	}
	return r.Session(), nil
}
func (c *Cluster) Delete(ctx context.Context, clientID, id string) error {
	r, err := c.directory.Lookup(ctx, clientID, id, "ready")
	if err != nil {
		return err
	}
	return c.client.Delete(ctx, r)
}
