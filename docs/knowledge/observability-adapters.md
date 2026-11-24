# Observability adapters and CSV analysis

CSV is the runtime disk-flush sink. DuckDB is an offline consumer of its finalized
segments; no Go dependency, runtime subprocess, database file or WAL is involved.
`telemetry.ReadCSVDir` reconstructs validated batches with exact uint64 values and batch
metadata, independently of DuckDB or Collector. Labels are sorted JSON objects.
Each `.csv` file contains a header and one complete batch; exactly one numeric value
column is nonempty in every metric row. Empty point batches return an explicit error;
empty component snapshots have no rows. Readers ignore `.tmp` files left by interrupted
writes. Retention happens only after a successful atomic rename.

The dedicated metrics directory must be private and owned by the effective process user;
files are created with mode 0600. The encoded batch bound applies independently of the
configured file-size cap. Filesystem failure tests replace the directory with a regular
file, which fails under root as well as ordinary users; chmod-only sink tests are invalid
in the root Docker development environment.

Prometheus scrapes read only an immutable encoded cache. Batch validation and encoding run
outside the cache write lock; only the final body/timestamp replacement holds that lock.
Enabled config invokes a private registry settings validator so `stale_after` can be
checked against twice the configured collection interval without changing the Factory
interface. No listener or settings validator runs in disabled mode. Boot IDs belong in
CSV metadata and never become Prometheus labels.

`deploy/observability/query-csv.sh DIRECTORY [SQL_FILE]` changes working directory and
queries relative `*.csv` paths in an in-memory DuckDB process; no directory is interpolated
into SQL. The default SQL supplies `snapshots_v1`, per-boot counter deltas, HTTP rate and
mean latency, in-flight gauges and OCC discard diagnostics. Mean latency divides duration
deltas by duration-count deltas, not an unrelated global request count. Duplicate identical
CSV observations are removed before computing deltas; boot IDs partition restarts.

The optional Docker target `dev-observability` pins the [DuckDB 1.5.0 release](https://github.com/duckdb/duckdb/releases/tag/v1.5.0)
and the downloaded linux-amd64 CLI archive SHA256
`179a481edf048dd1fef0f097d66933b4b5f5a5737d4034c645e415ec722c0292`.
Measured local image size: normal dev 299026663 bytes, analysis 320741177 bytes,
delta 21714514 bytes (about 20.7 MiB). The default Docker target remains the normal dev
image. The analysis target explicitly rejects other CPU architectures.

`agent_work/10-observability/check-csv.sh` runs a separate, manual Docker analysis smoke:
Go flushes two segments with an exact value above 2^53, then pinned DuckDB asserts JSON
extraction, temp-file exclusion, exact integers, counter deltas, rate and mean latency.
This fixture proof does not replace the real objnode CSV smoke or the real OTel pipeline.

`encoding/csv.Reader` normally changes literal CRLF inside quoted fields into LF.
Because the metrics identity contract permits CRLF, `ReadCSVDir` first reads the bounded
raw LF-terminated segment and protects CRLF before parsing; the reader's normalization
then restores the original field bytes. A dedicated file round-trip test covers CRLF in
all three identity fields. Reading a segment also enforces the encoded byte bound before
allocation grows beyond that small limit.

Retention ordering uses creation timestamp/sequence filenames. Finalized files survive
an error during later retention cleanup, so a Publish error is not a promise of no disk
side effect; sequence metadata makes replay identifiable. Crash temp files are deliberately
ignored rather than deleted on startup. `sync=true` synchronizes each file before rename;
it does not promise directory-entry durability after machine power loss.
