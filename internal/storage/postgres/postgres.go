package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"novel-bot/internal/auth"
)

func Open(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 0
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnLifetimeJitter = 5 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

var _ auth.Repository = (*Repository)(nil)

func (r *Repository) CreateClient(ctx context.Context, client auth.Client) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO clients (id, name, created_at) VALUES ($1, $2, $3)`, client.ID, client.Name, client.CreatedAt)
	return err
}

func (r *Repository) CreateKey(ctx context.Context, key auth.Key) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO api_keys (id, client_id, name, digest, created_at, expires_at) VALUES ($1, $2, $3, $4, $5, $6)`, key.ID, key.ClientID, key.Name, key.Digest, key.CreatedAt, key.ExpiresAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return auth.ErrNotFound
	}
	return err
}

func (r *Repository) FindKey(ctx context.Context, id string) (auth.Key, error) {
	var key auth.Key
	err := r.pool.QueryRow(ctx, `SELECT id, client_id, name, digest, created_at, expires_at, revoked_at FROM api_keys WHERE id = $1`, id).Scan(&key.ID, &key.ClientID, &key.Name, &key.Digest, &key.CreatedAt, &key.ExpiresAt, &key.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Key{}, auth.ErrNotFound
	}
	return key, err
}

func (r *Repository) ListKeys(ctx context.Context, clientID string) ([]auth.Key, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, client_id, name, created_at, expires_at, revoked_at FROM api_keys WHERE client_id = $1 ORDER BY created_at DESC, id`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]auth.Key, 0)
	for rows.Next() {
		var key auth.Key
		if err := rows.Scan(&key.ID, &key.ClientID, &key.Name, &key.CreatedAt, &key.ExpiresAt, &key.RevokedAt); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (r *Repository) RevokeKey(ctx context.Context, clientID, id string, now time.Time) error {
	result, err := r.pool.Exec(ctx, `UPDATE api_keys SET revoked_at = COALESCE(revoked_at, $3) WHERE id = $1 AND client_id = $2`, id, clientID, now)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return auth.ErrNotFound
	}
	return nil
}
