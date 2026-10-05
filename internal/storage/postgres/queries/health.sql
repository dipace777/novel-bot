-- name: CheckAuthSchema :exec
-- Validate connectivity, schema availability, and permissions without fetching rows.
SELECT k.id, u.id, s.id, c.max_concurrent_sessions, c.max_session_seconds, c.session_requests_per_minute
FROM api_keys k, users u, auth_sessions s, clients c
LIMIT 0;
