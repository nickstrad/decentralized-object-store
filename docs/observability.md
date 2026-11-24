# Observability

Object Store separates metric definition and collection from telemetry export. Protocol, runtime, and HTTP packages depend only on plain Go value types in `internal/metrics` (standard library only, no `context` import, no third-party telemetry SDKs). Configuration, periodic scheduling, and export adapters live in `internal/telemetry`, which sits outside the protocol core and is never imported by it.

## Architecture and dependency boundary

```text
components / noderuntime / httpapi  --->  internal/metrics  <---  internal/telemetry
                                                  ^                       ^
                                                  +----  cmd/objnode  ----+
```

- **`internal/metrics`** defines `Kind` (`Counter`, `Gauge`), `Label`, `Descriptor`, `Point`, `Snapshot`, `Source`, and `Batch`, along with the fixed `Catalogue()`, deep-copy `Clone()`, and `Validate()`. It imports only the Go standard library and omits `context` so deterministic simulation packages can import it safely.
- **Protocol components** (`internal/node`, `internal/gossip`, `internal/storage`, `internal/erasure`, `internal/merkle`, `internal/occ`, `internal/saga`) own config-gated private metric state (`nil` when disabled) and expose `Snapshot() metrics.Snapshot` on the node owner loop. `(*node.Node).MetricSources()` returns all seven core sources in fixed order (`node`, `gossip`, `storage`, `erasure`, `merkle`, `occ`, `saga`).
- **`internal/noderuntime` and `internal/storage/httpapi`** expose `(*Runtime).Snapshot()`, `(*httpapi.Server).Snapshot()`, and `(*Runtime).MetricsCollector(extra ...metrics.Source)`. Each collection gathers all core component snapshots in one bounded, metrics-exempt owner-loop turn (canceled metrics reads do not increment `objstore_runtime_calls_abandoned_total` and do not queue behind a stalled loop), then samples runtime and HTTP atomic counters and gauges outside the loop.
- **`internal/telemetry`** defines `Config`, `Identity`, `Collector`, `Adapter`, `Factory`, `Open`, `Runner`, the static adapter registry (`internal/telemetry/registry.go`), the built-in `otel`, `prometheus`, and `csv` adapters, and `ReadCSVDir`.
- **`cmd/objnode`** parses the `"observability"` JSON config block and `-observability*` CLI flags, enables component metrics only when configured, opens the selected adapter before starting runtime and HTTP listeners, runs one collector goroutine, and drains shutdown cleanly within `-shutdown-timeout`.
- **One adapter per process.** Fan-out to multiple backends belongs in the OpenTelemetry Collector rather than inside the node process.

### Bounds and semantics

- **Zero-cost off path:** when observability is disabled, component metric state is `nil`, asynchronous callbacks bypass metric wrappers without allocating closures, no adapter factory runs, no listener or directory is opened, and no collector goroutine starts. Existing durability and audit mechanisms (including persisted OCC discard counts, logs, and outbox records) continue to operate independently of telemetry.
- **Fixed cardinality and bounded cost:** every metric in `metrics.Catalogue()` is either a cumulative monotonic `Counter` (suffix `_total`) or a nonnegative integer `Gauge` (no `_total` suffix), with units `1`, `By`, or `ns`. Snapshots emit zero-valued fixed series on the first batch, never scan unbounded object maps or perform disk/network I/O during collection, and stay constant in point count and allocation profile regardless of object count.
- **Validation bounds:** `metrics.Validate` enforces the frozen catalogue, finite label value domains, uniqueness of components and series within a batch, at most 2,048 points per batch, and a 1 MiB encoded batch limit (`MaxBatchBytes`).
- **No histograms or percentiles:** HTTP latency is represented by cumulative duration nanoseconds and duration count per operation so mean latency can be computed at fixed cardinality; percentiles are not exposed.
- **Identity and restart semantics:** common identity fields (`node_id`, `region`) are carried on `Batch` metadata and attached by adapters rather than repeated in component `Point.Labels`. `boot_id` is included in OTLP resource metadata and CSV rows, and omitted from Prometheus series labels so process restarts do not multiply series. All ordinary telemetry counters start at zero per process boot and saturate at `MaxUint64`.
- **Numeric precision:** values remain exact `uint64` inside `metrics.Batch` and CSV segments. Float-backed destinations such as Prometheus round integers above 2^53; OTLP encodes values up to `MaxInt64` as decimal `asInt` strings and larger `uint64` values as `asDouble`.
- **Component accounting notes:**
  - **OCC:** `objstore_occ_lww_discards_total` counts only committed increases in the authoritative persisted discard counter since store open (avoiding double-counting on rollback, repeated merge, or outbox replay), while `objstore_occ_lww_discards_persisted` reports the lifetime persisted total. Counts are per-node observations, not globally deduplicated lost writes.
  - **Storage and erasure:** local shard and byte gauges are initialized once at storage node startup and updated incrementally on disk writes and deletes; region-scoped storage facades and ephemeral `erasure.MeteredCodec` instances share their owner's storage inventory and lifetime erasure counters without double-counting or resetting totals on layout changes.
  - **HTTP:** operation labels match router predicates (for example, only `PUT` with `?tag` is classified as `tag`); unknown paths map to `unknown`, never raw URLs. Byte counters measure actual bytes read from request bodies and written to responses rather than declared `Content-Length`.

