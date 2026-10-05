# Independent API and browser workers

`cmd/api` serves REST authentication, admission, Swagger, and public CDP routing.
It connects to PostgreSQL and Redis, and sends authenticated private HTTP requests
to the worker selected by Redis. It does not discover or launch Chromium, register
a worker, sample browser memory, or bind a worker port.

`cmd/worker` owns Chromium processes/profiles, private CDP forwarding, Redis
registration/heartbeats, cleanup, and Prometheus metrics. It connects only to
Redis. It does not require PostgreSQL credentials or `API_KEY_PEPPER`.
`cmd/migrate` needs only `DATABASE_URL` and optional `DB_MAX_CONNS`.

## Local development

Set `API_KEY_PEPPER` and `WORKER_AUTH_TOKEN` in `.env`. Preserve the pepper used
by existing accounts/keys. Generate the separate worker credential once with
`openssl rand -hex 32`; share it among API processes and workers. The credential
must have at least 32 characters. Workers must not receive the API-key pepper in
production. Make loads `.env` automatically for development; production binaries
read their role's environment directly.

```sh
make db
make migrate

# Terminal A: public API
make run-api                 # make run remains an alias

# Terminal B: first browser worker
WORKER_ID=worker-a make run-worker

# Terminal C: optional second worker
WORKER_ID=worker-b WORKER_HTTP_ADDR=127.0.0.1:8091 \
  WORKER_URL=http://127.0.0.1:8091 make run-worker

# Terminal D: optional second API replica; no extra worker is started
HTTP_ADDR=:8081 make run-api
```

The API's `SESSION_STARTUP_TIMEOUT` controls reservation and worker-RPC deadlines.
The worker's `BROWSER_STARTUP_TIMEOUT` controls Chromium launch. Set the API budget
at least as high as the largest worker launch timeout. Both default to `10s`.
Worker `BROWSER_SESSION_TTL` and each tenant's policy continue to cap lifetime.
Workers must advertise a unique reachable `WORKER_URL`; `127.0.0.1` works only
when the API and worker run on the same host.

## Role-specific readiness

| Process | Endpoint | Check | Authentication |
| --- | --- | --- | --- |
| API | `/healthz` | Process HTTP liveness | None |
| API | `/readyz` | PostgreSQL auth schema and Redis connectivity | None |
| Worker | `/healthz` | Private HTTP liveness | None |
| Worker | `/readyz` | Conservative local lease deadline and current Redis incarnation | Bearer worker credential |
| Worker | `/metrics` | Prometheus scrape, independent of readiness | Bearer worker credential |

An API with no workers, or with all browser slots reserved, stays ready and returns
`503 session_capacity_reached` from session creation. Authentication and docs
remain available. A worker that loses its lease stops its browsers and exits
nonzero; its supervisor can restart it with a new incarnation. Existing sessions
are ephemeral and are not recreated after worker loss.

Stopping an API closes its REST/CDP connections while leaving worker browsers
alive until deletion, TTL, or worker failure. Clients can reconnect through a
healthy API using the same session ID. Stopping a worker retires its registration,
kills its browsers, releases reservations, and closes the related CDP streams.
For abrupt worker death, Redis expiry reconciles capacity and the container
runtime must kill any remaining Chromium children.

## Docker Compose

The `app` profile adds one API, an automatic migration job, and two independently
supervised workers to the PostgreSQL/Redis services. `make db` still starts only
infrastructure. Use:

```sh
make up
# API: http://localhost:8080; docs: http://localhost:8080/docs/
# Worker A metrics/readiness: http://localhost:8090
# Worker B metrics/readiness: http://localhost:8091

docker compose --profile app ps
# Shut down containers without deleting database volumes:
make down
```

Compose passes database/pepper settings only to the API, database settings only
to migrations, and browser settings only to workers. The migration job must
finish before the API starts; workers depend only on healthy Redis. Workers
advertise `http://worker-a:8090` and `http://worker-b:8090` inside the Compose
network. Published worker ports are loopback-only for local monitoring; avoid
publishing them in production. Each role has a separate image target; the API
image contains no Chromium.

The Compose Redis namespace defaults to `novelbot-compose`, separate from local
Go processes using `novelbot`. Accounts/keys use the same development PostgreSQL
database. Do not run local servers on the same published ports as Compose.
Overrides for an isolated deployment include `COMPOSE_API_PORT`,
`COMPOSE_WORKER_A_PORT`, `COMPOSE_WORKER_B_PORT`, `COMPOSE_POSTGRES_PORT`,
`COMPOSE_REDIS_PORT`, and `COMPOSE_REDIS_NAMESPACE`.

Workers default to six browser slots, 4 CPUs, 4 GiB memory, 2,048 tasks, and
512 MiB shared memory, with swap disabled. The API has a separate 1 CPU, 512 MiB,
128-task limit. These defaults come from the controlled local workload baseline
in [worker sizing](capacity.md). Adjust the `COMPOSE_WORKER_*` and `COMPOSE_API_*`
settings after remeasuring on your deployment hardware. Budget node resources
for all co-located workers, the API, infrastructure, and the operating system.
The worker runs as a non-root user with a Chromium-compatible seccomp profile and
its browser sandbox enabled; the Docker host must support user namespaces. See
[container sandbox notes](../deploy/README.md).

## Verify routing and worker failure

Create an application key through Swagger or the existing account/API-key routes.
With `LOADTEST_API_KEY` set, a small routing/workload check is:

```sh
make loadtest LOADTEST_ARGS='--concurrency 2 --rounds 1 --hold 15s --metrics-urls http://localhost:8090,http://localhost:8091'
```

The report should show sessions on both workers. For a failure drill on a dedicated
development deployment, keep sessions open and stop one worker:

```sh
# Graceful failure/maintenance: terminates this worker's browsers immediately.
docker compose --profile app stop worker-a

# API remains ready; worker B continues serving and accepting sessions.
curl -f http://localhost:8080/readyz

# Restore worker A with a new incarnation; old sessions remain unavailable.
docker compose --profile app up -d --wait worker-a
```

For an abrupt-death drill, `docker compose --profile app kill -s SIGKILL worker-a`
kills the worker container and its Chromium processes. Reservations on that worker
remain unavailable until the lease expires (default `15s`); then new admission
reconciles quotas. Restart it with the `up` command above. Existing CDP sessions
on worker B must remain usable. API restarts close public WebSockets but do not
stop worker browsers, so reconnect after the restart.

The optional process integration test uses built API/worker binaries, two real
Chromium workers, an isolated PostgreSQL schema and Redis namespace. It checks API
readiness without workers, creation/CDP routing across workers, API restart and
reconnection, worker lease loss, and surviving-worker capacity/cleanup:

```sh
make build
# Export TEST_DATABASE_URL, TEST_REDIS_URL, and TEST_CHROMIUM_PATH first.
TEST_API_BINARY="$PWD/bin/api" TEST_WORKER_BINARY="$PWD/bin/worker" \
  go test -race -v ./internal/httpapi -run '^TestIndependentAPIAndWorkerProcesses$'
```
