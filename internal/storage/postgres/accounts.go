package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"novel-bot/internal/auth"
)

var _ auth.AccountRepository = (*Repository)(nil)

func (r *Repository) CreateAccount(ctx context.Context, user auth.User, client auth.Client) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `INSERT INTO clients (id, name, created_at) VALUES ($1, $2, $3)`, client.ID, client.Name, client.CreatedAt); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO users (id, client_id, name, email, password_hash, created_at) VALUES ($1, $2, $3, $4, $5, $6)`, user.ID, user.ClientID, user.Name, user.Email, user.PasswordHash, user.CreatedAt)
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
	var user auth.User
	err := r.pool.QueryRow(ctx, `SELECT id, client_id, name, email, password_hash, created_at FROM users WHERE email = $1`, email).Scan(&user.ID, &user.ClientID, &user.Name, &user.Email, &user.PasswordHash, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	return user, err
}

func (r *Repository) CreateSession(ctx context.Context, session auth.Session) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, digest, created_at, expires_at) VALUES ($1, $2, $3, $4, $5)`, session.ID, session.UserID, session.Digest, session.CreatedAt, session.ExpiresAt)
	return err
}

func (r *Repository) FindSession(ctx context.Context, id string) (auth.Session, auth.User, error) {
	var session auth.Session
	var user auth.User
	err := r.pool.QueryRow(ctx, `SELECT s.id, s.user_id, s.digest, s.created_at, s.expires_at, s.revoked_at, u.id, u.client_id, u.name, u.email, u.created_at FROM auth_sessions s JOIN users u ON u.id = s.user_id WHERE s.id = $1`, id).Scan(&session.ID, &session.UserID, &session.Digest, &session.CreatedAt, &session.ExpiresAt, &session.RevokedAt, &user.ID, &user.ClientID, &user.Name, &user.Email, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Session{}, auth.User{}, auth.ErrNotFound
	}
	return session, user, err
}

func (r *Repository) RevokeSession(ctx context.Context, userID, id string, now time.Time) error {
	result, err := r.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = COALESCE(revoked_at, $3) WHERE id = $1 AND user_id = $2`, id, userID, now)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return auth.ErrNotFound
	}
	return nil
}
