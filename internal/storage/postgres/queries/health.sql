-- name: CheckAuthSchema :exec
-- Validate connectivity, schema availability, and permissions without fetching rows.
SELECT k.id, u.id, s.id
FROM api_keys k, users u, auth_sessions s
LIMIT 0;
