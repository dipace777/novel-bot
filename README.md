# Novel Bot browser automation API

A Go REST API foundation for a browser automation platform. Users register and
log in before creating API keys for their applications. API keys can launch
isolated headless Chromium sessions and connect through a CDP WebSocket proxy.
Distributed scheduling and browser workers are future features.

## Project layout

```text
cmd/
  api/                 HTTP server entry point
  migrate/             Database migration executable
internal/
  auth/
    accounts.go        Registration, login, and login sessions
    api_keys.go        API key issuance and verification
    credentials.go     Shared credential parsing and identity validation
    repositories.go    Storage contracts used by authentication services
    errors.go          Shared domain errors
  config/
    config.go          Environment configuration and validation
  httpapi/
    router.go          Route wiring and HTTP method guards
    accounts.go        Account HTTP handlers and service contract
    api_keys.go        API key HTTP handlers and service contract
    middleware.go      Authentication, timeouts, request IDs, panic recovery
    responses.go       JSON parsing, responses, and error mapping
    docs.go            OpenAPI and Swagger UI routes
    sessions.go        Browser creation, termination, and public connection URLs
    cdp_proxy.go       Authenticated WebSocket proxy to worker-local Chromium
  sessions/            Browser lifecycle, tenant ownership, capacity, and expiry
  browser/             Chromium launch, isolated profiles, and process cleanup
  storage/postgres/
    pool.go                    Connection configuration
    repository.go              Repository construction and shared query handle
    accounts_repository.go     Account and login-session persistence
    api_keys_repository.go     Client and API-key persistence
    readiness.go               Authentication schema readiness check
    queries/                   Handwritten SQL query definitions
    dbgen/                     sqlc-generated query methods and database models
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
application packages separate. Tests live beside their packages, with HTTP tests
grouped by routes, accounts, API keys, middleware, and documentation. Shared HTTP
test fixtures live in `test_helpers_test.go`.

Keep authentication rules in `auth`, HTTP concerns in `httpapi`, and database
mapping in `storage/postgres`. Browser lifecycle lives in `sessions`, process
launching in `browser`, and CDP transport in `httpapi`. Add distributed execution
under `internal/workers/` when implemented. The SQL files in
`queries/` are source code; `dbgen/` is generated output and must not be edited by
hand.

## Run locally

Use the Go version in `go.mod`, Chromium or Google Chrome, and a running Docker
Desktop installation with Docker Compose, or an existing PostgreSQL database. Set
`CHROMIUM_PATH` if the browser is not on PATH or installed in `/Applications`.
Setup references:
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
| POST | `/sessions` | Application API key |
| GET | `/sessions/{id}` | Application API key; WebSocket upgrade |
| DELETE | `/sessions/{id}` | Application API key |
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
finish. API key revocation blocks new CDP connections; an already upgraded
connection runs until disconnect, browser deletion, expiry, or worker shutdown.

## Browser sessions

Create a browser using the API key issued to your application:

```sh
curl -X POST http://localhost:8080/sessions \
  -H 'Authorization: Bearer YOUR_API_KEY' \
  -H 'Content-Type: application/json' \
  -d '{}'
```

Returns `201` with `id`, `created_at`, `expires_at`, and `cdp_url`, for example
`ws://localhost:8080/sessions/SESSION_ID`. The debugging port is chosen by
Chromium and bound to loopback. Every browser uses a separate temporary profile.
Sessions outlive their creation requests. No browser flags or destination URLs
are accepted in the creation body.

Connect from Playwright using the returned URL and your application API key:

```js
import { chromium } from "playwright";

const browser = await chromium.connectOverCDP(session.cdp_url, {
  headers: { Authorization: `Bearer ${process.env.NOVEL_BOT_API_KEY}` },
});
const context = browser.contexts()[0];
const page = await context.newPage();
await page.goto("https://example.com");
```

CDP WebSocket handshakes accept Bearer or `X-API-Key`, with the same tenant
ownership checks as deletion. Credentials are consumed by the API and removed
before forwarding to Chromium. Swagger documents the WebSocket handshake but
its Try it out button does not open a CDP connection.

Delete a session when finished:

```sh
curl -X DELETE http://localhost:8080/sessions/SESSION_ID \
  -H 'Authorization: Bearer YOUR_API_KEY'
```

