package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"novel-bot/internal/storage/postgres/dbgen"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: dbgen.New(pool)}
}
