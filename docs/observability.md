# Observability

Object Store separates metric definition and collection from telemetry export. Protocol and runtime packages depend only on plain Go value types in `internal/metrics` (standard library only, no `context` import, no third-party telemetry SDKs). Export transports, configuration, scheduling, and adapter selection live in `internal/telemetry`, which sits outside the protocol core and is never imported by it.

## Architecture and dependency boundary

```text
components  --->  internal/metrics  <---  internal/telemetry
```

- **`internal/metrics`** defines `Kind` (`Counter`, `Gauge`), `Label`, `Descriptor`, `Point`, `Snapshot`, `Source`, and `Batch`, along with the fixed `Catalogue()`, deep-copy `Clone()`, and `Validate()`. It imports only the Go standard library and omits `context` so deterministic simulation packages can import it safely.
- **`internal/telemetry`** defines `Config`, `Identity`, `Collector`, `Adapter`, `Factory`, `Open`, `Runner`, the static adapter registry (`internal/telemetry/registry.go`), and the built-in OTLP/HTTP JSON adapter (`internal/telemetry/otel.go`).
- **One adapter per process.** Fan-out to multiple backends belongs in the OpenTelemetry Collector rather than inside the node process.

### Bounds and semantics

- Every metric in `metrics.Catalogue()` is either a cumulative monotonic `Counter` (suffix `_total`) or a nonnegative integer `Gauge` (no `_total` suffix). Units are `1`, `By`, or `ns`.
- `metrics.Validate` enforces the frozen catalogue, finite label value domains, uniqueness of components and series within a batch, at most 2,048 points per batch, and a 1 MiB encoded batch limit (`MaxBatchBytes`).
- There are no histograms or latency percentiles in this model; HTTP latency is represented by cumulative duration nanoseconds and count so mean latency can be computed at fixed cardinality.
- Common identity fields (`node_id`, `region`) are carried on `Batch` metadata and attached by adapters rather than repeated in component `Point.Labels`. `boot_id` is included in OTLP resource metadata and omitted from Prometheus series labels so process restarts do not multiply series.
- Values remain exact `uint64` inside `metrics.Batch`. Float-backed destinations such as Prometheus round integers above 2^53; OTLP encodes values up to `MaxInt64` as decimal `asInt` strings and larger `uint64` values as `asDouble`.

## What is measured per component

`metrics.Catalogue()` freezes 83 descriptors across 10 components. Metric names below omit the common `objstore_` prefix. Braces show the exact allowed values for each label; unbounded request strings (object keys, bucket names, tenant IDs, peer addresses, error text) are never used as labels.

> **Current wiring status:** `internal/metrics` defines and validates the full 83-descriptor catalogue below, and `telemetry.Runner` emits the `telemetry` self-metrics snapshot on every cycle. Component `Snapshot()` methods, `internal/noderuntime` collection, HTTP instrumentation, and `cmd/objnode` observability flags/config are not yet connected in the node binary on this branch.

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

## On/off toggle and common configuration

Observability is **off by default**. `telemetry.Config` unmarshals strictly from JSON (unknown fields, trailing JSON, and `null` common fields are rejected with `telemetry.ErrConfig`):

```json
{
  "enabled": false,
  "adapter": "otel",
  "interval": "10s",
  "timeout": "2s",
  "settings": {}
}
```

| Field | Default | Rules |
| --- | --- | --- |
| `enabled` | `false` | Master switch. When `false`, `telemetry.Open` returns `(nil, nil)` without invoking any adapter factory or opening external resources, and a disabled `Runner` makes zero `Collect` calls. |
| `adapter` | `"otel"` | Must match a key in `internal/telemetry/registry.go`. Validated **even when `enabled` is `false`**, so unknown adapter names always fail startup. |
| `interval` | `"10s"` | Go duration string in `[100ms, 1h]`. Validated even when disabled. |
| `timeout` | `"2s"` | Go duration string with `0 < timeout < interval`. Applied independently to each `Collect` and `Publish` call. Validated even when disabled. |
| `settings` | `{}` | Adapter-specific JSON object (`null` or omitted normalizes to `{}`). Decoded strictly by the selected adapter factory only when `enabled` is `true`. |

### Lifecycle

1. Create one `telemetry.Identity` via `telemetry.NewIdentity(nodeID, region)` so the adapter's resource metadata and every stamped `metrics.Batch` share the same `BootID` and `StartUnixNano`.
2. Call `telemetry.Open(ctx, cfg, id)`. If `!cfg.Enabled`, it returns `(nil, nil)` and no `Runner` is needed.
3. Construct a `Runner` with `telemetry.NewRunner(cfg, id, collector, adapter, logf)` and run `r.Run(ctx)` on a single goroutine. `Runner` collects immediately, then on each `interval` tick, coalescing missed ticks rather than building a queue. Failure logs are rate-limited to one per failure class (`collection`, `validation`, `export`) per minute and omit raw sink errors so credentials cannot leak into logs.
4. Call `r.Shutdown(ctx)` to cancel and join the runner loop before calling `adapter.Shutdown(ctx)`. `Shutdown` is idempotent, and `Publish` after shutdown returns `telemetry.ErrClosed`.

## Selecting an adapter

Adapters are selected by name in `telemetry.Config.Adapter` via the static `factories` map in `internal/telemetry/registry.go`.

### `otel` (registered)

Exports cumulative monotonic sums and gauges over OTLP/HTTP JSON (`POST /v1/metrics`, `Content-Type: application/json`) using the Go standard library.

```json
{
  "enabled": true,
  "adapter": "otel",
  "interval": "10s",
  "timeout": "2s",
  "settings": {
    "endpoint": "http://otel-collector:4318/v1/metrics",
    "insecure": true
  }
}
```

