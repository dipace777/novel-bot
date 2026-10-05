// Package migrations embeds ordered SQL migrations in the migration executable.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.up.sql
var files embed.FS

// Apply serializes migration runners and applies pending migrations atomically.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(726193004); CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(files, "*.up.sql")
	if err != nil {
		return err
	}
	for _, name := range names {
		sql, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(sql)
		checksum := hex.EncodeToString(sum[:])
		var stored string
		err = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name = $1`, name).Scan(&stored)
		if err == nil {
			if stored != checksum {
				return fmt.Errorf("migration %s has changed since it was applied", name)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)`, name, checksum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
