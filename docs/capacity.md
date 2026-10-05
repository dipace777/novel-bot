# Worker capacity measurement

Run a reproducible mixed scraping experiment before selecting browser slots. The
baseline budget is one worker with **4 GiB RAM and 4 CPUs**. The generator runs
on the host; the API, PostgreSQL, Redis, and fixture server run in separate
containers. Capacity measurements apply to the tested architecture, browser,
workloads, CPU quota, and memory limit.

## Measured baseline: 6 October 2026

The checked-in [benchmark evidence](benchmarks/mixed-scraping-4g-4cpu.json)
records **225 successful browser lifecycles** on a Linux ARM64 Docker Desktop
worker using Chromium 154.0.8037.92. The Docker VM had 10 CPUs and 7.65 GiB RAM;
the tested worker had an enforced 4 CPU / 4 GiB allocation. All sweep levels
through eight concurrent sessions passed. Eight is the highest level tested,
not a measured physical maximum.

The selected operating limit is **six concurrent starting/active sessions per
worker**, leaving 25% placement headroom below that tested level. Verification
at six covered each profile, three 60-second mixed batches, and 15 churn batches.
The following figures are per stage; CPU is steady utilization as a percentage
of the four-CPU quota, and memory is the kernel container peak.

| Check | Concurrent sessions | Completed lifecycles | Startup p95 | Peak memory | CPU p95 | Throttled periods p95 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Mixed sweep, highest level | 8 | 24 | 607 ms | 1.84 GiB | 19.0% | 0% |
| Article verification | 6 | 18 | 412 ms | 1.24 GiB | 13.7% | 0% |
| Feed verification | 6 | 18 | 487 ms | 1.45 GiB | 11.9% | 0% |
| JavaScript dashboard verification | 6 | 18 | 456 ms | 1.46 GiB | 15.9% | 10% |
| Mixed soak | 6 | 18 | 456 ms | 1.41 GiB | 13.8% | 0% |
| Mixed churn | 6 | 90 | 342 ms | 1.34 GiB | 23.1% | 20% |

Lower sweep levels account for the other 39 completed lifecycles. At six, the
worst navigation/initial extraction p95 was 1.29 seconds and ongoing extraction
p95 was 53 ms. No phase recorded swap, OOM, memory-limit, or task-limit events.
Startup bursts reached the CPU quota; the churn throttling p95 reached the
qualification threshold. Keep that startup headroom when sizing for bursts.

Worker task count peaked at 838 at six sessions. The selected limit is 2,048
tasks (Linux includes browser threads), with 512 MiB shared memory included
within the 4 GiB memory limit and swap disabled. The API peaked at 21.94 MiB
under this browser traffic; its deployment baseline is 1 CPU / 512 MiB / 128
tasks. Both worker services and the API use these defaults in `compose.yaml`.
Native workers also default to six slots, but native execution does not impose
the container resource limits.

```sh
make up                         # Apply the measured Compose limits
make capacity                   # Repeat the isolated experiment
```

Two default workers need 8 GiB for their worker allocations alone, plus the API,
database, Redis, and host overhead. Allocate enough Docker VM or node resources
before running both. This local controlled result is an initial baseline;
repeat on the target production hardware with customer pages and longer runs
before committing to production capacity.

## Run the experiment

Requirements: Docker Compose with cgroup v2, Go from `go.mod`, and Python 3.9+.

```sh
make capacity
```

The command creates a randomly named Compose project, temporary authentication
secrets, a new database/Redis namespace, and a dedicated benchmark tenant. It
selects unused loopback ports, raises limits only for that new tenant, and removes
its containers and volumes in a `finally` block. It does not load `.env`, change
existing tenant policies, or stop the normal deployment. Results remain in
`loadtest-results/capacity/`; no credentials or page URLs appear in the reports.

The experiment deliberately runs only one worker so concurrency is the observed
per-worker browser count. Sweep levels default to `1,2,4,6,8`; each runs three
fresh browser batches with a 20-second hold. Every known browser is deleted before
the next batch. This phase finds a passing tested upper bound rather than
extrapolating from one browser's memory.

The provisional operating capacity is 75% of that passing upper bound, rounded
down (minimum one). At that concurrency the command verifies each workload
separately, performs three 60-second mixed batches, then 15 mixed churn batches
with five-second holds. If any phase fails, it lowers capacity and repeats the
verification. Before the soak, it sets a task limit to the next power of two above
twice the measured verification task peak, with a minimum of 128, and retests
under that limit. Linux cgroup task counts include threads as well as processes.

