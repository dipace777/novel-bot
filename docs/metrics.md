# Worker metrics

Restart `make run-worker` after updating the code. Each worker exports Prometheus text
metrics at its private `WORKER_URL/metrics`, normally
`http://127.0.0.1:8090/metrics`. This endpoint requires
`Authorization: Bearer <worker credential>`. Application API keys do not grant
access. It does not require the per-incarnation or tenant headers used for browser
execution, and remains available when the worker is fenced. The public API does
not expose `/metrics`.

`WORKER_AUTH_TOKEN` is required by both APIs and workers. The local load-test
command reads it from `.env`; remote generators may use `LOADTEST_WORKER_TOKEN`.
Workers and monitoring need only this private credential, never `API_KEY_PEPPER`.

## Exported measurements

Every series has `worker_id`. No tenant IDs, browser session IDs, API keys, page
URLs, or PID labels are exported.

| Metric | Meaning |
| --- | --- |
| `novelbot_browser_startup_seconds` | Histogram from local launch to CDP readiness, labeled `outcome`: success, timeout, canceled, error |
| `novelbot_worker_capacity` | Configured browser slots |
| `novelbot_worker_reserved_sessions` | Slots including launches and browsers being stopped |
| `novelbot_worker_starting_sessions` | Launches in progress |
| `novelbot_worker_active_sessions` | Ready/stopping local sessions |
| `novelbot_worker_ready` | Local worker lease valid: 1, fenced: 0 |
| `novelbot_worker_accepting_sessions` | New-session admission available: 1, draining/fenced: 0 |
| `novelbot_worker_draining` | Draining with a valid lease: 1, otherwise: 0 |
| `novelbot_worker_drain_deadline_seconds` | Drain deadline Unix timestamp; 0 before drain |
| `novelbot_worker_inflight_creates` | Admitted create RPCs through publication/cleanup |
| `novelbot_browser_rss_bytes` | Sum of RSS across all owned Chromium process groups |
| `novelbot_browser_rss_max_bytes` | Largest RSS sum for one browser process group in the latest sample |
| `novelbot_browser_processes` | Sampled Chrome processes, including renderers and helpers in those groups |
| `novelbot_worker_service_rss_bytes` | RSS of the Go worker process from the same sample |
| `novelbot_browser_memory_sample_success` | Last sample complete and successful: 1; unavailable/partial: 0 |
| `novelbot_browser_memory_sample_timestamp_seconds` | Time of the last successful memory sample |
| `novelbot_browser_memory_sample_interval_seconds` | Background sampling interval |
| `novelbot_worker_cleanup_pending` | Stopped browser directory releases awaiting retry |
| `novelbot_worker_events_total` | Session stop, failed publication/cleanup, lease failure, and memory sampling events |

Drain events are `drain_started`, `drain_publish_failure`, `drain_completed`, and
`drain_forced`. A draining worker can have `worker_ready=1` and
`worker_accepting_sessions=0`: lease health keeps existing sessions usable.

Standard Go runtime metrics are included. The standard Prometheus process
collector also exports process measurements where supported by the OS; these
refer to the Go process, not its Chromium children.

Memory collection runs outside HTTP requests/scrapes. `METRICS_SAMPLE_INTERVAL`
defaults to `5s` and accepts `250ms` through `1m`. Linux reads `/proc` once per
sample; macOS and other Unix systems use one `ps` snapshot. The launcher tracks
owned process groups from process start through reaping, including startup and
shutdown. The sampler requires visibility of the worker and Chromium processes in
the same PID namespace. Unsupported systems report sample failure rather than
zero memory as a successful measurement.

RSS includes shared mappings, so summing processes can count shared pages more
than once. It is a conservative diagnostic estimate, not container memory usage
or PSS. Samples can miss short allocation peaks. On failures, prior memory values
are retained; check success and timestamp rather than interpreting stale values
as current. The worker process's RSS is reported separately. For hard container
limits, also measure cgroup memory current/peak, OOM events, CPU, and file descriptors.

## Prometheus

[monitoring/prometheus.yml.example](../monitoring/prometheus.yml.example) contains
a private-worker scrape configuration. Adjust its targets to reachable worker
addresses and provide the credential through its `credentials_file` in a mounted
secret. Scrape every replica separately. Use stable, unique `WORKER_ID` values
for useful time series across restarts. The sample target assumes Prometheus runs
on the same host; a container's `127.0.0.1` refers to that container.

Successful local startup p95 by worker:

```promql
histogram_quantile(0.95,
  sum by (worker_id, le) (
    rate(novelbot_browser_startup_seconds_bucket{outcome="success"}[5m])
  )
)
```

Reserved capacity utilization:

```promql
novelbot_worker_reserved_sessions / novelbot_worker_capacity
```

Sampled worker-plus-browser RSS:

```promql
novelbot_browser_rss_bytes + novelbot_worker_service_rss_bytes
```

Check `novelbot_browser_memory_sample_success == 0`, stale sample timestamps,
`novelbot_worker_ready == 0`, startup error/timeout rates, and accumulating cleanup
retries. The lifecycle/event label values are fixed by application call sites.

## Container resources

Workers also sample their own Linux cgroup v2 alongside RSS. These snapshots include
Chromium children, container page cache, shared memory, and all cgroup tasks. On
macOS, unsupported kernels, or incomplete/unreadable cgroup files,
`novelbot_container_sample_success` is zero; resource qualification fails explicitly.
Finite CPU/memory/task limits are required by the capacity experiment.

| Metric suffix (prefix `novelbot_container_`) | Meaning |
| --- | --- |
| `sample_success`, `sample_timestamp_seconds` | Completeness and timestamp of the last complete resource snapshot |
| `memory_bytes`, `memory_peak_bytes`, `memory_limit_bytes` | Current usage, lifetime kernel peak, enforced limit |
| `swap_bytes` | Current swap use |
| `oom_events`, `memory_limit_events` | Cumulative OOM and memory-max events |
| `cpu_seconds`, `cpu_throttled_seconds` | Cumulative CPU usage and throttled time |
| `cpu_limit_cores` | CPU quota divided by enforcement period |
| `cpu_periods`, `cpu_throttled_periods` | Cumulative enforcement and throttled periods |
| `pids`, `pids_limit`, `pids_limit_events` | Tasks (processes plus threads), limit, cumulative rejected forks |

These are gauges from one coherent background snapshot, including the cumulative
cgroup values. The capacity runner computes deltas and detects counter resets;
it does not mistake a worker restart for lower CPU consumption. A limit of zero
means unlimited. Container memory and RSS have different accounting and are
reported separately. See [worker sizing](capacity.md) for qualification thresholds
and workload runs.
