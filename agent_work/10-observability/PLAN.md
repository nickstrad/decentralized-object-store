# Object Store observability phase

Status: **PLANNED**. Planning baseline: main `379e622`. Branch: `feat/observability`.
No product implementation is part of this commit. This plan is the implementation brief;
[root PLAN](../../PLAN.md) remains the historical build plan and [STATE](../../STATE.md)
is the checkpoint authority. Nick's phase brief controls the seat split and deliverables.

## 1. Decisions and scope

Adopt Luca Sepe's “Metrics Without Framework Lock-In” pattern: components own runtime
state, expose snapshots, and know nothing about the telemetry tools consuming them.
The governing local sources are `/root/briefs/zgateway-backpressure-reference.txt`
(section at lines 755–762) and section 1 of
`/root/briefs/zgateway-observability-scale-releaseit-note.md`. The latter's proxy-specific
admission, tenant, and Release It! work is not additional Object Store scope.

- Preserve zero external production Go dependencies, including adapters. Implement a
  deliberately limited **OTLP/HTTP JSON metrics exporter using the standard library**.
  Do not hand-code protobuf binary or claim to implement the whole OTel SDK. First prove
  this adapter against a real Collector and Prometheus before building other adapters.
- The official Go SDK is the preferable general-purpose choice: maintained protocol,
  instrument aggregation, retries, resource conventions and ecosystem support. Its metric
  SDK/exporter and transitive protobuf/network modules would break today's root-module
  gate, even if imports were isolated from core. A nested module would require a second
  objnode composition/build path. Here we already own cumulative snapshots and need only
  sums and gauges, so the narrow JSON implementation has a defensible scope. Revisit this
  ruling if traces, exemplars, histograms, gRPC, or SDK integrations become requirements.
- DuckDB ships in this phase as a real runtime adapter backed by a **pinned DuckDB CLI**,
  behind the interface in section 6. No embedded Go driver this phase. `duckdb-go` supplies
  a `database/sql` driver but brings cgo, C/C++ linking and platform-specific native
  libraries through its bindings. That is an inappropriate default engine dependency here.
  CLI startup and disk costs are accepted for periodic local diagnostics, not per-request
  ingestion. A future embedded driver can implement the same interface.
- Default off. No new always-on telemetry counters; all new metrics state is allocated at
  construction only when enabled. The off hot path is a nil-state branch, no allocation,
  clock read, atomics, snapshot work, export goroutine, socket, file or child process.
  Existing correctness/audit state and Stats APIs remain intact. In particular the persisted
  OCC discard count/log/outbox must operate with telemetry off; it is not optional metrics.
  Existing runtime atomic counters (dropped/refused/badFrames/abandoned/unresolved) remain
  always-on single atomic increments on their existing exceptional paths. Do not add new
  always-on counters or turn loop-owned counters into atomics.
- No histograms in v1. Counts, in-flight gauges, and HTTP duration sum/count give useful
  throughput and mean latency at fixed cost. Percentiles are explicitly unavailable.
- One configured adapter per process. Fan-out belongs in the Collector; changing an
  adapter requires restart. No hot reload, metric-driven control logic, raw-key metrics,
  tracing, dashboards or public stats API in this phase.

