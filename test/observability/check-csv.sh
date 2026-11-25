#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
image=${OBS_ANALYSIS_IMAGE:-objstore-dev-observability:local}
cd -- "$repo"
mkdir -p test-artifacts
run_dir=$(mktemp -d test-artifacts/csv-fixture.XXXXXXXX)
printf 'Run artifacts: %s\n' "$run_dir"
docker run --rm -v objstore-gomod:/go/pkg/mod -v objstore-gocache:/root/.cache/go-build \
  -v "$repo:/src:ro" -v "$repo/$run_dir:/fixture" -e OBS_CSV_FIXTURE_DIR=/fixture/metrics \
  "$image" bash -euc '
    go test ./internal/telemetry -run "^TestWriteCSVAnalysisFixture$" -count=1
    chmod a+rx /fixture/metrics
    chmod a+r /fixture/metrics/*.csv
  '
# assert.sql = what this run proves + the standard report + its checks + the verdict.
cat > "$run_dir/assert.sql" <<'SQL'
.print make test-csv-fixture: DuckDB analysis over controlled CSV data
.print ===============================================================
.print What this run proves: the DuckDB views read exactly the finalized segments, keep
.print uint64 counters above 2^53 exact, extract JSON labels, and compute counter deltas,
.print request rates and mean latency correctly. The checks are listed in the VERDICT.
.print How the data was collected: TestWriteCSVAnalysisFixture wrote two hand-built
.print snapshots through the csv adapter (node n, region r, boot b, process start 1 ns after
.print the Unix epoch, snapshots 1 s apart; between them health 2xx requests went 2 -> 4 and
.print health handler time 20 -> 60 ns), then added crash.tmp, which must be ignored.
SQL
cat deploy/observability/queries.sql >> "$run_dir/assert.sql"
cat >> "$run_dir/assert.sql" <<'SQL'
CREATE OR REPLACE VIEW checks AS
SELECT 1 AS n, 'glob reads only the two finalized segments (crash.tmp ignored)' AS claim,
    printf('%d rows', count(*)) AS observed, '= 14' AS expected,
    count(*) = 14 AS pass
FROM snapshots_v1
UNION ALL
SELECT 2, 'uint64 above 2^53 is exact (request bytes)',
    coalesce(min(counter_uint64)::VARCHAR, 'no rows'), '= 9007199254740993',
    min(counter_uint64) = 9007199254740993::UBIGINT
FROM snapshots_v1
WHERE name = 'objstore_http_request_bytes_total'
UNION ALL
SELECT 3, 'JSON label extraction finds health requests in both snapshots',
    printf('%d rows', count(*)), '= 2',
    count(*) = 2
FROM snapshots_v1
WHERE json_extract_string(labels_json, '$.operation') = 'health'
    AND kind = 'counter' AND name = 'objstore_http_requests_total'
UNION ALL
SELECT 4, 'counter delta between snapshots (counter_deltas_v1)',
    coalesce(max(delta)::VARCHAR, 'none'), '= 2',
    max(delta) = 2
FROM counter_deltas_v1
WHERE name = 'objstore_http_requests_total'
UNION ALL
SELECT 5, 'request rate = delta / elapsed (http_rates_v1)',
    coalesce(max(requests_per_second)::VARCHAR || ' req/s', 'none'), '= 2 req/s',
    max(requests_per_second) = 2
FROM http_rates_v1
UNION ALL
SELECT 6, 'mean latency = duration delta / count delta (http_latency_v1)',
    coalesce(max(mean_latency_ns)::VARCHAR || ' ns', 'none'), '= 20 ns',
    max(mean_latency_ns) = 20
FROM http_latency_v1;
SQL
cat test/observability/verdict.sql >> "$run_dir/assert.sql"
docker run --rm --network none -v "$repo:/src:ro" -v "$repo/$run_dir:/fixture:ro" "$image" \
  /src/deploy/observability/query-csv.sh /fixture/metrics /fixture/assert.sql | tee "$run_dir/query.log"
printf 'Query this run with make query-csv CSV_DIR=%s/metrics\n' "$run_dir"
