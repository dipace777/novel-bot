# Browser load testing and worker sizing

`cmd/loadtest` uses the same authenticated REST/CDP routes as tenant applications.
At each concurrency level it creates a fresh batch, attaches to each browser,
navigates a page, holds every successful browser open together, periodically
checks CDP responsiveness, and deletes each known session before the next batch.
The default controlled workload renders 5,000 DOM rows. This gives repeatable
local measurements; replace it with a representative application/page to size
real workloads.

## Run locally

Start PostgreSQL/Redis and the development server first:

```bash
make db
make run
```

In another terminal, supply an application API key
issued through the existing login/API-key routes:

```bash
export LOADTEST_API_KEY='YOUR_APPLICATION_API_KEY'

make loadtest LOADTEST_ARGS='--concurrency 1,2,4 --rounds 3 --hold 15s --metrics-urls http://localhost:8090 --worker-memory-mib 2048 --output loadtest-report.json'
```

Use the memory budget allocated to this worker, not the entire host's RAM.
`2048` is an example allocation, not an inferred machine limit. The command reads
credentials from the environment, and never includes them in reports or logs.
Make automatically loads `.env`. When run locally, the command uses `WORKER_AUTH_TOKEN` or derives the same
worker credential as the API. On a separate load-generator host, set
`LOADTEST_WORKER_TOKEN` explicitly instead of provisioning `API_KEY_PEPPER`.

Use `--url` when the public API listens elsewhere; that origin must match the
returned CDP URLs. Use `--target-url https://your-test-page.example` for a different
workload. TLS verification remains enabled. Redirects on REST/metrics requests
are rejected, and credentials are never forwarded to a different CDP origin.
`make build` also produces `bin/loadtest`.

## Isolate the experiment

Use a dedicated test tenant and otherwise idle workers/Redis namespace.
The tool refuses a worker set that already owns browser reservations and rejects
duplicate worker identities. All admission limits remain enabled. Before testing
higher levels, an operator must set the test tenant's concurrent limit high enough,
its maximum lifetime longer than navigation + hold, and its creation rate high
enough for the planned batches. Keep normal tenants' policies intact.

For example, through an operator-only database connection:

```sql
UPDATE clients
SET max_concurrent_sessions = 20,
    max_session_seconds = 300,
    session_requests_per_minute = 120
WHERE id = 'DEDICATED_TEST_CLIENT_ID';
```

Raise `BROWSER_MAX_SESSIONS` on the test worker deliberately. A `429` measures
admission rejection, while `503` can indicate configured worker capacity or startup
failure. They are recorded separately and are not interpreted as evidence that the
machine has exhausted memory. The tool stops increasing concurrency after failed
lifecycles, excessive startup p95, or crossing the memory budget with headroom.

For multiple workers, provide every worker's private origin:

```bash
make loadtest LOADTEST_ARGS='--url http://localhost:8080 --metrics-urls http://localhost:8090,http://localhost:8091 --concurrency 2,4,8 --rounds 3 --hold 20s --worker-memory-mib 2048'
```

All measured workers use the supplied per-worker memory budget. For heterogeneous
workers, run separate experiments with each machine's allocation. The concurrency
levels refer to the total batch, while the report retains each worker's actual
observed session count. The configured API/Redis deployment must contain only the
measured workers so that browser placement matches the metrics set.

## Reports and thresholds

The JSON report contains attempted/created/completed sessions, failures by operation
or HTTP status, API startup p50/p95/p99, navigation p95, fresh memory sample counts,
per-worker peak RSS/process/session counts, qualification failures, and capacity
recommendations. The CLI also prints a compact summary. SIGINT/SIGTERM cancels
work and attempts deletion of known sessions with independent bounded deadlines;
failed/unacknowledged launches still have the server's TTL cleanup as a fallback.

A concurrency level qualifies only when:

- Every attempted browser completes creation, navigation, holding, and deletion.
- API creation p95 is within `--max-startup-p95` (default `2s`).
- Worker metrics are available, the leases are healthy, and each round captures at
  least two fresh memory samples while all target browsers are open.
- Each worker's peak browser + Go-process RSS stays within its supplied memory
  budget after reserving `--headroom` (default `0.3`, minimum `0.1`).

Hold batches long enough for the server's memory interval: with the default `5s`
sampling, use at least `15s`, preferably much longer for sizing. Scraping faster
cannot create new OS samples. Short/incomplete, stale, failed, or missing samples
produce an unqualified report rather than an invented capacity value. Worker
clocks should be synchronized for freshness checks.

The report estimates a memory-based capacity from the worst sampled browser RSS
per observed session and subtracts service RSS and headroom. Its suggested upper
bound is capped at a successfully tested session count. It never extrapolates a
production limit of thousands from a small run. Without metrics or a memory budget,
it reports workload performance but generates no capacity recommendation.

The command exits nonzero on lifecycle/metrics failures or an unqualified stage
when a memory budget was supplied, and still writes the report. A capacity sweep
may therefore exit nonzero after finding a passing lower level and a failing higher
one; inspect both stages and recommendations. Invalid configuration exits `2`.

Measure sustained CPU, container memory/peak/OOM, renderer-heavy pages, slow
networks, session churn, and worker failures before setting production capacity.
Run the generator on a separate host for larger experiments so its own resource
usage does not compete with Chromium. Configure the worker's capacity below the
observed upper bound, then validate it under representative sustained load.

## Automated verification

Unit tests cover RSS parsing, private scrape authentication, histograms, memory
qualification, rejection accounting, cancellation cleanup, and complete simulated
CDP browser lifecycles. The optional real-browser integration test runs only 1, 2,
and 3 concurrent sessions with isolated Redis metadata and profiles:

```bash
make build
TEST_REDIS_URL="$REDIS_URL" \
TEST_CHROMIUM_PATH='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' \
TEST_LOADTEST_BINARY="$PWD/bin/loadtest" \
TEST_LOADTEST_REPORT="$PWD/loadtest-results/local-smoke.json" \
go test -race -v ./internal/httpapi -run '^TestChromiumLoadTestCapacityReport$'
```

Create the report's parent directory first (`mkdir -p loadtest-results`). Generated
reports are ignored by Git. This smoke test verifies the measurement pipeline and
cleanup, not production throughput.
