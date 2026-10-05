CREATE TABLE clients (
    id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
    id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    client_id TEXT NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    digest BYTEA NOT NULL CHECK (octet_length(digest) = 32),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > created_at),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX api_keys_client_id_idx ON api_keys (client_id, created_at DESC);