## What is measured per component

`metrics.Catalogue()` defines 83 descriptors across 10 components, all wired and exported when observability is enabled. Metric names below omit the common `objstore_` prefix. Braces show the exact allowed values for each label; unbounded strings (object keys, bucket names, peer addresses, error text) are never used as labels.

| Component | Counters (`_total`) | Gauges |
| --- | --- | --- |
| `node` | `node_operations_total{operation=put,get,delete,other;outcome=ok,error}`, `node_rebuilds_total{outcome=ok,error}`, `node_rebuilt_bytes_total` (`By`), `node_scrubbed_total`, `node_scrub_corrupt_total`, `node_collected_total{reason=obsolete,loser,orphan}`, `node_lost_declared_total`, `node_quota_rejections_total`, `node_migrations_ended_total{outcome=ok,error}` | `node_ready`, `node_operations_in_flight`, `node_rebuilds_in_flight`, `node_metadata_records` |
| `runtime` | `runtime_datagrams_dropped_total`, `runtime_connections_refused_total`, `runtime_bad_frames_total`, `runtime_calls_abandoned_total`, `runtime_unresolved_total`, `runtime_loop_panics_total` | `runtime_control_queue_depth`, `runtime_control_queue_capacity`, `runtime_datagram_queue_depth`, `runtime_datagram_queue_capacity`, `runtime_connections`, `runtime_connection_capacity`, `runtime_running`, `runtime_goroutines` |
| `http` | `http_requests_total{operation=health,members,bucket,object,tag,acl,quota,migration,unknown;status_class=2xx,3xx,4xx,5xx,other}`, `http_request_duration_nanoseconds_total{operation}` (`ns`), `http_request_duration_count_total{operation}`, `http_request_bytes_total` (`By`), `http_response_bytes_total` (`By`) | `http_requests_in_flight` |
| `gossip` | `gossip_probes_total{kind=direct,indirect;outcome=ok,timeout,error}`, `gossip_membership_transitions_total{state=alive,suspect,dead,left}`, `gossip_syncs_total{outcome=ok,error}`, `gossip_malformed_total` | `gossip_members{state=alive,suspect,dead,left}`, `gossip_updates_queued`, `gossip_probes_in_flight` |
| `storage` | `storage_shard_operations_total{operation=put,get,delete;outcome=ok,not_found,corrupt,error}`, `storage_shard_bytes_total{direction=read,write}` (`By`), `storage_rpc_timeouts_total` | `storage_requests_in_flight`, `storage_local_shards`, `storage_local_bytes` (`By`) |
| `erasure` | `erasure_operations_total{operation=encode,reconstruct;outcome=ok,error}`, `erasure_input_bytes_total{operation=encode,reconstruct}` (`By`), `erasure_output_bytes_total{operation=encode,reconstruct}` (`By`), `erasure_reconstructed_shards_total` | `erasure_operations_in_flight` |
| `merkle` | `merkle_rounds_total{outcome=equal,repaired,error}`, `merkle_hashes_compared_total`, `merkle_differences_total`, `merkle_repairs_total{outcome=ok,error}`, `merkle_budget_exhaustions_total` | `merkle_rounds_in_flight`, `merkle_entries` |
| `occ` | `occ_writes_total{outcome=ok,precondition_failed,error}`, `occ_merges_total{outcome=changed,unchanged,error}`, `occ_replication_rounds_total{outcome=ok,error}`, `occ_replication_errors_total{kind=network,disk,malformed}`, `occ_lww_discards_total`, `occ_persist_errors_total` | `occ_records`, `occ_lww_discards_persisted`, `occ_discard_outbox_depth` |
| `saga` | `saga_starts_total`, `saga_completions_total{outcome=completed,compensated,failed}`, `saga_step_attempts_total`, `saga_step_failures_total`, `saga_compensations_total`, `saga_compensation_failures_total`, `saga_retries_total`, `saga_recoveries_total` | `saga_active`, `saga_suspended` |
| `telemetry` | `telemetry_collections_total{outcome=ok,error}`, `telemetry_exports_total{outcome=ok,error,partial}`, `telemetry_skipped_ticks_total` | `telemetry_last_success_unixtime`, `telemetry_collection_duration_nanoseconds` (`ns`), `telemetry_export_duration_nanoseconds` (`ns`) |

