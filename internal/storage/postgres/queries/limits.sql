-- name: GetTenantPolicy :one
SELECT max_concurrent_sessions, max_session_seconds, session_requests_per_minute
FROM clients WHERE id = sqlc.arg(client_id);

-- name: UpdateTenantPolicy :execrows
UPDATE clients
SET max_concurrent_sessions = sqlc.arg(max_concurrent_sessions),
    max_session_seconds = sqlc.arg(max_session_seconds),
    session_requests_per_minute = sqlc.arg(session_requests_per_minute)
WHERE id = sqlc.arg(client_id);
