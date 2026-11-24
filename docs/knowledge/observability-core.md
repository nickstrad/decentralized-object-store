# Observability core and OTel transport

The implementation contract is `agent_work/10-observability/PLAN.md`; A0/A1 deliver the
metrics value model, runner and first adapter, not component instrumentation. The build
record is `agent_work/10-observability/CORE-VERIFICATION.md`.

- `metrics.Catalogue()` returns deep copies of 83 descriptors. Point labels contain only
  the descriptor's finite dimensions; node_id and region come from Batch metadata and
  are added by adapters. Identities are nonempty valid UTF-8, bounded to 256 bytes each.
  Component names must match their metric prefixes. Empty snapshots are valid for known
  disabled components; duplicate components/series and unrecognized labels are rejected.
- Core validation must stay detlint-clean. Its byte bound conservatively accounts for
  sixfold JSON string escaping without importing encoding/json (which detlint rejects).
  Adapters must also enforce the 1 MiB bound on their actual wire representation. The
  OTel adapter does this; CSV must enforce it in addition to max_file_bytes.
- `telemetry.Open` validates common config and the adapter name even when off. It returns
  nil without invoking the factory in off mode. Build no runner in objnode when disabled.
  Strict adapter settings are factory-owned. Add an implementation plus a registry.go
  entry; there is no process-global SDK provider or init registration.
- Create one Identity and pass it to both Open and NewRunner. The runtime collector owns
  safe component inspection; Runner supplies all metadata and the telemetry component.
  Collectors must honor their context. Runner remains serial even if one fails to do so,
  but cannot forcibly interrupt synchronous user code. Shutdown cancels and joins before
  closing the sink; an expired shutdown budget can be retried to finish the join.
- OTLP uses asInt decimal strings through MaxInt64 (including exact integers above 2^53),
  then asDouble for the remaining uint64 range. Prometheus still stores doubles. The shared
  adapter contract permits that numeric rounding; CSV-specific tests must assert exact
  uint64 values and all batch metadata. Invoke adaptertest.Run from an external test
  package to avoid an import cycle. Fixture Identity/Batch helpers define the test input.
- The pinned Collector's UnderscoreEscapingWithoutSuffixes preserves catalogue names.
  Resource attributes also appear on target_info even with resource-to-telemetry
  conversion disabled. Prometheus metric relabeling drops boot_id to prevent per-boot
  series; the Docker fixture asserts no stored series has that label. OTLP retains boot
  metadata and start timestamps. Data point node_id/region labels remain queryable.
- `check-otel.sh --fixture` is a bounded manual proof through Runner, OTLP, real Collector
  and real Prometheus. Its default mode is the A2 real-objnode gate, which requires O2
  wiring. Do not pass off a fixture run as objnode completion. Both modes use the same
  pinned Compose services; no host OTLP or metrics ports are published. Each invocation
  gets its own project name and removes only its own services/node volume, retaining the
  shared Go caches. Never reuse a project name belonging to another live checkout.
- In httptest cancellation fixtures, consume the request body before waiting for the
  request context. Otherwise the server may not notice a disconnected client and Close
  can hang; that was a test-fixture defect, not an exporter retry problem.

No runtime DuckDB writer is registered: CSV is the disk flush, and DuckDB is the later
analysis consumer over finalized CSV files. Prometheus, CSV and component wiring remain
O1–O4 responsibilities.