## Configuration and CLI flags

Observability is **off by default**. Configure it in `objnode`'s JSON config file under `"observability"` or with CLI flags:

```json
{
  "id": "node-1:7946",
  "region": "us-east",
  "listen": ":7946",
  "http": ":8080",
  "data": "/var/lib/objnode",
  "observability": {
    "enabled": false,
    "adapter": "otel",
    "interval": "10s",
    "timeout": "2s",
    "settings": {}
  }
}
```

| JSON field | CLI flag | Default | Rules |
| --- | --- | --- | --- |
| `enabled` | `-observability` | `false` | Master switch. When `false`, `telemetry.Open` returns `(nil, nil)` without invoking any adapter factory or opening resources, and `objnode` constructs no collector runner. |
| `adapter` | `-observability-adapter` | `"otel"` | Must be one of `otel`, `prometheus`, or `csv`. Validated **even when `enabled` is `false`**. |
| `interval` | `-observability-interval` | `"10s"` | Go duration string in `[100ms, 1h]`. Validated even when disabled. |
| `timeout` | `-observability-timeout` | `"2s"` | Go duration string with `0 < timeout < interval`. Applied independently to each `Collect` and `Publish` call. Validated even when disabled. |
| `settings` | `-observability-settings` | `{}` | Adapter-specific JSON object (`null` or omitted normalizes to `{}`). Common JSON-object syntax is validated even when disabled; strict adapter field validation runs when `enabled` is `true`. |

Flag precedence and exit codes:

- Explicit CLI flags override only the corresponding JSON fields, including `-observability=false` overriding `"enabled": true` in a config file.
- `adapter` and `settings` stay independent: switching adapters via `-observability-adapter` while leaving stale settings from another adapter in the config file fails strict factory validation rather than silently discarding fields.
- Unknown JSON fields, trailing JSON, `null` common fields, unknown adapter names, invalid durations, or invalid adapter settings return `telemetry.ErrConfig` and cause `objnode` to exit with code `2` before starting listeners.
- Runtime sink failures (for example, an occupied Prometheus listen address or an unwritable CSV directory) cause `objnode` to roll back opened resources and exit with code `1`.

### Lifecycle

1. `objnode` validates configuration and creates one `telemetry.Identity` via `telemetry.NewIdentity(nodeID, region)` so adapter resource metadata and every stamped `metrics.Batch` share the same `BootID` and `StartUnixNano`.
2. `telemetry.Open(ctx, cfg, id)` opens the selected adapter (binding the local Prometheus listener or probing the private CSV directory). If `!cfg.Enabled`, it returns `(nil, nil)`.
3. After starting the node runtime and HTTP API, `objnode` starts one `telemetry.Runner` goroutine. `Runner` collects immediately and then on each `interval` tick, coalescing missed ticks rather than building a queue. Failure logs are rate-limited to one per failure class (`collection`, `validation`, `export`) per minute and omit raw sink errors so credentials cannot leak into logs.
4. On shutdown (`SIGINT`, `SIGTERM`, or node loop panic), `objnode` cancels and joins the runner loop first, drains HTTP and runtime within `-shutdown-timeout`, and then shuts down the adapter. `Shutdown` is idempotent, and `Publish` after shutdown returns `telemetry.ErrClosed`.

## Built-in adapters

Adapters are selected by name via the static `factories` map in `internal/telemetry/registry.go`: `otel`, `prometheus`, and `csv`.

### `otel`

Exports cumulative monotonic sums and gauges over OTLP/HTTP JSON (`POST /v1/metrics`, `Content-Type: application/json`) using the Go standard library.

