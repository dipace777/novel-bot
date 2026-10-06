# Production readiness roadmap

Last reviewed: 6 October 2026.

Goal: operate a tenant-isolated browser automation service with a public REST API
and authenticated CDP connections, scaling API replicas and browser workers
independently toward thousands of simultaneous sessions.

## How to use this file

- When asked to continue production-readiness work, start with the earliest
  unfinished task whose dependencies are complete. A specific user request takes
  precedence over this order.
- Implement one numbered task in reviewable increments. Update its checkboxes,
  relevant docs, configuration examples, OpenAPI contract, and tests together.
- Check a box only after its completion criteria are demonstrated. Record the
  test command, evidence path, date, and any remaining limitation in the work log.
- Inspect the current implementation before starting; this roadmap can become
  stale. Preserve existing tenant data and deployment settings.
- Use isolated infrastructure and dedicated tenants for failure/load tests. Never
  flush a shared Redis instance, expose credentials in reports, or treat mocked
  concurrency tests as browser capacity evidence.
- A completed roadmap item does not authorize an external production deployment.
  Keep infrastructure manifests and release artifacts reviewable in the repo.

## Existing foundation — preserve it

These capabilities are already implemented; future tasks should extend them.

- [x] Separate `cmd/api`, `cmd/worker`, and `cmd/migrate` processes.
- [x] PostgreSQL accounts, password hashing, login/logout, and API key management.
- [x] Tenant ownership checks, atomic Redis session/rate limits, and lifetime caps.
- [x] Redis worker registration, leases, incarnation fencing, routing, and quota cleanup.
- [x] Chromium lifecycle and authenticated API → worker → browser CDP proxying.
- [x] sqlc repositories, ordered checksummed migrations, and bundled Swagger UI.
- [x] Worker/browser metrics and controlled mixed scraping load tests.
- [x] Initial worker baseline: 6 starting/active sessions, 4 CPU quota, 4 GiB RAM,
  2,048 tasks, 512 MiB shared memory within the memory budget, and no swap.

Evidence: [capacity methodology](docs/capacity.md) and
[225 successful browser lifecycles](docs/benchmarks/mixed-scraping-4g-4cpu.json).
Eight sessions passed the local sweep; six is the selected operating limit.
Each session owns a separate browser and can contain multiple tabs. The benchmark
used one workload tab per session on one Linux ARM64 Docker worker. It does not
establish production multi-tab capacity or thousand-session capacity. Running
isolated profiles also does not establish a security boundary between tenants.

## Ordered implementation tasks

### 01. Worker draining and bounded graceful shutdown — complete

- [x] Add worker admission state to Redis: ready, draining, and unavailable;
  preserve existing session routing while atomically excluding draining workers
  from new reservations. Heartbeat renewal must never reset draining to ready.
- [x] Separate lease health from acceptance of new sessions. Keep heartbeats,
  existing CDP connections/reconnections, deletion, and expiry working while draining.
- [x] Handle reservations made just before drain begins: define whether they
  finish or are rejected/released, and test the race without leaking quota.
- [x] Add an idempotent, authenticated private drain operation, status reporting,
  drain metrics, and configurable maximum drain duration. A worker may exit only
  after browsers and in-flight launches finish or bounded forced cleanup completes.
- [x] Make SIGTERM enter draining. Lease loss must still fence immediately;
  draining must never extend the right to run browsers after lease expiry.
- [x] Align container/deployment termination deadlines with the drain deadline
  and cleanup margin; retain liveness and private routing during that interval.

**Done when:** a two-worker real-browser test proves an existing session on A
survives drain, new sessions land on B, reconnect/delete still work on A, and A
exits cleanly. Also verify duplicate drain requests, concurrent startup, forced
deadline, and Redis outage during drain. Starting points: `internal/worker`,
`internal/sessions`, `internal/storage/redis`, `internal/httpapi/worker.go`,
`cmd/worker`, and `docs/processes.md`.

