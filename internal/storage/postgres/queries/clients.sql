-- name: CreateClient :exec
INSERT INTO clients (id, name, created_at)
VALUES (sqlc.arg(id), sqlc.arg(name), sqlc.arg(created_at));