```json
{
  "observability": {
    "enabled": true,
    "adapter": "otel",
    "interval": "10s",
    "timeout": "2s",
    "settings": {
      "endpoint": "http://otel-collector:4318/v1/metrics",
      "insecure": true
    }
  }
}
```

| Setting | Default | Notes |
| --- | --- | --- |
| `endpoint` | *(required)* | Full `https://` or `http://` URL ending in `/v1/metrics`, with no userinfo, query string, or fragment. |
| `insecure` | `false` | Must be explicitly `true` to permit plaintext `http://`. |
| `ca_file` | `""` | Optional PEM certificate bundle appended to the system root pool (TLS 1.2 minimum). |
| `headers_file` | `""` | Optional JSON object (`<= 64 KiB`) of extra HTTP headers (for example, bearer tokens). Forbids `Content-Type`, `Host`, `Content-Length`, and control characters; header values are redacted from receiver warning logs. |

Behavior guarantees:

- Enforces the 1 MiB encoded request bound and a 64 KiB response bound; redirects are not followed.
- Honors the caller's context deadline (falling back to 2 seconds if no deadline is set).
- Retries at most once within the remaining publish deadline on transport failures or HTTP `429`, `502`, `503`, `504`, honoring `Retry-After`.
- Returns `telemetry.ErrPartial` (without retry) on HTTP `200` responses reporting non-zero `partialSuccess.rejectedDataPoints`.
- Attaches `service.name=objstore`, `service.instance.id=<node_id>`, `region`, and `boot_id` on the OTLP resource, and `node_id` and `region` on each data point.

### `prometheus`

Exposes Prometheus text exposition format `0.0.4` on an independent HTTP listener separate from the object API.

```json
{
  "observability": {
    "enabled": true,
    "adapter": "prometheus",
    "interval": "10s",
    "timeout": "2s",
    "settings": {
      "listen": "127.0.0.1:9464",
      "path": "/metrics",
      "stale_after": "30s"
    }
  }
}
```

| Setting | Default | Notes |
| --- | --- | --- |
| `listen` | `"127.0.0.1:9464"` | Host and port for the dedicated scrape listener. Inside an isolated Docker network, bind `"0.0.0.0:9464"` so Prometheus can scrape the container. |
| `path` | `"/metrics"` | HTTP scrape path; must begin with `/` and contain no query string, fragment, or CR/LF characters. |
| `stale_after` | `"30s"` | Go duration in `[200ms, 2h]` and at least `2 * interval`. |

Behavior guarantees:

- `Publish` validates and encodes the batch outside the cache write lock, then atomically swaps the immutable exposition body and timestamp.
- Scrapes (`GET` and `HEAD`) read only the cached body — they never call `Collect`, never touch the node owner loop, and are bounded to 8 concurrent handlers with bounded read/header/write/idle timeouts.
- Returns HTTP `503` before the first batch is published, once `stale_after` has elapsed since the last publish, after shutdown, or when concurrency slots are full; healthy scrapes return HTTP `200` with `Content-Type: text/plain; version=0.0.4; charset=utf-8`.
- Sorts metric families and series deterministically, escapes HELP and label strings, emits `# HELP` and `# TYPE` once per family, preserves `node_id` and `region` labels, and never emits `boot_id` as a Prometheus label.

### `csv`

Flushes each validated batch as a self-describing, atomic `.csv` file segment on local disk for offline inspection, Go test assertions, or DuckDB SQL analysis without running a Collector or Prometheus server.

```json
{
  "observability": {
    "enabled": true,
    "adapter": "csv",
    "interval": "10s",
    "timeout": "2s",
    "settings": {
      "directory": "/var/lib/objnode/metrics",
      "max_file_bytes": 16777216,
      "keep_files": 8,
      "sync": true
    }
  }
}
```

| Setting | Default | Notes |
| --- | --- | --- |
| `directory` | *(required)* | Per-node directory owned by the effective user with private permissions (`0700`, no group/other bits, not a symlink). Created with mode `0700` if absent and probed for write access at startup. |
| `max_file_bytes` | `16777216` (16 MiB) | Maximum allowed segment size in `[1, 67108864]` (64 MiB). The 1 MiB `metrics.MaxBatchBytes` encoded batch limit applies independently. |
| `keep_files` | `8` | Number of finalized `.csv` segments retained (`1`..`1024`). Oldest segments are pruned only after the new segment is renamed into place. |
| `sync` | `true` | Calls `Sync()` on the temporary segment file before atomic rename. File sync does not guarantee directory-entry durability across abrupt power loss. |