Deletion returns `204` after process termination and profile cleanup. Missing or
other tenants' sessions return `404`. Disconnection alone leaves the session
running until deletion or expiry. Sessions expire after 15 minutes by default;
expiry, browser exit, and graceful server shutdown remove their entries and close
CDP connections. On Unix, termination includes the browser's child process group.
Creation reserves capacity before launch; a full worker returns `503` with
`session_capacity_reached` and `Retry-After`. Startup timeouts return `504`.

## Configuration and security

| Variable | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | Required | PostgreSQL connection string |
| `API_KEY_PEPPER` | Required | Base64 random secret, at least 32 decoded bytes |
| `HTTP_ADDR` | `:8080` | HTTP listen address |
| `DB_MAX_CONNS` | `20` | Maximum database connections per process |
| `AUTH_TIMEOUT` | `2s` | Deadline for auth lookups, account operations, readiness |
| `SESSION_TTL` | `24h` | Lifetime of each login session |
| `CHROMIUM_PATH` | Auto-discovery | Chromium/Chrome executable |
| `BROWSER_PROFILE_DIR` | OS temp directory | Existing parent directory for isolated profiles |
| `BROWSER_MAX_SESSIONS` | `10` | Local capacity, including pending launches |
| `BROWSER_SESSION_TTL` | `15m` | Browser lifetime; positive duration up to 24h |
| `BROWSER_STARTUP_TIMEOUT` | `10s` | Launch deadline; positive duration up to 24h |
| `PUBLIC_API_URL` | Direct request origin | HTTP(S) origin used for returned CDP URLs |

Set `PUBLIC_API_URL=https://browsers.example.com` behind a TLS reverse proxy so
returned connection URLs use `wss`. Forwarded headers are not trusted to construct
URLs. The ingress must support WebSocket upgrades and suitable connection
timeouts. Chromium's sandbox remains enabled; use an environment that supports it.

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

API replicas keep login sessions and keys in PostgreSQL. Authentication uses indexed
lookups and a bounded [pgx pool](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool).
Reads do not update last-used timestamps. Budget connections as
`API replicas × DB_MAX_CONNS`, plus migrations and maintenance.

Browser sessions currently belong to one Go process and are held in its protected
in-memory map. `sessions.Launcher` separates launch mechanics from lifecycle;
the HTTP `SessionManager` interface allows a later worker-routing implementation.
Sessions are ephemeral and do not survive worker restarts.

Horizontal scaling will need a Redis directory containing session ownership,
worker identity, expiry, and leases; worker selection based on available capacity;
and routing CDP/deletion requests to the owning worker. Redis does not move or
share Chromium processes. Until that exists, use one worker or route each session
to its original worker, with `PUBLIC_API_URL` pointing to a suitable reachable
origin. Thousands of sessions will also need process/container resource limits,
quotas, workload admission, and observability. This implementation has not been
load-tested for thousands of browser sessions.

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

# Opt-in real Chromium launch + authenticated proxy test:
TEST_CHROMIUM_PATH="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" make test
```

Tests cover registration, password hashing, login errors, session expiry/logout,
token tampering, API key lifecycle, token type separation, request validation, and
tenant ownership. The concurrency test covers 1,000 authentication calls against
a test repository, verifying race safety rather than browser capacity.

Browser tests cover reserved capacity during concurrent launches, tenant
isolation, request cancellation, startup failure cleanup, expiry, process exit,
shutdown, and bidirectional WebSocket proxying beyond REST deadlines. Normal
tests use a child-process helper and a local WebSocket backend. The real-browser
test is explicitly skipped without `TEST_CHROMIUM_PATH`.

Without `TEST_DATABASE_URL`, PostgreSQL integration tests are explicitly skipped.
They create and remove an isolated schema and exercise the full HTTP account and
API key lifecycle, persistence, transactional signup rollback, tenant isolation,
and migration checksums. The test role needs schema creation permission.

`make migrate` runs `cmd/migrate`, applying embedded `*.up.sql` files in filename
order under a transaction and PostgreSQL advisory lock. Applied files are recorded
with checksums; changes to them are rejected. Add new numbered files instead.
Run migrations before API replicas start. Down SQL files are manual rollback
references; coordinate the migration ledger when rolling back.
