# Core build verification — A0/A1

Scope: framework-free snapshots/catalogue/validation; config/selector/runner and metrics
port; shared adapter contract; stdlib OTLP JSON adapter; pinned Collector/Prometheus
Compose path. Component Snapshot wiring, objnode observability flags/config, direct
Prometheus/CSV adapters and DuckDB analysis are not implemented here.

## Contract and implementation

- Binding Go signatures in PLAN sections 2 and 4 are implemented except ReadCSVDir,
  which belongs to the CSV seat. No placeholder implementation or factory entries for
  future adapters were added. `otel` is the only currently registered runtime adapter.
- No new Go module dependency. Core has no context or telemetry/framework import.
  Import tests and the expanded detlint roots enforce this. Metrics and telemetry
  are subject to the existing 85% coverage floor in the fast lane.
- Runner performs initial collection, serial collection/export with bounded contexts,
  coalesces ticks, deep-copies and stamps batches, exports previous-cycle self metrics,
  rate-limits failure logs, and cancels/joins before adapter shutdown.
- Contract cases cover zero/increasing counters, gauge up/down, all three units,
  label escaping, values above 2^53, duplicates, new sink lifetime, invalid names/labels/
  kinds/size, canceled Publish, external sink failure and recovery, copy isolation,
  idempotent shutdown, and resource leaks. OTel-specific tests add golden JSON,
  MaxUint64 handling, boot reset metadata, TLS/headers, partial acceptance, malformed
  and oversized responses, bounded retry/recovery/Retry-After, and active shutdown.
- Collector resource attributes default to target_info labels. The implemented scrape
  config drops boot_id; the plan's sample was updated to satisfy its existing no-boot-label
  rule. This retains the specified direct Collector Prometheus exporter mechanism.

## Commands and evidence

- `go build ./...` and `go vet ./...`: passed during build; repeated on final code.
- `COMPOSE_PROJECT_NAME=objstore-obs-core-fast agent_work/gates/ci-fast.sh`: PASS on `55e430b` (clean tree, Docker, G0/G1/G6), archived in
  core-fast-result.json. Metrics coverage 96.6%; telemetry coverage 95.7%. No race/full/fuzz/sweep lane was requested or run.
- `agent_work/10-observability/check-otel.sh --fixture`: real Docker, not a fake-only
  fallback. Collector 0.145.0 validates its configuration. Prometheus 3.9.1 observes
  HTTP requests 7 then 15 and an in-flight gauge of 1; boot_id stored-series count is 0.
  Image digests are in deploy/observability/images.lock and Compose uses those digests.
  Query evidence is in core-fixture-results.json.
- First fast run found encoding/json disallowed in metrics; replaced it with conservative
  deterministic sizing without weakening detlint. The earlier cancellation fixture hung
  until it consumed its HTTP request body; the corrected focused tests pass.

The fast run at `017c0ad` passed G0/G6 and observability tests (metrics 96.6%, telemetry
95.7%) but failed existing saga TestPropTerminalState: rapid reported 20 successful cases,
then its coverage-of-draws assertion found zero retry exhaustion cases. No saga code was
changed. The failure log/result were preserved under /root/runs/objstore-obs-core-fast-saga-failure.*;
the one bounded rerun at `55e430b` PASSED, with unchanged product/tests. Both outcomes
remain recorded; this is not a claim that the existing randomized coverage assertion is
deterministic. No full landing gate was run. Subsequent changes only record this result.

## Handoff

Conductor selects the fresh cross-company reviewer before acceptance/landing. No merge,
push or publication was performed. The default check-otel.sh path and
TestObjnodeOTelPrometheus are prepared but must be executed after O2; they require actual
health request counter growth and an in-flight gauge, and cannot silently skip when enabled.
Opus must use the frozen catalogue/finite label domains, supply the runtime Collector,
reuse one Identity, construct no runner when disabled, and implement settings in each
adapter's own factory. CSV tests additionally require exact uint64 and metadata checks;
float-backed formats can round above 2^53.