**Implemented policy:** launches admitted locally before draining may finish and
publish. Later arrivals, including older Redis reservations, receive `503
worker_draining` and release their reservation; transient release failures retry.
Redis preserves draining across renewals and excludes it from new reservations.
Unavailable means an expired/unregistered lease, with local status reporting
`unavailable`; Redis does not keep an indefinitely stale worker entry.

**Validation (6 October 2026):** `go test -race ./...`, `go vet ./...`,
`make build`, and `docker compose --profile app build api worker-a` passed.
Integration suites ran against temporary, isolated PostgreSQL/Redis containers
with real Chromium and built API/worker binaries; required tests executed rather
than skipped. PostgreSQL tests passed with `go test -race -count=1 -v
./internal/storage/postgres`; Redis and HTTP tests passed with `go test -race
-count=1 -v ./internal/storage/redis ./internal/httpapi` with `TEST_DATABASE_URL`,
`TEST_REDIS_URL`, `TEST_CHROMIUM_PATH`, `TEST_API_BINARY`, and `TEST_WORKER_BINARY`
set for that isolated run. Evidence:

- [Worker lifecycle tests](internal/worker/drain_test.go): admitted startup,
  late reservations, duplicate requests, pending Redis confirmation, forced
  deadline, lease loss during an outage, and retrying quota cleanup.
- [Redis integration tests](internal/storage/redis/drain_integration_test.go):
  concurrent placement, renewal/registration persistence, publication and routing
  during drain, and incarnation fencing.
- [Two-worker browser test](internal/httpapi/drain_integration_test.go): existing
  CDP/reconnect/delete on A, new placement on B, and clean drain completion.
- [Separate-process test](internal/httpapi/processes_integration_test.go): signal
  draining, preserved CDP routing, zero-exit worker shutdown, profile cleanup,
  API restart, and lease fencing.

