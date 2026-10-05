-- Policies apply to existing tenants and new registrations alike.
ALTER TABLE clients
 ADD COLUMN max_concurrent_sessions INTEGER NOT NULL DEFAULT 5 CHECK (max_concurrent_sessions BETWEEN 1 AND 10000),
 ADD COLUMN max_session_seconds INTEGER NOT NULL DEFAULT 900 CHECK (max_session_seconds BETWEEN 1 AND 86400),
 ADD COLUMN session_requests_per_minute INTEGER NOT NULL DEFAULT 30 CHECK (session_requests_per_minute BETWEEN 1 AND 1000000);