| Setting | Default | Notes |
| --- | --- | --- |
| `endpoint` | *(required)* | Full `https://` or `http://` URL ending in `/v1/metrics`, with no userinfo, query string, or fragment. |
| `insecure` | `false` | Must be explicitly `true` to permit plaintext `http://`. |
| `ca_file` | `""` | Optional PEM certificate bundle appended to the system root pool (TLS 1.2 minimum). |
| `headers_file` | `""` | Optional JSON object (`<= 64 KiB`) of extra HTTP headers (e.g. bearer tokens). Forbids `Content-Type`, `Host`, `Content-Length`, and control characters; header values are redacted from receiver warning logs. |

Behavior guarantees:

- Enforces the 1 MiB encoded request bound and a 64 KiB response bound; redirects are not followed.
- Honors the caller's context deadline (falling back to 2 seconds if no deadline is set).
- Retries at most once within the remaining publish deadline on transport failures or HTTP `429`, `502`, `503`, `504`, honoring `Retry-After`.
- Returns `telemetry.ErrPartial` (without retry) on HTTP `200` responses reporting non-zero `partialSuccess.rejectedDataPoints`.
- Attaches `service.name=objstore`, `service.instance.id=<node_id>`, `region`, and `boot_id` on the OTLP resource, and `node_id` and `region` on each data point.

### Other adapters

Only `otel` is currently registered in `internal/telemetry/registry.go`. Direct Prometheus HTTP scraping (`prometheus`) and local CSV disk flushing with DuckDB SQL analysis over flushed CSV segments (`csv`) are not yet registered on this branch.

## Running the local OTel Collector and Prometheus stack

`compose.observability.yaml` layers pinned OpenTelemetry Collector (`0.145.0`) and Prometheus (`v3.9.1`) containers onto `compose.yaml` over an isolated `observability` bridge network. Image digests are recorded in `deploy/observability/images.lock`.

- `deploy/observability/otel-collector.yaml` accepts OTLP/HTTP on `0.0.0.0:4318` and exposes a Prometheus scrape endpoint on `0.0.0.0:9464` with `translation_strategy: UnderscoreEscapingWithoutSuffixes` so `objstore_*` metric names are preserved verbatim.
- `deploy/observability/prometheus.yaml` scrapes `otel-collector:9464` every `1s` with `honor_labels: true` and drops `boot_id` via `metric_relabel_configs` so Collector `target_info` resource attributes do not create per-boot Prometheus series.
- Ports stay internal to the `observability` bridge network by default. Always pass a distinct `-p` / `COMPOSE_PROJECT_NAME` so the observability containers do not collide with the base `compose.yaml` project.

### Verify the Collector and Prometheus pipeline

Start the Collector and Prometheus services, run the end-to-end transport fixture from inside the `dev` container, and tear down only that Compose project:

```sh
PROJECT=objstore-obs-demo
docker compose -p "$PROJECT" -f compose.yaml -f compose.observability.yaml build dev
docker compose -p "$PROJECT" -f compose.yaml -f compose.observability.yaml run --rm -T --no-deps \
  otel-collector validate --config=/etc/otelcol-contrib/config.yaml
docker compose -p "$PROJECT" -f compose.yaml -f compose.observability.yaml up -d otel-collector prometheus
docker compose -p "$PROJECT" -f compose.yaml -f compose.observability.yaml run --rm -T --no-deps \
  -e OBS_FIXTURE=1 dev bash -euc '
    until curl -fsS http://otel-collector:9464/metrics >/dev/null \
      && curl -fsS http://prometheus:9090/-/ready >/dev/null \
      && curl -fsS http://prometheus:9090/api/v1/targets | jq -e ".data.activeTargets | any(.health == \"up\")" >/dev/null; do
      sleep 0.2
    done
    go test -v -count=1 -run "^TestOTelCollectorFixture$" ./internal/telemetry
  '
docker compose -p "$PROJECT" -f compose.yaml -f compose.observability.yaml down --remove-orphans
```

`TestOTelCollectorFixture` runs `telemetry.Runner` with the `otel` adapter against `http://otel-collector:4318/v1/metrics`, then queries `http://prometheus:9090/api/v1/query` to verify that `objstore_http_requests_total` advances from `7` to `15`, `objstore_http_requests_in_flight` reads `1`, and `count({boot_id!=""}) or vector(0)` is `0`.

`compose.observability.yaml` and `deploy/observability/obs-node.json` also define an `obs-node` service and `test/observability/otel_test.go` (`OBS_INTEGRATION=1`) for live node export once `cmd/objnode` supports the `"observability"` config block.

## Adding a new adapter

Adding a runtime adapter touches one new Go file and one line in the registry — no `init()` hooks, no changes to `internal/metrics`, and no edits to protocol components or `Runner`.

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
       "otel":    newOTel,
       "example": newExample,
   }
   ```

3. **Run the shared contract suite in `internal/telemetry/<name>_contract_test.go` (`package telemetry_test`):**

   Call `adaptertest.Run(t, makeFixture)` with a `Fixture` supplying your opened `Adapter`, a transport-specific `Readback`, and (for external I/O sinks) a `FailNext` trigger. The shared suite tests zero and increasing counters, gauge transitions, values above 2^53, label escaping, duplicate-publish replacement, caller slice isolation, new-process zero reset, invalid batch rejection, canceled `Publish`, sink failure recovery, idempotent `Shutdown`, and goroutine leaks. File-backed adapters should also add format-specific tests asserting exact `uint64` fidelity and batch metadata (`BootID`, `Sequence`, timestamps).