Compose now allows `WORKER_DRAIN_TIMEOUT=15m` plus a
`WORKER_STOP_GRACE_PERIOD=16m` termination budget. Supplied termination budgets
are validated against the drain deadline plus at least 30 seconds for cleanup.
Planned drains stay stopped; failures restart. See [operator commands and private
routing requirements](docs/processes.md#worker-maintenance--draining). Production
orchestrator manifests/hooks remain task 07; real network-partition qualification
remains task 10.

### 02. Automated CI and reproducible releases — implemented; CI qualification pending

- [x] Add CI for formatting, race tests, vet, binary builds, and sqlc consistency.
- [x] Add Testcontainers for Go helpers to provision PostgreSQL and Redis with
  pinned deployment-compatible images, readiness checks, dynamically mapped ports,
  and cleanup on success/failure. Share containers within a suite while preserving
  per-test PostgreSQL schemas and Redis namespaces; retain existing assertions.
- [x] Add `make test-integration` to provision those dependencies automatically.
  Keep `make test` usable without Docker; explicitly requested integration suites
  must fail when Docker/dependencies cannot start or required tests are skipped.
- [x] Add `make test-e2e` using Testcontainers to run the actual API image and two
  worker images with real Chromium, the production sandbox configuration, and an
  isolated network. Cover routing, restart, lease fencing, draining, and cleanup;
  preserve container logs on failure without exposing credentials.
- [ ] Run both container-backed suites in Linux CI with Docker available. Document
  local requirements and optional external test URLs for development; CI must
  use isolated infrastructure. Keep `make capacity` as the resource-sizing workflow.
- [x] Validate OpenAPI syntax and route/response coverage, plus Compose and future
  production manifests. Documentation is currently maintained manually.
- [x] Build versioned API/worker/migration images, record browser and base-image
  versions/digests, scan dependencies/images, and publish an SBOM in release artifacts.
- [x] Define a browser/security update schedule and repeat integration/capacity
  checks before promoting a new Chromium or runtime image.

**Done when:** a clean checkout produces the same identified release artifacts,
`make test-integration` and `make test-e2e` provision/clean up their own dependencies,
required integration evidence is attached to CI, and unavailable dependencies,
skipped required suites, stale generated SQL, or a broken browser sandbox fail
the pipeline. `make test` remains Docker-independent.

**Local evidence (6 October 2026):** `make check test vet build sqlc-check` and
actionlint passed. Required Testcontainers integration executed 14 cases without
skips; the image-based e2e scenario passed with real Chromium namespace/seccomp
sandboxes, API restart/reconnect, draining, lease fencing, profile cleanup, and
quota reuse. An unavailable explicit Docker host fails without socket fallback;
unit tests also reject skipped/missing test evidence. Both success and exercised
failure paths removed their temporary infrastructure and saved redacted failure
logs. See [validation summary](docs/benchmarks/testing-2026-10-06.json),
[testing commands](docs/testing.md), and [release policy](docs/releases.md).

Local identified image archives, package/browser records, CycloneDX SBOMs, and
Trivy reports were generated under ignored `release-artifacts/local/`. The scan
gate initially found fixable Perl/base-image and Testcontainers archive dependency
vulnerabilities; the snapshot security upgrade and dependency patch fixed those
blockers. Fixed HIGH/CRITICAL findings now number zero. This is a local development
artifact, with its dirty checkout recorded, not an approved release.
Unfixed findings, including HIGH/CRITICAL entries, remain in the saved reports
and require operator review before promotion; passing this gate is not a claim
that the images have no known vulnerabilities.

**Still required to close task 02:** push the reviewed changes and demonstrate
both jobs in the checked-in Linux CI workflow, then attach a clean tagged release's
artifact/SBOM evidence. Workflow syntax and local Docker/Linux browser behavior
were tested; GitHub Actions execution was not. Production manifest validation is
added when those manifests exist in task 07. A changed browser/runtime image still
requires a new target-hardware capacity qualification before promotion; the
historical six-slot benchmark is not a qualification of these rebuilt images.

### 03. Browser tenant isolation and network egress — public launch blocker

- [ ] Document the threat model: arbitrary tenant CDP commands, hostile pages,
  filesystem access, shared worker resources, downloads, and internal networking.
- [ ] Choose and implement a defensible tenant/session execution boundary for
  untrusted CDP clients. Evaluate per-session containers or stronger runtimes;
  isolated profiles under a shared OS user are insufficient by themselves.
- [ ] Preserve Chromium sandboxing, non-root execution, minimal capabilities,
  and the reviewed seccomp policy. Verify the chosen production host supports them.
- [ ] Enforce browser egress outside the client-controlled browser: deny access
  to worker/API control listeners, PostgreSQL, Redis, cloud metadata, loopback,
  private/link-local destinations, and infrastructure DNS names unless explicitly
  permitted. Keep required worker → Redis/control traffic separate from browser egress.
- [ ] Test IPv4/IPv6, redirects, DNS rebinding, WebSockets, subresources, service
  workers, and non-HTTP paths. API URL validation alone cannot protect CDP navigation.
- [ ] Bound downloads/profile disk usage and verify cleanup after normal exit,
  crashes, forced termination, and worker restart. Keep credentials and other
  tenants' profiles inaccessible from a session's filesystem.

**Done when:** adversarial tests cannot read another tenant's profile or platform
secrets, reach protected services, or escape the selected execution boundary.
Document residual risks and the supported trust model before opening registration.

### 04. Tenant resource fairness and worker pressure admission

- [ ] Measure multiple tabs, browser contexts, large DOMs, CPU-heavy pages,
  downloads, and bursty startups; define supported per-session resource envelopes.
- [ ] Add enforceable per-session memory/CPU/task/disk limits through the execution
  boundary selected in task 03. Use charged resources for hard memory limits;
  process RSS sums are diagnostics and can double-count shared pages.
- [ ] Define tab/context limits or resource-based admission for them. Verify any
  CDP-level enforcement covers all creation paths and cannot be bypassed by clients.
- [ ] Stop admitting new sessions under worker memory/CPU/task/disk pressure with
  hysteresis; stale/missing resource evidence must have a documented safe policy.
- [ ] Preserve tenant `429` versus worker-capacity `503` semantics and `Retry-After`.
  Enforce concurrent limits across ready and starting browsers during pressure changes.
- [ ] Add an authenticated, audited operator path for tenant policy changes and
  suspension; public registration cannot grant its own higher quotas.

**Done when:** a resource-heavy tenant stays within its envelope while another
tenant's sessions remain usable; multi-worker tests prove no quota leak or
over-admission during pressure, deletion, startup failure, or worker death.

### 05. Authentication, credentials, and abuse controls

- [ ] Choose invite-only or verified public registration for launch. Implement
  email verification and one-time, expiring password reset for public accounts.
- [ ] Add password change, logout-all/session revocation, and periodic removal of
  expired authentication records. Decide refresh-token behavior explicitly.
- [ ] Add account-based login throttling alongside trusted client-IP limits,
  enumeration-resistant recovery responses, and tests across API replicas.
- [ ] Define least-privilege API key scopes and operator permissions; enforce
  tenant suspension consistently across REST and new CDP connections.
- [ ] Define what key revocation/account suspension does to already-connected
  CDP clients and existing browsers, then implement and test that policy.
- [ ] Support key versioning and safe rotation of API key peppers and private
  worker credentials without invalidating all tenants unintentionally. Store
  production secrets outside `.env`/images; never log raw keys or tokens.
- [ ] Record security audit events for login, credential changes, key issuance/
  revocation, tenant policy changes, and privileged worker operations.

**Done when:** account recovery and rotation work end to end, unauthorized scope
and cross-tenant operations fail, and revocation behavior matches the published
contract. Recovery emails use a configured provider and contain no reusable credentials.

### 06. Session API reliability and application client contract

- [ ] Add tenant-scoped idempotency keys for session creation, with atomic
  in-flight ownership, request conflict detection, expiry, and retry-safe results.
  Define recovery when a browser starts but the response never reaches the client.
- [ ] Provide tenant-scoped session listing/status and explicit lifecycle states
  suitable for finding/reconnecting to a session after a client timeout.
- [ ] Publish stable error codes and retry guidance for `429`, `503`, startup
  timeout, stale ownership, worker loss, and reconnect failure.
- [ ] Add a maintained Playwright client example that creates a session,
  authenticates `connectOverCDP`, performs work, and deletes in `finally`.
- [ ] Define CDP credential transport, connection limits, credential expiry, and
  proxy behavior for supported clients; avoid long-lived secrets in URLs/logs.
- [ ] Update OpenAPI and add contract tests for auth, ownership, pagination,
  idempotency, failures, and API-version compatibility.

**Done when:** concurrent retries across two API replicas create at most one
browser per idempotency key, timeout recovery has a defined outcome, and an
external app can complete the documented workflow without accessing private workers.

### 07. Production deployment, TLS, and safe rollout

- [ ] Select the deployment platform/region and supported CPU architecture.
  Add production manifests separate from local Compose; record requests/limits,
  node capacity, worker addressing/discovery, and resource overhead.
- [ ] Configure public HTTPS/WSS, trusted proxies, WebSocket upgrade/idle timeout
  behavior, request/body limits, and access-log credential redaction.
- [ ] Keep worker controls/metrics, browser debug ports, Redis, and PostgreSQL
  private. Enforce authenticated encrypted API → worker transport and restricted
  service-to-service access; plan certificate/credential rotation.
- [ ] Configure startup/readiness/liveness probes, restart policies, disruption
  budgets and graceful rollout hooks. Browser saturation must not trigger restarts.
- [ ] Add a migration release job and backward-compatible schema rollout policy;
  bound total PostgreSQL connections across API replicas and maintenance jobs.
- [ ] Document rollback, immutable image promotion, configuration validation, and
  manual scaling. Reserve node space for replacement workers during draining.

**Done when:** staging deploy/rollback preserves worker sessions, API restarts
allow reconnect, private services are unreachable publicly, and HTTPS/WSS works
through the actual ingress. Depends on tasks 01–06.

### 08. PostgreSQL/Redis availability and disaster recovery

- [ ] Deploy PostgreSQL backups/PITR and a tested restore procedure; choose and
  record acceptable recovery time and data-loss objectives for account/key data.
- [ ] Choose a supported Redis HA topology and implement/test adapter support.
  The current adapter assumes one primary; Sentinel/Cluster are not configured.
- [ ] Audit every coordination Lua script for the selected topology. Redis Cluster
  multi-key operations require an intentional hash-slot strategy; hash-tagging
  everything to one slot does not distribute coordination load.
- [ ] Test failover consistency: lost/stale reservations, changed worker leases,
  duplicate identities, startup publication, and pending quota releases.
- [ ] Preserve fail-closed lease fencing during outages; never run browsers on
  stale ownership to preserve apparent availability. Document possible session loss.
- [ ] Add scheduled reconciliation/retention jobs with bounded batches and metrics
  for authentication records, orphaned metadata, and residual browser profiles.

**Done when:** restore and Redis failover drills meet recorded objectives and
produce no double ownership or permanently leaked quotas. Browser sessions remain
ephemeral; backups do not restore live Chromium processes.

### 09. Operational visibility, SLOs, and runbooks

- [ ] Define initial SLOs for authenticated REST availability, session startup,
  CDP connection establishment, and session-loss rate. Choose numeric targets
  before qualification runs; distinguish tenant rejections from platform failures.
- [ ] Add API/coordination metrics and correlated request/session lifecycle logs
  or traces. Keep tenant/session IDs out of unbounded Prometheus label sets and
  credentials, page contents, and sensitive URLs out of telemetry.
- [ ] Add dashboards/alerts for slot utilization, startup p95, pressure,
  throttling, OOMs, task/disk limits, stale samples, drain progress, lease loss,
  quota cleanup failures, PostgreSQL saturation, and Redis latency/availability.
- [ ] Add external synthetic REST + CDP checks and test alert delivery.
- [ ] Write incident runbooks for worker loss, Redis outage/failover, database
  outage, exhausted capacity, compromised keys, failed migrations, and rollback.

**Done when:** deliberate failures in staging produce actionable alerts with
working runbooks, and operators can explain a session's failure without exposing
tenant data. Define on-call responsibility before accepting production traffic.

### 10. Production workload and failure qualification

- [ ] Repeat sizing on deployment hardware and the execution boundary from task
  03; the current shared-worker benchmark does not size a new container/microVM model.
- [ ] Test real approved workload mixes, multiple tabs/contexts, slow networks,
  DOM growth, downloads, long-lived CDP, and simultaneous startup bursts.
- [ ] Run multi-hour soak/churn tests at proposed limits, measuring memory/task/
  disk growth, startup/action latency, cleanup, and cross-tenant fairness.
- [ ] Load-test auth, REST/CDP proxies, PostgreSQL connection budgets, and Redis
  coordination separately so browser limits do not hide control-plane bottlenecks.
- [ ] Drill worker OOM/kill, API restart, network partitions, Redis failover,
  database interruption, rolling upgrades, and overlapping drains.
- [ ] Preserve sanitized reports with workload versions, images, architecture,
  resource limits, observed concurrency, SLO results, and failure outcomes.

**Done when:** chosen limits pass task 09's SLOs and task 04's fairness envelope
over sustained runs; failures recover within the documented contract. This is
the evidence gate for a limited production pilot, not yet a thousand-session claim.

### 11. Independent autoscaling and global capacity policy

- [ ] Scale APIs using measured request/connection pressure; scale workers using
  eligible reserved slots, capacity rejection demand, resource headroom, and startup lead time.
- [ ] Exclude draining/unhealthy/stale workers from schedulable capacity, set
  minimum/maximum replicas, warm spare capacity, cooldowns, and scale-down stabilization.
- [ ] Make scale-down use task 01's drain flow; allocate replacement capacity
  before retiring busy workers. Test node provisioning delays and quota/cost ceilings.
- [ ] Define global admission/fairness at saturation. Keep bounded `503` responses
  initially; add a durable, cancellable queue only if the product requires waiting,
  with explicit queued-session API states, deadlines, and per-tenant fairness.
- [ ] Verify load-balancer, file descriptor, socket, conntrack, database, Redis,
  and network bandwidth limits at the intended fleet size.

**Done when:** ramp-up/down tests preserve existing sessions, never exceed tenant
or global limits, and reach the documented demand target within the scaling SLO.
Depends on draining, production deployment, telemetry, and qualified resource limits.

### 12. Staged launch and thousand-session validation

- [ ] Launch an invite-only pilot with explicit tenant quotas, supported workloads,
  ephemeral-session guarantees, privacy/retention policy, and incident ownership.
- [ ] Use durable lifecycle/usage events if customer reporting or billing is
  offered; do not bill from transient Redis counts or Prometheus samples.
- [ ] Validate increasing fleet concurrency in stages, for example 50 → 100 →
  250 → 500 → 1,000+, with enough live browser sessions for the claimed target.
- [ ] At each stage repeat sustained load, churn, multi-tenant fairness, worker
  loss, rollout, and scale-down tests; stop promotion when an SLO or safety gate fails.
- [ ] Record approved capacity, operational cost/resource budget, failure results,
  release version, and rollback decision for each promotion.

**Done when:** the actual deployment handles the claimed simultaneous browser
count within SLOs and resource/fairness limits, including failure/rollout drills.
At the current six-slot baseline, 1,000 sessions need at least 167 workers before
spare capacity; this is slot arithmetic, not evidence that the fleet will perform.

## Launch gates

- Limited production pilot: tasks 01–10 complete, with evidence and no unresolved
  isolation, credential exposure, ownership, or quota-cleanup defects.
- Automated fleet operation: task 11 additionally complete.
- Thousand-session production claim: task 12 demonstrates that concurrency on
  the target deployment. Never infer it from replica counts or unit tests.

## Deferred product features

These are separate scope unless needed by a customer or a gate above: persistent
profiles, recordings/screenshots, managed proxies, browser extensions, reusable
SDK packages, dashboards, SSO/MFA, regional scheduling, and managed scraping jobs.
Playwright remains an application client; adding a hosted Playwright job service
would require its own scheduler, quotas, and execution lifecycle.

## Reference material

- [Worker/process architecture](docs/processes.md)
- [Capacity experiment and limitations](docs/capacity.md)
- [Metrics](docs/metrics.md) and [load testing](docs/load-testing.md)
- [Current sandbox profile](deploy/README.md)
- [Kubernetes lifecycle hooks and termination budget](https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/)
- [Kubernetes autoscaling/custom metrics](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/)
- [Playwright container guidance for untrusted sites](https://playwright.dev/docs/docker)

## Work log

| Date | Task | Result / evidence | Remaining limitation |
| --- | --- | --- | --- |
| 2026-10-06 | Roadmap baseline | Reviewed current API/worker/Redis/auth implementation and recorded ordered tasks. Capacity evidence linked above. | Production gates remain open; next implementation is task 01. |
| 2026-10-06 | 01 — worker draining | Completed all task 01 criteria; race/vet/build checks, Docker image builds, isolated Redis/PostgreSQL integration suites, and two-worker real-browser/process shutdown tests passed. Commands and evidence paths are recorded above. | Deployment alignment covers Compose; production orchestrator routing/hooks remain task 07 and real network-partition qualification remains task 10. Testcontainers/CI are next in task 02. |
| 2026-10-06 | 02 — CI/Testcontainers/releases | Added required container suites, pinned Linux CI, contract validation, identified image/archive builds, SBOM/scanning gates, and update policy. Local race/vet/build/sqlc checks, 14 integration cases, real-browser e2e, archive generation, and the fixed HIGH/CRITICAL scan gate passed. Commands and evidence paths are recorded above. | Task remains open for actual Linux CI execution and clean tagged release artifact evidence. New image capacity qualification is required before promotion; production isolation/deployment gates remain open. |
