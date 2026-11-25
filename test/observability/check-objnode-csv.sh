#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
image=${OBS_ANALYSIS_IMAGE:-objstore-dev-observability:local}
cd "$repo"
mkdir -p test-artifacts
run_dir=$(mktemp -d test-artifacts/node-csv.XXXXXXXX)
container="objstore-${run_dir##*/}"
cleanup() {
  rc=$?
  trap - EXIT
  docker rm -f "$container" >/dev/null 2>&1 || true
  exit "$rc"
}
trap cleanup EXIT
printf 'Run artifacts: %s\n' "$run_dir"
# assert.sql = what this run proves + the standard report + its checks + the verdict.
cat > "$run_dir/assert.sql" <<'SQL'
.print make test-csv: real objnode CSV smoke test
.print ==========================================
.print What this run proves: a real objnode with the csv adapter writes metric snapshots
.print that DuckDB can read back, from all ten components, including the HTTP requests
.print this script sent. The checks are listed in the VERDICT at the end.
.print How the data was collected: the script built objnode, started one node
.print "(-observability-adapter csv, -observability-interval 200ms, keep_files 8), polled"
.print /_health until it answered, sent five more /_health requests, waited for three
.print finalized segments, stopped the node with SIGTERM, then added unfinished.tmp as crash
.print residue that the analysis must ignore. The report below reads only the CSV files.
SQL
cat deploy/observability/queries.sql >> "$run_dir/assert.sql"
cat >> "$run_dir/assert.sql" <<'SQL'
CREATE OR REPLACE VIEW checks AS
SELECT 1 AS n, 'objnode wrote more than one snapshot' AS claim,
    printf('%d snapshots', count(DISTINCT sequence)) AS observed, '>= 2' AS expected,
    count(DISTINCT sequence) >= 2 AS pass
FROM snapshots_v1
UNION ALL
SELECT 2, 'every metric component reported',
    printf('%d components', count(DISTINCT component)), '= 10',
    count(DISTINCT component) = 10
FROM snapshots_v1
UNION ALL
SELECT 3, 'the /_health requests were counted (health, 2xx)',
    printf('%s requests', coalesce(max(counter_uint64)::VARCHAR, 'no')), '> 0',
    max(counter_uint64) > 0
FROM snapshots_v1
WHERE name = 'objstore_http_requests_total'
    AND json_extract_string(labels_json, '$.operation') = 'health'
    AND json_extract_string(labels_json, '$.status_class') = '2xx'
UNION ALL
SELECT 4, 'the in-flight gauge was sampled',
    printf('%d samples', count(*)), '> 0',
    count(*) > 0
FROM snapshots_v1
WHERE name = 'objstore_http_requests_in_flight' AND gauge_uint64 IS NOT NULL;
SQL
cat test/observability/verdict.sql >> "$run_dir/assert.sql"
# No Collector/Prometheus or network is required: both node and queries run locally.
docker run --rm --name "$container" --init --network none -v objstore-gomod:/go/pkg/mod -v objstore-gocache:/root/.cache/go-build -v "$repo:/src:ro" -v "$repo/$run_dir:/fixture" "$image" timeout -k 5s 90s bash -euc '
  go build -o /tmp/objnode ./cmd/objnode
  /tmp/objnode -id 127.0.0.1:7946 -region local -listen 127.0.0.1:7946 -http 127.0.0.1:8080 -data /tmp/node-data -k 1 -m 0 -observability -observability-adapter csv -observability-interval 200ms -observability-timeout 50ms -observability-settings '\''{"directory":"/fixture/metrics","keep_files":8}'\'' > /fixture/node.log 2>&1 &
  node_pid=$!
  trap '\''kill -TERM "$node_pid" 2>/dev/null || true; wait "$node_pid" 2>/dev/null || true'\'' EXIT
  timeout 20s bash -euc '\''until curl -fs http://127.0.0.1:8080/_health >/dev/null; do sleep 0.05; done'\''
  for i in 1 2 3 4 5; do curl -fsS http://127.0.0.1:8080/_health >/dev/null; done
  timeout 20s bash -euc '\''until test "$(find /fixture/metrics -maxdepth 1 -name "*.csv" | wc -l)" -ge 3; do sleep 0.05; done'\''
  kill -TERM "$node_pid"
  wait "$node_pid"
  trap - EXIT
  chmod a+rx /fixture/metrics
  chmod a+r /fixture/metrics/*.csv
  printf "crash residue must be ignored\n" > /fixture/metrics/unfinished.tmp
  /src/deploy/observability/query-csv.sh /fixture/metrics /fixture/assert.sql
' 2>&1 | tee "$run_dir/query.log"
printf 'Query this run with make query-csv CSV_DIR=%s/metrics\n' "$run_dir"
