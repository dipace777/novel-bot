-- Existing clients and API keys are preserved. New accounts own a new client.
CREATE TABLE users (
    id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    client_id TEXT NOT NULL UNIQUE REFERENCES clients(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    email TEXT NOT NULL UNIQUE CHECK (char_length(email) BETWEEN 3 AND 254 AND email = lower(email)),
    password_hash BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE auth_sessions (
    id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    digest BYTEA NOT NULL CHECK (octet_length(digest) = 32),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > created_at),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX auth_sessions_user_id_idx ON auth_sessions(user_id);
CREATE INDEX auth_sessions_expires_at_idx ON auth_sessions(expires_at);
