package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"novel-bot/internal/auth"
	"novel-bot/internal/storage/postgres/dbgen"
)

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

func clientParams(client auth.Client) dbgen.CreateClientParams {
	return dbgen.CreateClientParams{ID: client.ID, Name: client.Name, CreatedAt: client.CreatedAt}
}
