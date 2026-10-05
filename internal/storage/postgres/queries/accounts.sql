-- name: CreateUser :exec
INSERT INTO users (id, client_id, name, email, password_hash, created_at)
VALUES (
    sqlc.arg(id), sqlc.arg(client_id), sqlc.arg(name),
    sqlc.arg(email), sqlc.arg(password_hash), sqlc.arg(created_at)
);

-- name: FindUserByEmail :one
SELECT id, client_id, name, email, password_hash, created_at
FROM users
WHERE email = sqlc.arg(email);
