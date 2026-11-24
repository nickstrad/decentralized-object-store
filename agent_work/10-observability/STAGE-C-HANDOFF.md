# Stage C handoff for the second final pass

Stage C implements O1/O2/O3/O4/A2. Phase readiness remains the final pass's decision;
this handoff does not authorize a push, main merge or publication. See STATE.md for current
verification status and STAGE-C-VERIFICATION.md for the finished acceptance record.

The existing docs/observability.md and README deliberately describe the earlier core-only
checkpoint. They now need to describe the actual delivered behavior:

- All seven protocol sources and runtime/HTTP snapshots are wired, with fixed zero series,
  nil-off state, owner-loop collection, exact finite labels and per-boot cumulative counters.
- Objnode accepts the observability JSON block plus -observability, -observability-adapter,
  -observability-interval, -observability-timeout and -observability-settings. Explicit CLI
  fields override corresponding file fields, including false; names/common values validate
  even off. Off constructs no collector runner or adapter resources.
- Built-ins are otel, prometheus and csv. Factories own strict settings. Prometheus uses
  an independent bounded listener/cache, returns 503 before the first sample or on stale
  data, and requires stale_after at least twice the collection interval. Defaults and CSV
  settings match PLAN. This is separate from the object HTTP API.
- CSV's 14-column schema is in PLAN; files use exact unsigned values, JSON object labels,
  private one-batch atomic segments, retention, and ReadCSVDir. Empty snapshots have no
  rows; a wholly point-free batch is rejected (the runner always adds telemetry points).
  File Sync is not a directory power-loss guarantee; see adapter knowledge for limitations.
- DuckDB is analysis over finalized CSV, with no runtime DuckDB writer/dependency. Optional
  dev-observability Docker target pins CLI 1.5.0 and SHA256 for linux amd64; ordinary default
  builds retain the plain dev image. Measured added image bytes: 21,714,514.
- query-csv.sh consumes a caller directory without SQL/shell path interpolation. queries.sql
  provides exact typed rows, per-boot counter deltas, OCC discards, HTTP rates/mean latency
  and in-flight observations. Percentiles remain unavailable.
- Docker OTel proof: check-otel.sh, with the observability Compose profile, builds a real
  objnode, requires its health counter to increase in Prometheus, checks the gauge and
  boot-label exclusion, archives query JSON and cleans up its project. CI has one separate
  five-minute job. Collector resource metadata retains boot ID; Prometheus storage drops it.
- check-csv.sh is the precision/SQL fixture. check-objnode-csv.sh separately exercises real
  CSV-enabled objnode and pinned DuckDB without Collector/Prometheus or network access,
  asserts all ten components and multiple batches, and verifies clean process shutdown.

Component semantics and accepted review nits are recorded in `docs/knowledge/observability-components.md`, `observability-runtime.md` and
`observability-adapters.md`. Review reports under
reviews/ distinguish initial NITS from passing confirms. In particular shard write bytes
follow successful disk Write, including a later Sync failure; idempotent Put adds no bytes.

Final pass should update README/help/reference/book/C4 to match this tree, verify its prose
and commands, then make the phase readiness decision. Do not repeat a fixture as evidence
for real objnode; both real-node proofs have their own results JSON and logs.