Segment format and readback:

- Every `.csv` file contains one complete batch with a header row of 14 columns:
  `schema_version,node_id,region,boot_id,sequence,start_unix_nano,time_unix_nano,component,name,kind,unit,labels_json,counter_uint64,gauge_uint64`.
- Exactly one of `counter_uint64` or `gauge_uint64` is populated per row (decimal `uint64`); the other column is empty. `labels_json` holds a sorted JSON object of point labels. Empty component snapshots emit no rows, and wholly point-free batches are rejected (batches from `Runner` always include `telemetry` points).
- Writes go to a private temporary file (`.metrics-*.tmp`, mode `0600`) and are atomically renamed to a unique finalized `metrics-*.csv` filename. Interrupted writes leave only `.tmp` files, which `telemetry.ReadCSVDir` and `*.csv` globs ignore.
- `telemetry.ReadCSVDir(dir)` reads finalized `.csv` files back into validated `[]metrics.Batch` values with exact `uint64` precision and full batch metadata (including CRLF-safe identity fields).

## Analyzing CSV segments with DuckDB

DuckDB queries finalized `.csv` segments directly in memory — Object Store carries no Go DuckDB dependency, no cgo driver, and no runtime `.duckdb` writer.

- `deploy/observability/queries.sql` defines typed views over `*.csv` (`UBIGINT` columns preserve integers above 2^53):
  - `snapshots_v1`: typed rows from `read_csv('*.csv', header=true, nullstr='', columns=...)`.
  - `counter_deltas_v1`: deduplicated counter samples with per-`(node_id, boot_id, name, labels_json)` sequence deltas and elapsed seconds.
  - `occ_discards_v1`: per-sequence `objstore_occ_lww_discards_total` values and deltas.
  - `http_rates_v1`: HTTP requests per second grouped by `operation` and `status_class`.
  - `http_latency_v1`: mean HTTP request latency in nanoseconds per `operation` (`duration_ns / request_count`).
  - `in_flight_v1`: `objstore_http_requests_in_flight` gauge observations.
- `deploy/observability/query-csv.sh CSV_DIRECTORY [SQL_FILE]` verifies that finalized `.csv` files exist and that pinned DuckDB `v1.5.0` is available, changes into `CSV_DIRECTORY`, and runs `duckdb -bail -init /dev/null :memory:` with `queries.sql` (or a custom SQL file) without interpolating directory paths into SQL.
- `Dockerfile` defines an optional `dev-observability` target on top of the standard `dev` image that installs the pinned DuckDB `v1.5.0` `linux/amd64` CLI verified by SHA-256 (leaving the default `dev` image unchanged).

Build the optional analysis image and query a directory of flushed CSV segments:

```sh
docker build --target dev-observability -t objstore-dev-observability:local .
docker run --rm --network none \
  -v "$PWD:/src:ro" \
  -v /path/to/metrics:/metrics:ro \
  objstore-dev-observability:local \
  /src/deploy/observability/query-csv.sh /metrics
```

To run the bundled end-to-end CSV smoke (starts a real CSV-enabled `objnode` with `--network none`, issues health requests, verifies all 10 components and clean shutdown, and queries the segments through DuckDB):

```sh
OBS_ANALYSIS_IMAGE=objstore-dev-observability:local agent_work/10-observability/check-objnode-csv.sh
```

## Running the local OTel Collector and Prometheus stack

`compose.observability.yaml` layers pinned OpenTelemetry Collector (`0.145.0`), Prometheus (`v3.9.1`), and a sample `obs-node` container onto `compose.yaml` under the `observability` Compose profile over an isolated `observability` bridge network. Image digests are recorded in `deploy/observability/images.lock`.

- `deploy/observability/obs-node.json` runs `objnode` with `adapter: "otel"`, `interval: "1s"`, `timeout: "500ms"`, exporting to `http://otel-collector:4318/v1/metrics` (`insecure: true`).
- `deploy/observability/otel-collector.yaml` accepts OTLP/HTTP on `0.0.0.0:4318` and exposes a Prometheus scrape endpoint on `0.0.0.0:9464` with `translation_strategy: UnderscoreEscapingWithoutSuffixes` so `objstore_*` metric names are preserved verbatim.
- `deploy/observability/prometheus.yaml` scrapes `otel-collector:9464` every `1s` with `honor_labels: true` and drops `boot_id` via `metric_relabel_configs` so Collector `target_info` resource attributes do not create per-boot Prometheus series.
- Ports stay internal to the `observability` bridge network by default. Always pass a distinct `-p` / `COMPOSE_PROJECT_NAME` so observability containers do not collide with the base `compose.yaml` project.

