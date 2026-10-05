-- name: CreateSession :exec
INSERT INTO auth_sessions (id, user_id, digest, created_at, expires_at)
VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(digest),
    sqlc.arg(created_at), sqlc.arg(expires_at)
);

-- name: FindSession :one
SELECT
    s.id AS session_id,
    s.user_id,
    s.digest,
    s.created_at AS session_created_at,
    s.expires_at,
    s.revoked_at,
    u.client_id,
    u.name,
    u.email,
    u.created_at AS user_created_at
FROM auth_sessions s
JOIN users u ON u.id = s.user_id
WHERE s.id = sqlc.arg(id);

-- name: RevokeSession :execrows
UPDATE auth_sessions
SET revoked_at = COALESCE(revoked_at, sqlc.arg(revoked_at)::timestamptz)
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);
