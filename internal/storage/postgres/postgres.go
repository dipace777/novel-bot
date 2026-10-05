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
	"novel-bot/internal/storage/postgres/dbgen"
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

type Repository struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: dbgen.New(pool)}
}

var _ auth.Repository = (*Repository)(nil)

func (r *Repository) CreateClient(ctx context.Context, client auth.Client) error {
	return r.queries.CreateClient(ctx, clientParams(client))
}

func (r *Repository) CreateKey(ctx context.Context, key auth.Key) error {
	err := r.queries.CreateAPIKey(ctx, dbgen.CreateAPIKeyParams{
		ID: key.ID, ClientID: key.ClientID, Name: key.Name, Digest: key.Digest,
		CreatedAt: key.CreatedAt, ExpiresAt: key.ExpiresAt,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return auth.ErrNotFound
	}
	return err
}

func (r *Repository) FindKey(ctx context.Context, id string) (auth.Key, error) {
	key, err := r.queries.FindAPIKey(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Key{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Key{}, err
	}
	return auth.Key{
		ID: key.ID, ClientID: key.ClientID, Name: key.Name, Digest: key.Digest,
		CreatedAt: key.CreatedAt, ExpiresAt: key.ExpiresAt, RevokedAt: key.RevokedAt,
	}, nil
}

func (r *Repository) ListKeys(ctx context.Context, clientID string) ([]auth.Key, error) {
	rows, err := r.queries.ListAPIKeys(ctx, clientID)
	if err != nil {
		return nil, err
	}
	keys := make([]auth.Key, 0, len(rows))
	for _, key := range rows {
		keys = append(keys, auth.Key{
			ID: key.ID, ClientID: key.ClientID, Name: key.Name,
			CreatedAt: key.CreatedAt, ExpiresAt: key.ExpiresAt, RevokedAt: key.RevokedAt,
		})
	}
	return keys, nil
}

func (r *Repository) RevokeKey(ctx context.Context, clientID, id string, now time.Time) error {
	count, err := r.queries.RevokeAPIKey(ctx, dbgen.RevokeAPIKeyParams{ID: id, ClientID: clientID, RevokedAt: now})
	if err != nil {
		return err
	}
	if count == 0 {
		return auth.ErrNotFound
	}
	return nil
}

// Ready checks connectivity, schema availability, and required read permissions.
func (r *Repository) Ready(ctx context.Context) error {
	return r.queries.CheckAuthSchema(ctx)
}

func clientParams(client auth.Client) dbgen.CreateClientParams {
	return dbgen.CreateClientParams{ID: client.ID, Name: client.Name, CreatedAt: client.CreatedAt}
}
