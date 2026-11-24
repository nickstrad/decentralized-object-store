#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
image=${OBS_ANALYSIS_IMAGE:-object-store-obs-analysis:stagec}
evidence="$repo/agent_work/10-observability/evidence/node-csv"
mkdir -p -- "$evidence"
fixture=$(mktemp -d)
container="objstore-csv-$$"
cleanup() {
  rc=$?
  trap - EXIT
  docker rm -f "$container" >/dev/null 2>&1 || true
  if [[ -f "$fixture/node.log" ]]; then cp -- "$fixture/node.log" "$evidence/node.log"; fi
  rm -rf -- "$fixture"
  exit "$rc"
}
trap cleanup EXIT
mkdir -m 700 -- "$fixture/metrics"
cp -- "$repo/deploy/observability/queries.sql" "$fixture/assert.sql"
cat >> "$fixture/assert.sql" <<'SQL'
SELECT CASE WHEN count(DISTINCT sequence)>=2 THEN true ELSE error('expected multiple objnode batches') END FROM snapshots_v1;
SELECT CASE WHEN count(DISTINCT component)=10 THEN true ELSE error('missing objnode metric component') END FROM snapshots_v1;
SELECT CASE WHEN max(counter_uint64)>0 THEN true ELSE error('missing actual HTTP health metric') END FROM snapshots_v1 WHERE name='objstore_http_requests_total' AND json_extract_string(labels_json,'$.operation')='health' AND json_extract_string(labels_json,'$.status_class')='2xx';
SELECT CASE WHEN count(*)>0 THEN true ELSE error('missing in-flight gauge') END FROM snapshots_v1 WHERE name='objstore_http_requests_in_flight' AND gauge_uint64 IS NOT NULL;
SQL
# No Collector/Prometheus or network is required: both node and queries run locally.
timeout 90s docker run --rm --name "$container" --init --network none -v "$repo:/src:ro" -v "$fixture:/fixture" "$image" bash -euc '
  go build -o /tmp/objnode ./cmd/objnode
  /tmp/objnode -id 127.0.0.1:7946 -region local -listen 127.0.0.1:7946 -http 127.0.0.1:8080 -data /tmp/node-data -k 1 -m 0 -observability -observability-adapter csv -observability-interval 200ms -observability-timeout 50ms -observability-settings '\''{"directory":"/fixture/metrics","keep_files":8}'\'' > /fixture/node.log 2>&1 &
  node_pid=$!
  trap '\''kill -TERM "$node_pid" 2>/dev/null || true; wait "$node_pid" 2>/dev/null || true'\'' EXIT
  timeout 20s bash -euc '\''until curl -fsS http://127.0.0.1:8080/_health >/dev/null; do sleep 0.05; done'\''
  for i in 1 2 3 4 5; do curl -fsS http://127.0.0.1:8080/_health >/dev/null; done
  timeout 20s bash -euc '\''until test "$(find /fixture/metrics -maxdepth 1 -name "*.csv" | wc -l)" -ge 3; do sleep 0.05; done'\''
  kill -TERM "$node_pid"
  wait "$node_pid"
  trap - EXIT
  printf "crash residue must be ignored\n" > /fixture/metrics/unfinished.tmp
  /src/deploy/observability/query-csv.sh /fixture/metrics /fixture/assert.sql
' 2>&1 | tee "$evidence/query.log"
cp -- "$fixture/node.log" "$evidence/node.log"
cp -- "$fixture/metrics/"*.csv "$evidence/"
