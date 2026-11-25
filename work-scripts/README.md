# Reusable work scripts

Run these Make targets from the repository root.

- `make test-csv` verifies that a real node emits CSV metrics and that DuckDB can query them. [Node CSV smoke test](../test/observability/check-objnode-csv.sh)
- `make test-csv-fixture` verifies exact counters above 2^53, label extraction, exclusion of unfinished files, counter deltas, request rates, and mean latency. [CSV fixture test](../test/observability/check-csv.sh)
- `make test-otel` verifies that real node metrics reach Prometheus through the Collector. The script also accepts `--fixture` for a test with controlled metrics. [OTel pipeline test](../test/observability/check-otel.sh)
- `make test-cluster` verifies cross-node reads and writes and conditional requests in a three-node Compose cluster. `make test-cluster-csv` also collects each node's CSV metrics. [Cluster smoke test](../test/e2e/cluster-smoke.sh)
- `make query-csv CSV_DIR=/path/to/metrics` runs the bundled SQL against a directory of finalized segments. The query script also accepts a custom SQL file. [CSV query script](../deploy/observability/query-csv.sh)

The CSV targets print a query command for the artifacts they save under `test-artifacts/`.
