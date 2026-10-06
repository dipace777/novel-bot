package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"novel-bot/internal/limits"
	"novel-bot/internal/sessions"
)

var _ sessions.Directory = (*Directory)(nil)

func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 2*time.Second)
}
func (d *Directory) workerKey(w sessions.Worker) string { return d.prefix + "worker:" + w.ID }
func (d *Directory) slotsKey(id, token string) string   { return d.prefix + "slots:" + id + ":" + token }
func (d *Directory) tenantKey(id string) string         { return d.prefix + "tenant:" + id }
func (d *Directory) sessionKey(id string) string        { return d.prefix + "session:" + id }

func (d *Directory) Register(ctx context.Context, w sessions.Worker, ttl time.Duration) error {
	return d.lease(ctx, w, ttl, "register")
}
func (d *Directory) Renew(ctx context.Context, w sessions.Worker, ttl time.Duration) error {
	return d.lease(ctx, w, ttl, "renew")
}
func (d *Directory) Drain(ctx context.Context, w sessions.Worker) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	result, err := drainScript.Run(ctx, d.client, []string{d.workerKey(w)}, w.Token).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return sessions.ErrLeaseLost
	}
	return nil
}
func (d *Directory) lease(ctx context.Context, w sessions.Worker, ttl time.Duration, mode string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if w.ID == "" || w.Token == "" || w.URL == "" || w.Capacity <= 0 || ttl < time.Millisecond || (w.State != "" && w.State != sessions.WorkerReady && w.State != sessions.WorkerDraining) {
		return errors.New("invalid worker lease")
	}
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	result, err := leaseScript.Run(ctx, d.client, []string{d.workerKey(w), d.prefix + "workers"}, string(data), ttl.Milliseconds(), mode).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return sessions.ErrLeaseLost
	}
	return nil
}
func (d *Directory) Unregister(ctx context.Context, w sessions.Worker) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	return unregisterScript.Run(ctx, d.client, []string{d.workerKey(w), d.prefix + "workers"}, w.Token, w.ID).Err()
}
func (d *Directory) Reserve(ctx context.Context, clientID, id string, ttl time.Duration, policy limits.Policy) (sessions.Record, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if clientID == "" || id == "" || ttl < time.Millisecond {
		return sessions.Record{}, errors.New("invalid session reservation")
	}
	if err := policy.Validate(); err != nil {
		return sessions.Record{}, err
	}
	data, err := reserveScript.Run(ctx, d.client, []string{d.prefix + "workers", d.sessionKey(id), d.tenantKey(clientID), d.prefix + "rate:session:" + clientID}, d.prefix, clientID, id, ttl.Milliseconds(), policy.MaxConcurrentSessions, policy.MaxSessionTTL.Milliseconds(), policy.SessionRequestsPerMinute).Text()
	if errors.Is(err, goredis.Nil) {
		return sessions.Record{}, sessions.ErrCapacity
	}
	if err != nil {
		return sessions.Record{}, err
	}
	var denied struct {
		Rejection string `json:"rejection"`
		RetryMS   int64  `json:"retry_ms"`
	}
	if err := json.Unmarshal([]byte(data), &denied); err != nil {
		return sessions.Record{}, err
	}
	if denied.Rejection != "" {
		reason := limits.ErrConcurrency
		if denied.Rejection == "rate" {
			reason = limits.ErrRate
		}
		return sessions.Record{}, &limits.Rejection{Reason: reason, RetryAfter: time.Duration(denied.RetryMS) * time.Millisecond}
	}
	var r sessions.Record
	err = json.Unmarshal([]byte(data), &r)
	return r, err
}
func (d *Directory) Lookup(ctx context.Context, clientID, id, state string) (sessions.Record, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	data, err := lookupScript.Run(ctx, d.client, []string{d.sessionKey(id)}, d.prefix, clientID, state).Text()
	if errors.Is(err, goredis.Nil) {
		return sessions.Record{}, sessions.ErrNotFound
	}
	if err != nil {
		return sessions.Record{}, err
	}
	var r sessions.Record
	err = json.Unmarshal([]byte(data), &r)
	return r, err
}
func (d *Directory) Publish(ctx context.Context, r sessions.Record, s sessions.Session) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if s.ID != r.ID || s.ClientID != r.ClientID {
		return sessions.ErrNotFound
	}
	ttl := time.Until(s.ExpiresAt).Milliseconds()
	if ttl <= 0 {
		return sessions.ErrNotFound
	}
	result, err := publishScript.Run(ctx, d.client, []string{d.sessionKey(r.ID), d.prefix + "worker:" + r.WorkerID, d.slotsKey(r.WorkerID, r.WorkerToken), d.tenantKey(r.ClientID)}, r.WorkerToken, r.ClientID, s.CreatedAt.UnixMilli(), s.ExpiresAt.UnixMilli(), ttl).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return sessions.ErrNotFound
	}
	return nil
}
func (d *Directory) Release(ctx context.Context, r sessions.Record) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	return releaseScript.Run(ctx, d.client, []string{d.sessionKey(r.ID), d.slotsKey(r.WorkerID, r.WorkerToken), d.tenantKey(r.ClientID)}, r.WorkerToken, r.ClientID, r.ID).Err()
}

// CheckWorker verifies connectivity and this process's current lease without
// renewing it outside the worker's conservative heartbeat deadline.
func (d *Directory) CheckWorker(ctx context.Context, w sessions.Worker) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	data, err := d.client.Get(ctx, d.workerKey(w)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return sessions.ErrLeaseLost
	}
	if err != nil {
		return err
	}
	var registered sessions.Worker
	if err := json.Unmarshal(data, &registered); err != nil {
		return err
	}
	if registered.Token != w.Token {
		return sessions.ErrLeaseLost
	}
	return nil
}