Reports include `mixed-sweep.json`, workload checks, soak/churn reports,
`summary.json`, and a suggested `capacity.env`. A sweep can return a failed higher
stage while lower stages pass; the orchestrator checks the complete report and
uses only qualified stages. Missing resource evidence cannot produce a capacity.
The emitted environment file contains only resource settings; applying it to an
existing deployment remains an operator action.

For different worker budgets or a longer soak:

```sh
make capacity CAPACITY_ARGS='--cpus 4 --memory-mib 4096 --levels 1,2,4,6,8 --soak-rounds 10 --soak-hold 120s --output loadtest-results/my-worker'
```

Default Docker image builds use the generated project name. For repeated local
runs, build reusable images and use `--no-build`:

```sh
docker compose -f compose.yaml -f compose.capacity.yaml --profile app --profile benchmark build api migrate worker-a fixtures
make capacity CAPACITY_ARGS='--no-build'
```

## Controlled workload mix

`cmd/fixtures` serves HTTP pages and assets only for benchmarking. It is not part
of the public API or normal application Compose profile.

| Profile | Browser work |
| --- | --- |
| `article` | Long article text, links, images, metadata extraction and scrolling |
| `feed` | Initial post list, incremental DOM growth up to a bounded 500 posts, lazy images, repeated extraction and scrolling |
| `dashboard` | HTTP data fetch, a 16 MiB retained heap, repeated sorting of 20,000 values, canvas rendering, DOM cards, scrolling and extraction |
| `mixed` | Rotates all three profiles across sessions and rounds, including low-concurrency runs |

Each session opens a fresh page through CDP, waits for navigation and initial
extraction, then scrolls and extracts structured text/links every second. Browser
JavaScript exceptions and unsuccessful extraction fail the lifecycle. This mix
represents common scraping operations; it is not a reproduction of Reddit or
customer-specific pages. Replay those pages and interaction patterns on production
hardware before treating the result as a production capacity commitment.

## Qualification

Each batch must complete creation, navigation, ongoing extraction and deletion.
The defaults require:

- Startup p95 <= 2 seconds, navigation/initial extraction p95 <= 5 seconds, and
  ongoing extraction p95 <= 1 second.
- Complete worker RSS and container samples, with at least two fresh steady
  resource samples per round; stale snapshots or worker restarts fail the stage.
- Container CPU and memory limits matching the requested budget, and kernel
  memory peak <= 70% of the limit. RSS remains a separate diagnostic measurement.
- Steady CPU utilization p95 <= 80% of the CPU quota, and steady throttled-period
  fraction p95 <= 20%. Startup bursts are measured separately from steady CPU.
- No swap usage, OOM/memory-limit/task-limit events, and task usage <= 80% of its
  configured limit.

Resource telemetry covers the worker and its browser descendants. The kernel's
`memory.peak` is retained for the lifetime of the cgroup, so the command recreates
the worker between workload checks and validation runs. CPU utilization comes
from deltas in `cpu.stat`, normalized by elapsed sample time and `cpu.max` quota.
The throttling fraction is the change in throttled periods divided by enforcement
periods. These interfaces are documented in the
[Linux cgroup v2 reference](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html).

The API has a separate 512 MiB/1 CPU/128-task test allocation; the summary records
its kernel memory peak. This experiment tests browser API traffic, not a sustained
registration/password-hashing load, so it does not establish an authentication
request-rate capacity. PostgreSQL/Redis sizing also needs its own traffic tests.

## Run a profile against an existing isolated worker

Start a fixture server reachable by the browsers:

```sh
go run ./cmd/fixtures
```

On a native worker on the same host, use `http://127.0.0.1:8082`. In Compose the
fixture origin is `http://fixtures:8082`; inside a container, loopback refers to
that container. Supply an API key and private worker credential as usual:

```sh
make loadtest LOADTEST_ARGS='--workload mixed --fixture-url http://fixtures:8082 --concurrency 1,2,4 --rounds 3 --hold 20s --metrics-urls http://localhost:8090 --worker-memory-mib 4096 --worker-cpus 4 --require-container-resources'
```

Use all worker metrics origins for a multi-worker experiment. Total concurrency
then covers the whole worker set; the report records observed per-worker counts.
For custom customer pages, `--target-url` retains the original navigation/probe
mode. Extend the controlled action fixtures when custom workflows require clicks,
multiple tabs, downloads, streaming media, or specific JS behavior.
