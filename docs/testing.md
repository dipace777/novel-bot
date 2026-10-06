# Testing and CI

Run from the repository root with the Go version in `go.mod`:

```sh
make test             # race tests; no Docker dependencies started
make vet build
make check sqlc-check # formatting, OpenAPI JSON, Compose, pinned inputs, generated SQL
make test-integration # provision PostgreSQL + Redis, execute required integration cases
make test-e2e         # build/run API, migrations, and two real Chromium worker images
```

`make check` needs the Docker Compose CLI but does not need a running daemon.
Container suites need Docker Desktop on macOS or Docker Engine/Compose on Linux,
network access for uncached images/modules, and a host that permits Chromium's
unprivileged user namespace sandbox. Start Docker before those suites. The e2e
suite launches only a few browsers; its passing result does not measure capacity.
`make capacity` remains the resource-sizing workflow.

## Dependency integration suite

`cmd/testsuite` starts one digest-pinned PostgreSQL 17 and one Redis 8 container
using Testcontainers for Go, on a uniquely named network. Ports bind to loopback
and are dynamically allocated. It passes their URLs to the existing test packages,
which retain per-test PostgreSQL schemas and Redis namespaces. No shared database
is flushed, no developer `.env` is sourced, and deployment secrets are not used.
Readiness checks run before test execution. Containers/network are removed on
success and failure; Testcontainers' resource reaper is an additional safeguard.
Do not disable the reaper in CI.

Required test names are discovered from the repository's PostgreSQL/Redis
integration files and non-browser HTTP cluster cases. Go JSON evidence is checked
for skipped cases, failed cases, and missing passing cases. A missing Docker daemon,
failed dependency startup, or skipped required test fails the command. Requests
always use `-count=1`; cached results cannot substitute for a test run.

For development against already provisioned infrastructure:

```sh
TEST_DATABASE_URL='postgres://test-user:...@localhost:6927/testdb?sslmode=disable' \
TEST_REDIS_URL='redis://localhost:6930/0' \
make test-integration INTEGRATION_ARGS=-external
```

Use a dedicated test database/user with permission to create schemas. External
mode retains isolated data cleanup and does not stop your services. CI rejects
external mode and always provisions isolated dependencies.

## Browser end-to-end suite

Tests under `tests/e2e` use the `e2e` build tag and the actual Dockerfile's API,
worker, and migration targets. The suite checks registration/login/key issuance,
private worker authentication, cross-worker REST/CDP routing, renderer namespace
and seccomp sandbox status, API restart/reconnect, draining while CDP remains
usable, worker lease fencing, profile cleanup, and tenant quota reuse.

Worker containers use the checked-in seccomp profile, a non-root image user,
dropped capabilities, no added `SYS_ADMIN`, no-new-privileges, an init process,
512 MiB shared memory, and the current 4 CPU/4 GiB/no-swap/2,048-task envelope.
One slot per worker makes placement assertions deterministic; it is not a
capacity setting for deployment. Broken sandboxing fails the suite instead of
falling back to `--no-sandbox` or privileged containers.

OpenAPI is validated by kin-openapi. Unit checks compare mounted route patterns,
methods, security alternatives, and references with the contract. E2e validates
observed REST status codes, headers, and bodies against response schemas. This
does not automatically generate documentation or exhaust every possible error;
keep the existing handler/error tests and update the spec with route changes.

Native browser/process tests remain available with the `TEST_CHROMIUM_PATH`,
`TEST_API_BINARY`, and `TEST_WORKER_BINARY` environment flow documented in
[process architecture](processes.md). They are opt-in in `make test`; the required
CI browser coverage comes from `make test-e2e`, without a host Chrome installation.

## Evidence and failures

Evidence goes to ignored `test-results/integration/tests.jsonl` and
`test-results/e2e/tests.jsonl`. Failure logs go to each suite's `logs/` directory,
including dependency startup failures. Logs redact randomly generated database,
private worker, and account/API credentials; environment dumps are not captured.
Artifacts are bounded and removed/replaced only within the current suite's paths.

`.github/workflows/ci.yml` runs unit/contract checks and both container suites on
Linux, with pinned action commits and tool versions. It uploads test evidence and
redacted logs even on failure. Tagged releases additionally build identified image
archives, generate SBOMs, and scan dependencies/images; see
[release policy](releases.md). Adding production manifests later requires extending
`scripts/check.py` and CI to validate them before deployment.
