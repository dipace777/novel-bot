# Novel Bot browser automation API

A Go REST API foundation for a browser automation platform. Users register and
log in before creating API keys for their applications. Browser sessions,
scheduling, and browser workers are future features.

## Project layout

```text
cmd/
  api/                 HTTP server entry point
  migrate/             Database migration executable
internal/
  auth/                Accounts, login sessions, API keys, repository contracts
  config/              Environment configuration and validation
  httpapi/             REST routes, validation, authentication middleware
  storage/postgres/    Repository adapters and connection pool
    queries/           Handwritten SQL query definitions
    dbgen/             sqlc-generated Go query methods and database models
sqlc.yaml              Query generation configuration
migrations/            Embedded, ordered, checksummed SQL migrations
api/                   OpenAPI contract and bundled Swagger UI
.vscode/settings.json  Go module proxy configuration for the editor
compose.yaml           Local PostgreSQL service on port 6927
.env.example           Configuration template
Makefile               Development and verification commands
```

The HTTP layer calls authentication services, which depend on repository
interfaces implemented by PostgreSQL. `cmd/api` wires them together. Go has no
single mandatory project layout; `cmd` and `internal` keep executables and private
application packages separate. Add session and worker domain packages as those
features are implemented.

## Run locally

Use the Go version in `go.mod` and a running Docker Desktop installation with
Docker Compose, or an existing PostgreSQL database. Setup references:
[Go](https://go.dev/doc/install) and
[Docker Desktop for macOS](https://docs.docker.com/desktop/setup/install/mac-install/).

```sh
go mod download
```

If `.env` does not exist, copy `.env.example` to `.env`, generate a pepper with
`openssl rand -base64 32`, and put it in `API_KEY_PEPPER`. Preserve your existing
pepper: changing it invalidates API keys and login sessions. The application reads
environment variables and does not automatically load `.env`.

```sh
set -a
source .env
set +a

make db
make migrate
make run
```

The API listens on `http://localhost:8080`. PostgreSQL is exposed at
`127.0.0.1:6927`, mapped to container port `5432`. Stop the API with Ctrl+C and
restart it after code changes. To restart the editor's Go tooling, use
**Go: Restart Language Server** from VS Code's Command Palette. Workspace
settings explicitly enable module downloads through the Go module proxy.

## Swagger UI

After starting the API, open [Swagger UI](http://localhost:8080/docs/).
The downloadable OpenAPI 3.0.3 contract is at
[`/openapi.json`](http://localhost:8080/openapi.json), maintained in
[`api/openapi.json`](api/openapi.json).

The UI provides examples, request/response schemas, error codes, filtering, and
**Try it out** for every implemented API operation. Register and log in through
the public routes, then click **Authorize** and paste `access_token` into
**LoginSession** to generate and manage keys. Paste an issued `api_key` into
**ApplicationBearer** or **ApplicationKeyHeader** to test `/v1/whoami`. Enter
raw token values without adding `Bearer`.

Authorization is kept only in page memory and is cleared on reload. If both
application key methods are authorized, UI requests prefer Bearer and omit the
duplicate `X-API-Key` header. All UI assets are bundled into the server binary;
there is no CDN or online-validator dependency at runtime. The API server address
comes from the current browser origin, so custom HTTP ports work too.

When adding or changing an endpoint, update `api/openapi.json` alongside its Go
handler and rebuild/restart the server. Swagger assets are pinned to 5.33.1;
their source, package integrity, and license notices are in `api/swagger-ui/`.

## Authentication and API key flow

### 1. Register

```sh
curl -X POST http://localhost:8080/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"My App","email":"owner@example.com","password":"choose-a-long-unique-password"}'
```

Returns `201` with `{"user": ...}`. Registration atomically creates an account
and its own application client. Emails are trimmed and normalized to lowercase.
Passwords must contain 12 to 72 bytes and names 1 to 100 characters. Duplicate
emails return `409`. Registration does not issue an API key or log the user in.

### 2. Log in

```sh
curl -X POST http://localhost:8080/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"owner@example.com","password":"choose-a-long-unique-password"}'
```

Returns `200` with `access_token`, `token_type` (`Bearer`), `expires_at`, and
`user` metadata. The opaque token starts with `nb_session_` and expires after
24 hours by default. Wrong passwords and unknown emails receive the same `401`
response. Copy `access_token` into the following requests as `LOGIN_TOKEN`.

### 3. Generate an API key

```sh
curl -X POST http://localhost:8080/v1/api-keys \
  -H 'Authorization: Bearer LOGIN_TOKEN' \
  -H 'Content-Type: application/json' \
  -d '{"name":"production","ttl_seconds":7776000}'
```

Returns `201` with `{"key": ..., "api_key": "nb_key_..."}`. Save `api_key`
securely; it is shown only on issuance. `ttl_seconds` is optional, defaults to
90 days, and must be between 1 and 7776000. Client ownership comes from the login
session; requests cannot choose a different client ID.

### 4. Use the API key from your application

```sh
curl http://localhost:8080/v1/whoami \
  -H 'Authorization: Bearer YOUR_API_KEY'
```

Returns `{"client_id":"CLIENT_ID","key_id":"KEY_ID"}`. Application routes also
accept `X-API-Key: YOUR_API_KEY`. Use exactly one credential header.

### 5. Manage keys and log out

```sh
curl http://localhost:8080/v1/api-keys \
  -H 'Authorization: Bearer LOGIN_TOKEN'

curl -X DELETE http://localhost:8080/v1/api-keys/KEY_ID \
  -H 'Authorization: Bearer LOGIN_TOKEN'

curl -X POST http://localhost:8080/v1/auth/logout \
  -H 'Authorization: Bearer LOGIN_TOKEN'
```

Listing returns metadata only in `{"keys": [...]}`. Deleting revokes the key and
returns `204`; repeating it for an owned key is idempotent. Unknown or other
accounts' keys return `404`. Logout revokes the current login session and returns
`204`; subsequent use of that session returns `401`. Other login sessions and API
keys remain active. Rotate keys by issuing a replacement, switching the consumer,
and revoking the old key.

## Routes

| Method | Route | Authentication |
| --- | --- | --- |
| POST | `/v1/auth/register` | Public |
| POST | `/v1/auth/login` | Public |
| GET | `/v1/auth/me` | Login token via Bearer |
| POST | `/v1/auth/logout` | Login token via Bearer |
| POST | `/v1/api-keys` | Login token via Bearer |
| GET | `/v1/api-keys` | Login token via Bearer |
| DELETE | `/v1/api-keys/{id}` | Login token via Bearer |
| GET | `/v1/whoami` | Application API key |
| GET | `/healthz` | Public liveness |
| GET | `/readyz` | Public database/schema readiness |

API keys cannot manage keys or access account routes. Login tokens cannot access
application routes. Each account owns one client; future team/project membership
can extend this model.

JSON requests require `Content-Type: application/json`. Unknown fields, invalid
JSON, and additional JSON values are rejected. Bodies are limited to 16 KiB.
Errors include `error.code`, `error.message`, and `request_id`. Responses carry
`X-Request-ID` and `Cache-Control: no-store`. Database failures and timeouts return
`503`. Revocation affects subsequent lookups; already authenticated requests may
finish.

## Configuration and security

| Variable | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | Required | PostgreSQL connection string |
| `API_KEY_PEPPER` | Required | Base64 random secret, at least 32 decoded bytes |
| `HTTP_ADDR` | `:8080` | HTTP listen address |
| `DB_MAX_CONNS` | `20` | Maximum database connections per process |
| `AUTH_TIMEOUT` | `2s` | Deadline for auth lookups, account operations, readiness |
| `SESSION_TTL` | `24h` | Lifetime of each login session |

Passwords use Go's [bcrypt implementation](https://pkg.go.dev/golang.org/x/crypto/bcrypt)
at its default cost of 10. Hashing/checking is bounded to four concurrent operations
per process. Unknown accounts use a dummy hash on login. Password hashes are
excluded from user response metadata.

API keys and login tokens use separate prefixes, a random 128-bit lookup ID, and
a random 256-bit secret. PostgreSQL stores HMAC-SHA256 token digests; verification
compares digests in constant time. All replicas share the same pepper, kept in a
deployment secret manager. `.env` is ignored by Git. Tokens, passwords, and request
bodies are not included in application logs.

Use HTTPS at ingress and TLS for production PostgreSQL connections. Compose's
credentials and `sslmode=disable` URL are development settings. The API database
role needs read/write access for account/session/key operations; migrations should
use a separate role with schema permissions. Configure distributed ingress rate
limits for public registration/login before deployment. Email verification,
password reset, MFA, session refresh, and automated expired session cleanup are
future features.

Existing clients and API keys are preserved by the account migration. They have
no associated user account; registration creates a new client and does not claim
existing IDs. Backfill ownership explicitly if legacy clients need management
through account routes.

## Scaling direction

API replicas keep sessions and keys in PostgreSQL. Authentication uses indexed
lookups and a bounded [pgx pool](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool).
Reads do not update last-used timestamps. Budget connections as
`API replicas × DB_MAX_CONNS`, plus migrations and maintenance.

Thousands of browser sessions require a separate execution layer: a queue,
resource-limited workers, tenant authorization, leases, quotas, observability, and
workload admission. Long-lived browser/WebSocket traffic needs different transport
timeouts. This project has not been load-tested for thousands of browser sessions.

## SQL query workflow

The PostgreSQL repositories use [sqlc](https://github.com/sqlc-dev/sqlc) with
`pgx/v5`. Application SQL lives in `internal/storage/postgres/queries/`; generated
Go methods and models live in `internal/storage/postgres/dbgen/`. The repositories
map generated models into authentication domain types, keeping database details
and sensitive fields out of HTTP responses. Registration binds generated queries
to its transaction with `WithTx` so client/user creation remains atomic.

```sh
make tools       # Install pinned sqlc v1.31.1 into ./bin if needed
make generate    # Generate Go from migrations and named SQL queries
make sqlc-check  # Compile queries and detect stale generated code
```

Add or edit a named query in `queries/*.sql`, then run `make generate` and tests.
For schema changes, add a new numbered `migrations/*.up.sql` file first; sqlc uses
these same migration files as its schema input, excluding down migrations. It does
not apply schema changes: use `make migrate` separately. SQL generation needs no
running database and does not use a cloud service.

Commit generated Go files with the query changes. Do not edit `dbgen/*.go` by hand.
Normal builds and tests use those files directly, so sqlc is a development tool
and adds no runtime dependency. Migration bootstrap SQL and integration-test
fixture SQL remain in their respective migration/test code.

## Tests and migrations

```sh
make test
make vet
make build

# Load .env first and keep PostgreSQL running:
TEST_DATABASE_URL="$DATABASE_URL" make test
```

Tests cover registration, password hashing, login errors, session expiry/logout,
token tampering, API key lifecycle, token type separation, request validation, and
tenant ownership. The concurrency test covers 1,000 authentication calls against
a test repository, verifying race safety rather than browser capacity.

Without `TEST_DATABASE_URL`, PostgreSQL integration tests are explicitly skipped.
They create and remove an isolated schema and exercise the full HTTP account and
API key lifecycle, persistence, transactional signup rollback, tenant isolation,
and migration checksums. The test role needs schema creation permission.

`make migrate` runs `cmd/migrate`, applying embedded `*.up.sql` files in filename
order under a transaction and PostgreSQL advisory lock. Applied files are recorded
with checksums; changes to them are rejected. Add new numbered files instead.
Run migrations before API replicas start. Down SQL files are manual rollback
references; coordinate the migration ledger when rolling back.
