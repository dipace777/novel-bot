-- name: CreateAPIKey :exec
INSERT INTO api_keys (id, client_id, name, digest, created_at, expires_at)
VALUES (
    sqlc.arg(id), sqlc.arg(client_id), sqlc.arg(name),
    sqlc.arg(digest), sqlc.arg(created_at), sqlc.arg(expires_at)
);

-- name: FindAPIKey :one
SELECT id, client_id, name, digest, created_at, expires_at, revoked_at
FROM api_keys
WHERE id = sqlc.arg(id);

-- name: ListAPIKeys :many
SELECT id, client_id, name, created_at, expires_at, revoked_at
FROM api_keys
WHERE client_id = sqlc.arg(client_id)
ORDER BY created_at DESC, id;

-- name: RevokeAPIKey :execrows
UPDATE api_keys
SET revoked_at = COALESCE(revoked_at, sqlc.arg(revoked_at)::timestamptz)
WHERE id = sqlc.arg(id) AND client_id = sqlc.arg(client_id);