The official [Go exporter documentation](https://opentelemetry.io/docs/languages/go/exporters/)
describes the SDK's binary-protobuf HTTP exporter; the
[OTLP specification](https://opentelemetry.io/docs/specs/otlp/) also defines JSON HTTP.
The [DuckDB Go client](https://duckdb.org/docs/current/clients/go/overview) documents its
cgo cost, while the [CLI documentation](https://duckdb.org/docs/2.0/clients/cli/overview)
describes a standalone executable and noninteractive SQL input. These are design inputs,
not proof that any implementation or image combination has been tested in this phase.

## 2. Architecture and exact port signatures

All proposed paths below are inside the existing root Go module. Existing module/toolchain
pins (`go 1.26.0`, `go1.26.11`, rapid test-only) and dependency gates remain unchanged.

| Package/path | Responsibility / permitted dependencies |
|---|---|
| `internal/metrics` | Plain value types, catalogue, validation, Source and Collector interfaces; stdlib only; safe for sim-reachable packages |
| Existing component packages | Config-gated private metrics state and Snapshot methods; import metrics, never telemetry |
| `internal/noderuntime/metrics.go` | Marshal owner-loop snapshots through existing Inspect; combine runtime, node children and HTTP sources |
| `internal/telemetry` | Runner, config, registry, adapter interface and implementations; stdlib plus metrics; never imported by protocol core |
| `cmd/objnode` | Parse config, enable component state, assemble collector/adapter/runner and own shutdown |
| `deploy/observability/` | Collector and Prometheus configs, sample objnode JSON and image lock record |
| `compose.observability.yaml` | Isolated opt-in Docker services and dev-test network attachment |
| `internal/telemetry/adaptertest` | Reusable contract test harness; testing dependency only |
| `test/observability` | One real objnode → Collector → Prometheus integration test |

Dependency flow: components → metrics ← telemetry; objnode → noderuntime + telemetry.
The metrics port is a Go interface, not a required listening TCP port. Only the selected
Prometheus adapter opens a metrics listener. OTel sends outbound HTTP to Collector:4318.

Proposed `internal/metrics` API (signatures are binding; private implementation is not):

```go
package metrics

import "context"

type Kind uint8
const (
    Counter Kind = iota + 1
    Gauge
)
type Label struct { Name, Value string }
type Descriptor struct {
    Name, Help, Unit string
    Kind Kind
    LabelNames []string
}
type Point struct {
    Name string
    Labels []Label
    Counter uint64 // used only for Counter descriptors
    Gauge uint64   // used only for Gauge descriptors; v1 gauges are nonnegative integers
}
type Snapshot struct {
    Component string
    Points []Point
}
type Source interface { Snapshot() Snapshot }
type Batch struct {
    SchemaVersion uint32
    NodeID, Region, BootID string
    Sequence uint64
    StartUnixNano, TimeUnixNano int64
    Snapshots []Snapshot
}
type Collector interface {
    Collect(context.Context) (Batch, error)
}
func Catalogue() []Descriptor
func Validate(Batch) error
```

Catalogue returns an immutable-by-copy sorted list, independent of adapters. Each descriptor
name is globally unique and prefixed `objstore_<component>_`; counters end `_total`, gauges
do not. Units are `1`, `By`, `ns`. Keep uint64 exact in snapshots and files; Prometheus storage and OTLP doubles can lose integer precision above 2^53. OTLP uses signed
`asInt` where representable, otherwise `asDouble`; never silently wrap uint64 to int64.

Every component owns its metrics state, not an exporter callback. Add `MetricsEnabled bool`
to the relevant config types; no global singleton. Proposed methods:

```go
func (*node.Node) Snapshot() metrics.Snapshot
func (*gossip.Node) Snapshot() metrics.Snapshot
func (*storage.Node) Snapshot() metrics.Snapshot
func (*merkle.Worker) Snapshot() metrics.Snapshot
func (*occ.Store) Snapshot() metrics.Snapshot
func (*occ.Node) Snapshot() metrics.Snapshot
func (*saga.Engine) Snapshot() metrics.Snapshot
func (*noderuntime.Runtime) Snapshot() metrics.Snapshot
func (*httpapi.Server) Snapshot() metrics.Snapshot
func (*node.Node) MetricSources() []metrics.Source
func (*noderuntime.Runtime) MetricsCollector(extra ...metrics.Source) metrics.Collector
```

Disabled sources return their component name and no points. `MetricSources` lists node
itself and its child components in a fixed order; it is only called on the owner loop.
Merkle uses Worker for its metrics; `Tree.Snapshot() *Tree` remains a structural copy API.
Aggregate per-bucket trees into Worker/node state without bucket labels or scanning trees.
Saga's existing `saga.Snapshot` resume type is untouched; an Engine method can coexist.
OCC Node combines its Store snapshot with replication counters and is the registered source;
do not separately register Store and double-export its series. Storage/node similarly
aggregate all codec and Merkle worker instances into one source per component.

Erasure Codec is currently immutable and used by pure callers. Preserve `erasure.New` and
that guarantee. Add an optional owner-confined facade with the same operation signatures:

```go
type MeteredCodec struct { /* private codec and metrics state */ }
func NewMetered(k, m int, enabled bool) (*MeteredCodec, error)
func (*MeteredCodec) K() int
func (*MeteredCodec) M() int
func (*MeteredCodec) N() int
func (*MeteredCodec) ShardSize(dataLen int) int
func (*MeteredCodec) Split(data []byte) [][]byte
func (*MeteredCodec) Encode(shards [][]byte) error
func (*MeteredCodec) Verify(shards [][]byte) error
func (*MeteredCodec) Reconstruct(shards [][]byte) error
func (*MeteredCodec) ReconstructData(shards [][]byte) error
func (*MeteredCodec) Join(shards [][]byte, size int) ([]byte, error)
func (*MeteredCodec) Snapshot() metrics.Snapshot
```

These mirror the existing Codec operations; only Encode, Reconstruct and ReconstructData
increment operation metrics (the last two share the reconstruct label). Add
`storage.StripeCodec` with the above operation methods except Snapshot. Change private
stripe helpers to accept it, retaining the existing exported EncodeStripe/DecodeStripe
functions as wrappers so existing function types stay compatible. Add measured entry points:

```go
func EncodeStripeWithCodec(codec StripeCodec, v VersionID, stripe uint32, data []byte) ([][]byte, error)
func DecodeStripeWithCodec(codec StripeCodec, v VersionID, stripe uint32, dataLen int, blobs [][]byte, want [][32]byte) ([]byte, []int, error)
```

Wire both normal storage operations and node inventory repair through these paths. Storage
and node own their facades and roll totals into a bounded node-level erasure Source; retire
facades by folding counts into lifetime totals, not retaining every historical layout.
No duplicate export per codec and no layout label. Pure callers need not adopt the facade.
Resetting a cached codec must not reset aggregate counters. Reconstructed-shard count covers
only shards actually restored (data-only reconstruction does not count parity erasures).

### Ownership, cost and reset rules

Protocol methods and Snapshot execute only on the existing node loop. `MetricsCollector`
uses **one** bounded `Runtime.Inspect(ctx, fn)` per batch to copy all core sources; it then
samples HTTP/runtime atomic gauges outside the loop. Sources never traverse unbounded
object maps or read disk/network: maintain gauges at state transitions and use O(1) map
lengths where available. Core snapshots are mutually consistent at that loop turn;
HTTP/runtime observations can be slightly later. A batch is not a distributed transaction.
No atomics/mutexes/goroutines or wall-clock reads enter deterministic packages. Snapshot
allocation is O(fixed catalogue cardinality); returned slices are deep copies. The real
HTTP layer may use atomic counters and a bounded label array, never a label map per path.

Do not use the generic Inspect cancellation path unmodified if it increments abandoned
business-call counters for a canceled metrics read. Add a dedicated read-only bounded
request path or exempt metrics inspection from that counter, with a focused test.
On collection timeout discard the entire batch, report failure and continue next tick;
no overlapping inspection requests and no partial zero-filled snapshots. A cancellation
that occurs after enqueue leaves at most one bounded pending read; do not enqueue another
until it is consumed or runtime shutdown drains it. Tests prove
canceled queued inspections do not accumulate indefinitely behind a stalled loop.

Runner supplies wall timestamps, start time, sequence and a runtime-generated BootID;
these are not protocol state and never consume env.Rand. All ordinary telemetry counters
start at zero for a component lifetime and saturate at uint64 max. A node restart resets
them with a new BootID/start time. Metrics are excluded from protocol persistence and state
hashes except pre-existing state, including Stats fields already encoded by components and durable audit data. Enable/disable is fixed for a lifetime.

For OCC, expose `objstore_occ_lww_discards_total` as the **increase in the authoritative
persisted counter since successful store open**, not log callbacks (outbox replay would
double-count). Also export `objstore_occ_lww_discards_persisted` gauge for the lifetime
persisted total. Neither causal supersession nor a rejected conditional write is a LWW
loss. Count after successful persistence only; rollback, repeated merge and log replay
cannot increment telemetry. The count remains per-node observation, not globally distinct
lost writes; sums over replicas must be described accordingly.

## 3. Metric catalogue

Names below omit the common `objstore_` prefix. Braces denote finite label domains, not
raw values from requests. These are required series, not a promise to export every private
Stats field. Export zero-valued fixed series on the first batch. Terminal failures count
once; attempts are separately named when retries exist. All counters are cumulative.

| Owner | Counters (`_total`) | Gauges | Increment/sample boundary |
|---|---|---|---|
| node | `node_operations_total{operation=put,get,delete,other;outcome=ok,error}`, `node_rebuilds_total{outcome}`, `node_rebuilt_bytes_total`, `node_scrubbed_total`, `node_scrub_corrupt_total`, `node_collected_total{reason=obsolete,loser,orphan}`, `node_lost_declared_total`, `node_quota_rejections_total`, `node_migrations_ended_total{outcome}` | `node_ready`, `node_operations_in_flight`, `node_rebuilds_in_flight`, `node_metadata_records` | Public operation completion exactly once, repair/scrub/GC transitions; readiness as 0/1 |
| runtime | `runtime_datagrams_dropped_total`, `runtime_connections_refused_total`, `runtime_bad_frames_total`, `runtime_calls_abandoned_total`, `runtime_unresolved_total`, `runtime_loop_panics_total` | `runtime_control_queue_depth`, `runtime_control_queue_capacity`, `runtime_datagram_queue_depth`, `runtime_datagram_queue_capacity`, `runtime_connections`, `runtime_connection_capacity`, `runtime_running`, `runtime_goroutines` | Existing exceptional-path counters; queue len/cap; protected connection count; Go goroutine sample only while collecting |
| http | `http_requests_total{operation;status_class=2xx,3xx,4xx,5xx,other}`, `http_request_duration_nanoseconds_total{operation}`, `http_request_duration_count_total{operation}`, `http_request_bytes_total`, `http_response_bytes_total` | `http_requests_in_flight` | At handler return; implicit status 200 and errors/cancellation tested; bytes actually consumed/written, not declared Content-Length |
| gossip | `gossip_probes_total{kind=direct,indirect;outcome=ok,timeout,error}`, `gossip_membership_transitions_total{state=alive,suspect,dead,left}`, `gossip_syncs_total{outcome}`, `gossip_malformed_total` | `gossip_members{state}`, `gossip_updates_queued`, `gossip_probes_in_flight` | One completed probe/sync attempt, actual membership transition, decoder rejection; retries are attempts |
| storage | `storage_shard_operations_total{operation=put,get,delete;outcome=ok,not_found,corrupt,error}`, `storage_shard_bytes_total{direction=read,write}`, `storage_rpc_timeouts_total` | `storage_requests_in_flight`, `storage_local_shards`, `storage_local_bytes` | Shard completion and actual accepted bytes; inventory gauges maintained incrementally, initialized by existing startup inventory work |
| erasure | `erasure_operations_total{operation=encode,reconstruct;outcome=ok,error}`, `erasure_input_bytes_total{operation}`, `erasure_output_bytes_total{operation}`, `erasure_reconstructed_shards_total` | `erasure_operations_in_flight` | Facade call entry/exit; bytes only on successful operations; missing shards counted before reconstruction |
| merkle | `merkle_rounds_total{outcome=equal,repaired,error}`, `merkle_hashes_compared_total`, `merkle_differences_total`, `merkle_repairs_total{outcome}`, `merkle_budget_exhaustions_total` | `merkle_rounds_in_flight`, `merkle_entries` | Worker/session completion and comparison/repair events; no full-tree scan |
| occ | `occ_writes_total{outcome=ok,precondition_failed,error}`, `occ_merges_total{outcome=changed,unchanged,error}`, `occ_replication_rounds_total{outcome}`, `occ_replication_errors_total{kind=network,disk,malformed}`, `occ_lww_discards_total`, `occ_persist_errors_total` | `occ_records`, `occ_lww_discards_persisted`, `occ_discard_outbox_depth` | Store transaction completion and committed discard deltas; includes local losing writes and incoming losing versions |
| saga | `saga_starts_total`, `saga_completions_total{outcome=completed,compensated,failed}`, `saga_step_attempts_total`, `saga_step_failures_total`, `saga_compensations_total`, `saga_compensation_failures_total`, `saga_retries_total`, `saga_recoveries_total` | `saga_active`, `saga_suspended` | Aggregate existing step statistics; terminal state once per execution, recovery distinguished from fresh start; no saga ID labels |
| telemetry runner | `telemetry_collections_total{outcome}`, `telemetry_exports_total{outcome=ok,error,partial}`, `telemetry_skipped_ticks_total` | `telemetry_last_success_unixtime`, `telemetry_collection_duration_nanoseconds`, `telemetry_export_duration_nanoseconds` | Previous cycle's state included in next batch; failures also rate-limited to stderr, so exporter failure is visible without exporter success |

HTTP operation domain: `health,members,bucket,object,tag,acl,quota,migration,unknown`.
Unknown method/path maps to unknown, never raw URL. Use the routing table for classification.
`outcome` without an expanded domain above means `ok,error`.
Common labels are `node_id` and `region` (bounded configured identities, not peer IDs).
BootID is stored in files/OTLP resource metadata, not a Prometheus label; start timestamps
and reset semantics handle restarts without a new series per boot. Never label by object,
bucket, tenant, user, saga, version, peer address, error message or arbitrary definition.
Keep detailed losses in the existing bounded DiscardTail and audit logs, not a new export.

The implementation freezes names, help, units and allowed labels in `metrics.Catalogue`.
Validate uniqueness, kind/value consistency and known labels/values before publication.
All v1 gauges are nonnegative integers (including nanoseconds and Unix seconds); core
metrics contain no float expressions, preserving detlint. Only adapter encoding converts
to floating-point formats when a destination requires it. Maximum
2,048 points and 1 MiB encoded batch per node; fail a batch that exceeds either bound.
Cross-component double counting is intentional only where names describe different work
(e.g. HTTP request vs shard RPC); do not sum them as object throughput.

## 4. Config, registry and adapter lifecycle

Proposed `internal/telemetry` public API:

```go
package telemetry

import (
    "context"
    "encoding/json"
    "github.com/nickstrad/object-store/internal/metrics"
)
type Config struct {
    Enabled bool `json:"enabled"`
    Adapter string `json:"adapter"`
    Interval string `json:"interval"`
    Timeout string `json:"timeout"`
    Settings json.RawMessage `json:"settings"`
}
type Identity struct { NodeID, Region, BootID string; StartUnixNano int64 }
type Adapter interface {
    Publish(context.Context, metrics.Batch) error
    Shutdown(context.Context) error
}
type Factory func(context.Context, Identity, json.RawMessage) (Adapter, error)
func Open(context.Context, Config, Identity) (Adapter, error)
func NewRunner(Config, metrics.Collector, Adapter, func(string, ...any)) (*Runner, error)
func (*Runner) Run(context.Context) error
func (*Runner) Shutdown(context.Context) error
// Exported sentinels, each initialized with errors.New in implementation.
var ErrConfig, ErrClosed, ErrPartial error
// Private normalized config uses time.Duration after parsing strings.
```

Private normalized durations use time.Duration. Open validates names against a static, explicit factory map in
`registry.go`. Each built-in is one new implementation file (`otel.go`, `prometheus.go`,
`csv.go`, `duckdb.go`) plus one map entry; tests may be separate files. No init registration,
switch in objnode, central per-adapter config union, or component mapping per adapter.
Factories decode their own Settings strictly (unknown keys and trailing JSON rejected).
All adapters consume the same catalogue/batch. A new adapter does not change objnode,
components, runner or the config schema. Runtime implementation details may use helpers.

Default: enabled=false, adapter=otel, interval=10s, timeout=2s. Interval range 100ms–1h;
timeout >0 and < interval. Startup validates adapter names **even when disabled** so an
unknown name is always a config error. Disabled mode never calls a factory or Collect;
adapter-specific external resources are not checked/opened until enabled. Unknown common
fields and malformed common values fail regardless of toggle. Settings must always be a JSON object;
strict per-adapter field validation runs only when enabled, without opening resources. Nil adapter in disabled
mode is an intentional result from Open, and objnode constructs no runner.

Example addition to objnode's existing JSON config:

```json
{
  "observability": {
    "enabled": true,
    "adapter": "otel",
    "interval": "10s",
    "timeout": "2s",
    "settings": {"endpoint": "http://otel-collector:4318/v1/metrics", "insecure": true}
  }
}
```

Add `-observability`, `-observability-adapter`, `-observability-interval`,
`-observability-timeout`, `-observability-settings` (JSON object). Explicit CLI flags override
only corresponding JSON fields, including explicit false. Adapter/settings stay separate:
changing adapter with stale settings fails strict factory validation instead of guessing.
Do not reinterpret existing numeric duration JSON fields; these new durations use strings.

| Adapter name | Settings and defaults |
|---|---|
| `otel` | endpoint required, full metrics URL; insecure=false; ca_file optional; headers_file optional (JSON string map); response limit 64 KiB; plaintext permitted only with explicit insecure=true; credentials never logged |
| `prometheus` | listen=`127.0.0.1:9464`, path=`/metrics`, stale_after=`30s` (must be >=2 intervals); read/write/header timeouts bounded; Docker sample binds `0.0.0.0:9464` inside isolated network |
| `csv` | directory required; max_file_bytes=16MiB, keep_files=8, sync=true; startup requires a private writable directory |
| `duckdb` | path required; executable=`/usr/local/bin/duckdb`; retention=`24h`; max_database_bytes=256MiB; batch process deadline uses common timeout |

Startup order: validate everything → construct components/state → open selected adapter
(bind/check local sink) → start runtime and HTTP → run collector. Roll back opened resources
on any failure; readiness callback fires after local setup. Remote Collector outage does not
block node readiness: exports fail with bounded retries and logs. Invalid URL, TLS files,
occupied listen address, CSV permissions or unavailable DuckDB executable are startup errors.

One runner goroutine owns collection and Publish calls, serially. Initial collection at
startup, then one tick per interval. Collect and Publish each receive a context with timeout;
missed ticks coalesce, never spawn work or build a backlog. Bound logs to one per failure
class per minute with suppressed-count reporting. Runner retains no historical queue.
Adapter errors do not fail object writes or stop the node. Malformed local batches are
reported and dropped. Publish may not retain mutable references after return; a pull adapter
must copy into its bounded immutable cache. Concurrent scrapes never call Collect.

Shutdown: cancel/join collection first; drain HTTP and runtime using the existing common
shutdown budget; shut down adapter with remaining budget, joining errors. Do not do a final
collection after stopping the node loop. Last already-collected batch may be flushed if time
remains. Idempotent Shutdown; Publish after shutdown returns ErrClosed; cancellation must
close response bodies and terminate/reap processes; local filesystem operations must be
bounded to the small batch and ordinary local disk (network filesystems are unsupported).
Shutdown must close files and leave no runner goroutine behind.

## 5. First adapter: OTel → Collector → Prometheus

OTel adapter sends cumulative monotonic sums and gauges in OTLP JSON, no SDK globals or
background reader. Resource identifies `service.name=objstore`, `service.instance.id=node_id`,
region and boot ID; data-point attributes include `node_id` and `region` for stable queries.
All mappings come from catalogue Kind/Unit, not component-specific switches. Preserve batch
start/sample times and cumulative temporality. JSON uses lowerCamelCase fields, numeric enum
values, and decimal-string 64-bit values. POST the full configured `/v1/metrics` URL with
`Content-Type: application/json`. These encoding constraints follow the
[OTLP HTTP specification](https://opentelemetry.io/docs/specs/otlp/).

Honor context and response-size bounds. A 200 with rejectedDataPoints is ErrPartial and
is never retried; warning text with zero rejected points is logged. Malformed successful
response is an error. Retry only transport failures and HTTP 429/502/503/504, at most once
within the Publish deadline, with capped exponential delay and Retry-After respected (skip
retry if it cannot fit). Other statuses drop the sample and report failure. Next tick sends
fresh cumulative values, so transient loss does not lose counter history. No unbounded
retry queue, persistent spool, compression, gRPC or trace/log export in this implementation.

Build these files in chunk A1, not in this planning commit:

`deploy/observability/otel-collector.yaml`:

```yaml
receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
exporters:
  prometheus:
    endpoint: 0.0.0.0:9464
    send_timestamps: false
    metric_expiration: 30s
    translation_strategy: UnderscoreEscapingWithoutSuffixes
service:
  pipelines:
    metrics:
      receivers: [otlp]
      exporters: [prometheus]
```

`deploy/observability/prometheus.yaml`:

```yaml
global:
  scrape_interval: 1s
  scrape_timeout: 1s
scrape_configs:
  - job_name: objstore-otel
    honor_labels: true
    static_configs:
      - targets: [otel-collector:9464]
```

This is **OTLP receiver → Collector prometheus exporter → Prometheus scrape**, not remote
write and not Prometheus's direct OTLP ingestion endpoint. No batch processor needed for
this small local pipeline. Exact metric spelling is preserved using the translation setting;
the real integration assertion must verify it. The Collector exporter exposes `/metrics`
and documents naming translation and expiry in its
[configuration reference](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/exporter/prometheusexporter/README.md).

`compose.observability.yaml` adds profile `observability`: `otel-collector` (official contrib
image containing that exporter), `prometheus` (official image, bounded 24h/256MB retention),
and a one-node `obs-node` with k=1,m=0, private data volume, HTTP :8080 and the sample JSON.
Mount configs read-only. Give dev the same private network for integration checks. Default
ports stay internal; optional Prometheus UI maps `127.0.0.1:9090:9090`. No OTLP/public
metrics host publishing by default. Set `COMPOSE_PROJECT_NAME` uniquely per worktree/test
because base compose fixes a shared project name. Do not remove shared Go-cache volumes.

A1 selects compatible released images, records tags **and immutable digests** in
`deploy/observability/images.lock`, and uses those digests in Compose. Validate the config
with that Collector binary. Do not guess digests in planning or use floating latest.
OTel exporter settings above are requirements for selecting the pinned version.

One integration test, `TestObjnodeOTelPrometheus` in `test/observability`, is enabled by
`OBS_INTEGRATION=1` and executed in dev through
`agent_work/10-observability/check-otel.sh` (to be built). This script builds the current
objnode in Docker, starts isolated services, waits on bounded readiness checks, runs the
test, saves logs/query JSON and tears down only its own services on all exits. Overall
post-image-start deadline 60s, CI job deadline 5 minutes; no infinite retries or fixed long
sleep. A missing service or skipped test when enabled is failure.

Test makes successful `GET /_health` requests (the existing route), then polls `/api/v1/query` on Prometheus for
`objstore_http_requests_total{node_id="obs-node:7946",operation="health",status_class="2xx"}`.
Require success JSON, a nonempty vector, positive value, then a larger value after more
requests; assert an in-flight gauge is also present. Collector internal metrics or `up=1`
alone cannot pass. Archive the observed names and labels. Local command after implementation:

```sh
COMPOSE_PROJECT_NAME=objstore-obs-check agent_work/10-observability/check-otel.sh
```

A1 initially proves this same pipeline with an in-memory Source/Collector fixture through
the port (unit/contract test, not a second Docker integration). Once Opus's component and
config wiring arrives, Astra finishes the one real objnode integration. No other adapter
is built before the OTel port fixture passes; full production proof depends on that wiring.

## 6. Other adapters and DuckDB runtime interface

**Prometheus:** stdlib HTTP text exposition 0.0.4 from the last successfully validated batch.
Publish atomically replaces the immutable cache. Escape HELP/label text, emit HELP/TYPE once,
sort families/labels, preserve counter/gauge distinctions, avoid double `_total` suffixes.
Before first batch and after stale_after, return 503 rather than plausible zeros; healthy
cache scrapes return 200. Bounded concurrent handlers and timeouts, independent listener
from the object API. Scrapes never take a node-loop lock or perform snapshot collection.

**CSV:** periodic Publish writes long-form rows using encoding/csv. Columns:
`schema_version,node_id,region,boot_id,sequence,start_unix_nano,time_unix_nano,component,name,kind,unit,labels_json,counter_uint64,gauge_uint64`.
Exactly one value column populated; sorted labels encoded as JSON; decimal integers remain
exact. One complete batch per rotated file segment, never split a batch across segments;
reject a batch bigger than max_file_bytes. Write to a private temp file, flush/check error,
Sync when configured, then rename to a finalized unique filename; crash leaves only a temp
file that readers ignore. Rotate/delete finalized files beyond keep_files only after a
successful new segment. No append into half-written files; sequence/boot disambiguate runs.
Directory ownership is per node, restrictive modes, no path derived from metric labels.
This trades small-file overhead for unambiguous complete snapshots and bounded storage.

**DuckDB:** unlike CSV, Publish must write a queryable database during runtime. Concrete
v1 is a subprocess implementation, not “CSV export with a suggested import command.”
The following interface lives in `internal/telemetry/duckdb.go`; tests inject a fake:

```go
type DuckDBRow struct {
    SchemaVersion uint32
    NodeID, Region, BootID string
    Sequence uint64
    StartUnixNano, TimeUnixNano int64
    Component, Name, Kind, Unit, LabelsJSON string
    Counter *uint64
    Gauge *uint64
}
type DuckDBRuntime interface {
    WriteBatch(context.Context, []DuckDBRow) error
    Close(context.Context) error
}
type DuckDBRuntimeConfig struct {
    Path, Executable string
    Retention time.Duration
    MaxDatabaseBytes int64
}
func NewDuckDBRuntime(context.Context, DuckDBRuntimeConfig) (DuckDBRuntime, error)
```

One serial writer per database, held via exclusive lock file; startup rejects concurrent
writers, unwritable path, wrong executable version or schema. `WriteBatch` runs a single
`exec.CommandContext` without a shell, feeds generated SQL via stdin to the pinned CLI,
uses bail-on-error and no-init mode, caps stderr/stdout, waits/reaps on every path. Timeout
kills the process; a process killed mid-transaction leaves rollback/recovery to DuckDB.
No extension auto-install, network queries or arbitrary configured SQL. Encode variable string literals as hex bytes decoded by fixed SQL expressions, so
newlines cannot introduce CLI dot commands; reject NULs; table/column names are constants. Tests include hostile
node IDs, paths and labels. Process arguments and SQL are never interpolated into a shell.

Schema `snapshots_v1` uses VARCHAR identities, UBIGINT counter/sequence, BIGINT timestamps,
UBIGINT gauge and labels JSON stored as VARCHAR; CHECK exactly one numeric value is set.
In a transaction insert all rows for a batch and prune rows older than retention. Key is
(node_id,boot_id,sequence,component,name,labels_json); duplicate Publish is idempotent via
conflict-ignore. Schema version change needs an explicit migration, never silent overwrite.
Use SQL transaction completion and process exit 0 as acknowledgement. On uncertain commit,
replaying the same batch is safe; ordinary runner still proceeds to the next sample.

Pin CLI version and download SHA256 in an optional Docker `dev-observability` target based
on the normal dev image. Installing the executable adds native binary/image size and
process startup latency but no Go modules or cgo. Record measured image delta and per-flush
cost in build notes. Disk cap includes WAL; reaching it stops new writes with a clear error,
not deletion of the user's database. Retention does not guarantee physical file shrink;
no automatic VACUUM in the hot path. Close releases ownership after any process is reaped.
Read-only queries should run between flushes or after shutdown; v1 promises no concurrent
multi-process writer/query service. Future database/sql implementation can use transactions
and prepared statements behind this same interface without changing snapshots or selector.

## 7. Shared contracts and test strategy

`adaptertest.Run(t *testing.T, makeFixture func(*testing.T) Fixture)` drives every adapter
with one frozen catalogue/batch fixture and a transport-specific readback function. Proposed:

```go
type Fixture struct {
    Adapter telemetry.Adapter
    Readback func(context.Context) (metrics.Batch, error)
    FailNext func()
}
func Run(t *testing.T, makeFixture func(*testing.T) Fixture)
```

Readback normalizes exported numeric values/labels; formats lacking boot/sequence metadata
compare only the metric projection. File adapters additionally assert exact metadata.
OTel uses httptest capture, Prometheus uses HTTP scrape, CSV reads finalized files, DuckDB
uses a fake runtime in fast contracts. Unit tests cover actual CLI command construction;
a short manual Docker smoke inserts/queries/reopens the real DB at chunk O4 acceptance.
That smoke is not another CI service integration and is explicitly recorded as such.

Shared cases: zero/positive/increasing counters, gauge up/down, zero-valued series, stable
labels/help/units, uint64 >2^53, escaping, deep-copy isolation, duplicates, process reset,
unknown/duplicate metric and label rejection, wrong-kind values, oversized batch, canceled
Publish, sink failure, recovery, idempotent Shutdown and no resource leaks. Duplicate
semantics are format-appropriate: cumulative values replace rather than add; file rows
carry sequence so replay is identifiable; DuckDB deduplicates. Each adapter also has its
protocol-specific malformed-response, stale-cache, disk-full or killed-child tests.

Component unit tests cause real transitions and assert counters: HTTP 2xx/4xx/5xx and
cancellation; SWIM timeout then recovery; corrupt shard and reconstruction; Merkle equal
and repaired rounds; OCC concurrent loser, repeated merge, persist rollback and outbox
replay; saga retry, compensation and recovery. Existing fake envs and small pinned fixtures
suffice. Assert on/off produces identical protocol results and persisted data, preserves
LWW logs, and disabled runtime makes **zero Collect and factory calls**. Test startup
failure rollback, stalled inspection and shutdown under export failure. Use synctest/fakes
for time; focused race checks for new runtime/HTTP/cache code in Docker at acceptance.

CI stays fast: existing `agent_work/gates/ci-fast.sh` (G0/G1/G6), these unit/contract tests
under `go test -short`, and **one** separate bounded OTel→Prometheus integration invocation.
Reuse its result across chunks at a given commit; never enable long fuzz, large DST sweeps
or full-gate jobs on push. Keep existing fixed-seed smoke, coverage floors, dependency and
determinism lint. Extend package classification for metrics (deterministic) and telemetry
(real-runtime only), never exempt all new packages from lint. Add an import-boundary test
that sim-reachable packages cannot import telemetry/SDK packages.

Historical full local landing authority in root PLAN/STATE is not silently removed by this
plan; if still required by the conductor, it remains an on-demand local gate, never a CI
addition. This phase's new acceptance evidence is deliberately bounded. Planning itself
requires only document/diff/link review, not running product gates on unchanged Go code.

## 8. Chunk order, ownership and acceptance

Nick's split overrides historical product-build seats: **Astra builds the core port,
selector and OTel adapter/Collector path; Opus builds component snapshots, objnode plumbing,
Prometheus, CSV and DuckDB; Neon handles stuck cleanup and the final docs/polish pass.**
Each chunk is a fresh headless run in its own worktree, based on accepted predecessors.
No builder owns concurrent edits to a shared file. At most three active workers; check usage
pools before dispatch. Record brief, permission profile (branch-local build, no publish),
checks, exit status, outputs and next action at each STATE checkpoint. Do not reuse a
completed agent for the next chunk. No build agents are launched by this planning deliverable.

| Chunk / owner | Dependencies and concrete deliverable | Acceptance before completion |
|---|---|---|
| A0 / Astra | Freeze metrics value model/catalogue, selector, runner, shared contract harness; no real adapter dependency yet | Dummy factory proves name selection, unknown-name error even off, no factory/Collect when off, cancellation/deep copies, bounded scheduling; stdlib/import/detlint gates pass |
| A1 / Astra | A0; OTel implementation first, exact Collector/Prom configs, isolated Compose, image pins, integration script skeleton | Port fixture emitted through OTLP; golden JSON and error/retry/partial tests pass; pinned Collector config validates and manual fixture scrape works; no fake fixture accepted as objnode completion |
| O1 / Opus | A1; node/gossip/storage/erasure/merkle/occ/saga config-gated snapshots and catalogue mapping | Every component's transitions tested; LWW persistence/replay/off semantics proven; no immutable Codec or Tree.Snapshot break; deterministic results unchanged |
| O2 / Opus | O1; runtime safe collection, HTTP metrics, objnode flags/config/lifecycle | Actual objnode produces batches; on/off, precedence, bad config, startup rollback, snapshot timeout and shutdown tests pass; no race or callback into core from exporters |
| A2 / Astra | O2 + A1; finish real objnode→Collector→Prometheus proof, CI bounded integration hook | Required metric increases in Prometheus query JSON, gauge present, names/labels verified; isolated Docker cleanup and enabled-test skip detection; fast CI remains bounded |
| O3 / Opus | A2; direct Prometheus and CSV adapters (separate logical commits) | Shared contract suite and scrape staleness/escaping/file rotation/crash tests pass; adding each touches only implementation/test files and registry entry, no component changes |
| O4 / Opus | A2; DuckDB runtime and adapter, pinned optional Docker target | Shared contract with fake; real Docker insert/query/reopen smoke; cancellation, quoting, lock, schema and duplicate handling; dependency/image/startup cost recorded |
| N1 / Neon | A2,O3,O4; cleanup and final tweak pass: README/help/book/C4 match actual behavior | Docs show Docker commands, all adapter settings, off semantics and limitations; prose provenance passes; commands and final fast lane + single integration verified on final tree |

O3/O4 may run independently after A2; registry entries are small but must be rebased and
reviewed serially. O1/O2 remain separate runs despite same owner. Astra owns port changes;
Opus proposes contract changes before building against them, rather than silently forking
interfaces. Neon can take a stuck cleanup item only as an explicit conductor dispatch,
with a fresh brief and an independent reviewer; no silent seat substitutions at quota limits.

Review: a fresh cross-company reviewer for Astra builds (Claude seat selected by conductor);
**Fable only reviews Opus builds**, per the supplied global Opus exception, which supersedes
the old project's Neon-review default. Neon final changes receive fresh independent review.
PASS or closed NITS required; one repair round, then second FAIL/standoff to remaining-family
top-seat arbitration, subject to Nick's gates. Do not invent a Neon model identifier; use
the verified project route. Finish chains automatically, verify launches after ~15s,
write exit files under `/root/runs`, and diagnose/relaunch a failed run once only. Check all
three usage gauges before dispatch and stop dispatch if two pools are limited.

Every chunk updates STATE and assesses durable findings: project findings go in
`docs/knowledge/`, indexed reusable scripts in `work-scripts/`, separate commits when needed.
No project findings go into VM kb. Those build-stage artifacts are future work; the planning
commit deliberately contains only this PLAN and STATE. Keep the phase branch unmerged until
reviews/gates pass; no push, merge, publication or creative verdict is authorized by this
planning task. Neon prepares publish-ready documentation; Nick retains publication approval.

## 9. Risks, limits and finish line

| Risk | Decision / verification |
|---|---|
| Hand-written OTLP drift | Narrow sums/gauges subset; source-linked encoding rules, golden requests, real pinned Collector proof; SDK migration behind port if scope grows |
| Snapshot stalls the owner loop | Fixed catalogue/cardinality, no scans/I/O, one timed inspection; benchmark collection at large existing node state and assert work independent of object count |
| Off toggle breaks audit/tests | Preserve existing Stats and persisted LWW machinery; gate only new metrics; explicit off/replay tests |
| Gauges misleading during failure/restart | Whole-batch failure, stale expiry, start/boot semantics and per-node labels; never fabricate zeros after collection failure |
| Export overload feeds back into writes | One worker, no queue, small time/payload limits, dropped observations and rate-limited logs; no component callback from sink |
| DuckDB disk/process overhead | Optional native executable/image, one process per batch, retention and cap, no cgo; report actual cost and leave embedded driver as later option |
| Cardinality/secret leakage | Frozen label enums; no object/bucket/tenant/error strings; credentials only in protected adapter settings; local Docker ports |
| Double-counted LWW losses or retries | Count persisted delta, not log events; distinguish attempts/terminal completions and replicas; repeated-merge/recovery tests |
| Codec integration bypasses metrics | Cover normal writes/reads and node inventory repair; preserve pure Codec and stripe wrappers; test aggregate totals when facades retire |
| Quota/cutoff interrupts phase | STATE names exact branch, SHA, logs, live PID/exit path and next chunk; no transfer of unfinished work without conductor instruction |

Finish means all nine components expose tested snapshots, off mode is inert, all four
adapter names work, unknown names fail startup, adding a fifth needs no component edits,
and a metric originating in **objnode** is queryable in local Prometheus through Collector.
All specified acceptance evidence and Neon polish must be complete; a successful fixture
export or a plan alone is not completion of the implementation phase.
