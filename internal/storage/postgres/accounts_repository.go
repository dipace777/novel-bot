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

var _ auth.AccountRepository = (*Repository)(nil)

func (r *Repository) CreateAccount(ctx context.Context, user auth.User, client auth.Client) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	queries := r.queries.WithTx(tx)
	if err := queries.CreateClient(ctx, clientParams(client)); err != nil {
		return err
	}
	err = queries.CreateUser(ctx, dbgen.CreateUserParams{
		ID: user.ID, ClientID: user.ClientID, Name: user.Name, Email: user.Email,
		PasswordHash: user.PasswordHash, CreatedAt: user.CreatedAt,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key" {
		return auth.ErrConflict
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) FindUserByEmail(ctx context.Context, email string) (auth.User, error) {
	user, err := r.queries.FindUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.User{}, err
	}
	return auth.User{
		ID: user.ID, ClientID: user.ClientID, Name: user.Name, Email: user.Email,
		PasswordHash: user.PasswordHash, CreatedAt: user.CreatedAt,
	}, nil
}

func (r *Repository) CreateSession(ctx context.Context, session auth.Session) error {
	return r.queries.CreateSession(ctx, dbgen.CreateSessionParams{
		ID: session.ID, UserID: session.UserID, Digest: session.Digest,
		CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt,
	})
}

func (r *Repository) FindSession(ctx context.Context, id string) (auth.Session, auth.User, error) {
	row, err := r.queries.FindSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Session{}, auth.User{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Session{}, auth.User{}, err
	}
	session := auth.Session{
		ID: row.SessionID, UserID: row.UserID, Digest: row.Digest,
		CreatedAt: row.SessionCreatedAt, ExpiresAt: row.ExpiresAt, RevokedAt: row.RevokedAt,
	}
	user := auth.User{
		ID: row.UserID, ClientID: row.ClientID, Name: row.Name, Email: row.Email,
		CreatedAt: row.UserCreatedAt,
	}
	return session, user, nil
}

func (r *Repository) RevokeSession(ctx context.Context, userID, id string, now time.Time) error {
	count, err := r.queries.RevokeSession(ctx, dbgen.RevokeSessionParams{ID: id, UserID: userID, RevokedAt: now})
	if err != nil {
		return err
	}
	if count == 0 {
		return auth.ErrNotFound
	}
	return nil
}