### Verify the real `objnode` → Collector → Prometheus pipeline

Run the end-to-end verification script (validates the Collector config, builds `objnode` in Docker, starts `obs-node`, `otel-collector`, and `prometheus`, runs `TestObjnodeOTelPrometheus` inside `dev`, and tears down only that Compose project):

```sh
COMPOSE_PROJECT_NAME=objstore-obs-demo agent_work/10-observability/check-otel.sh
```

`TestObjnodeOTelPrometheus` (`test/observability/otel_test.go`, gated by `OBS_INTEGRATION=1`) sends `GET /_health` requests to `obs-node:8080`, queries `http://prometheus:9090/api/v1/query` to verify that `objstore_http_requests_total{node_id="obs-node:7946",operation="health",status_class="2xx"}` increases across request bursts, checks that `objstore_http_requests_in_flight{node_id="obs-node:7946"}` is present, asserts `count({boot_id!=""}) or vector(0)` is `0`, and confirms Collector `objstore_*` lines carry no `boot_id` label.

For direct Prometheus scraping without the Collector, configure `objnode` with `"adapter": "prometheus"` and `"settings": {"listen": "0.0.0.0:9464", "path": "/metrics", "stale_after": "30s"}` on the isolated network and point a Prometheus `scrape_configs` target at `<node>:9464`.

## Adding a new adapter

Adding a runtime adapter touches one new Go file and one entry in `internal/telemetry/registry.go` — no `init()` hooks, no changes to `internal/metrics`, and no edits to protocol components, `Runner`, or `cmd/objnode`.

1. **Implement `Adapter` and `Factory` in `internal/telemetry/<name>.go`:**

   ```go
   package telemetry

   import (
       "context"
       "encoding/json"
       "fmt"

       "github.com/nickstrad/object-store/internal/metrics"
   )

   type exampleSettings struct {
       // Adapter-specific JSON fields.
   }

   type exampleAdapter struct {
       id Identity
       // Synchronized sink state.
   }

   func newExample(ctx context.Context, id Identity, raw json.RawMessage) (Adapter, error) {
       var s exampleSettings
       if err := decodeStrict(raw, &s); err != nil {
           return nil, fmt.Errorf("%w: example settings", ErrConfig)
       }
       // Validate settings; return ErrConfig for bad configuration.
       return &exampleAdapter{id: id}, nil
   }

   func (a *exampleAdapter) Publish(ctx context.Context, b metrics.Batch) error {
       // 1. Return ErrClosed if Shutdown has been called.
       // 2. Respect ctx cancellation/deadline.
       // 3. Validate with metrics.Validate(b) and verify b's identity matches a.id.
       // 4. Enforce metrics.MaxBatchBytes on the encoded representation.
       // 5. Never retain mutable references to b's slices after returning.
       return nil
   }

   func (a *exampleAdapter) Shutdown(ctx context.Context) error {
       // Idempotent cleanup; abort or join in-flight publishes within ctx.
       return nil
   }
   ```

2. **Register the factory in `internal/telemetry/registry.go`:**

   ```go
   var factories = map[string]Factory{
       "otel":       newOTel,
       "prometheus": newPrometheus,
       "csv":        newCSV,
       "example":    newExample,
   }
   ```

   If the adapter needs validation that depends on the configured collection interval (as `prometheus` does for `stale_after >= 2*interval`), add an optional entry to `settingValidators` in `internal/telemetry/registry.go`.

3. **Run the shared contract suite in `internal/telemetry/<name>_contract_test.go` (`package telemetry_test`):**

   Call `adaptertest.Run(t, makeFixture)` with a `Fixture` supplying your opened `Adapter`, a transport-specific `Readback`, and (for external I/O sinks) a `FailNext` trigger. The shared suite tests zero and increasing counters, gauge transitions, values above 2^53, label escaping, duplicate-publish replacement, caller slice isolation, new-process zero reset, invalid batch rejection, canceled `Publish`, sink failure recovery, idempotent `Shutdown`, and goroutine leaks. File-backed adapters should also add format-specific tests asserting exact `uint64` fidelity and batch metadata (`BootID`, `Sequence`, timestamps).
