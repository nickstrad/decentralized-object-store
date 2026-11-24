#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
image=${OBS_ANALYSIS_IMAGE:-object-store-obs-analysis:stagec}
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
cd -- "$repo"
OBS_CSV_FIXTURE_DIR="$fixture/metrics" go test ./internal/telemetry -run '^TestWriteCSVAnalysisFixture$' -count=1
# The ordinary query file supplies the views; assertions below exercise their consumers.
cat deploy/observability/queries.sql > "$fixture/assert.sql"
cat >> "$fixture/assert.sql" <<'SQL'
SELECT CASE WHEN count(*)=14 THEN true ELSE error('glob or temp isolation failed') END FROM snapshots_v1;
SELECT CASE WHEN min(counter_uint64)=9007199254740993::UBIGINT THEN true ELSE error('uint64 precision failed') END FROM snapshots_v1 WHERE name='objstore_http_request_bytes_total';
SELECT CASE WHEN count(*)=2 THEN true ELSE error('JSON extraction failed') END FROM snapshots_v1 WHERE json_extract_string(labels_json,'$.operation')='health' AND kind='counter' AND name='objstore_http_requests_total';
SELECT CASE WHEN max(delta)=2 THEN true ELSE error('counter delta failed') END FROM counter_deltas_v1 WHERE name='objstore_http_requests_total';
SELECT CASE WHEN max(requests_per_second)=2 THEN true ELSE error('rate failed') END FROM http_rates_v1;
SELECT CASE WHEN max(mean_latency_ns)=20 THEN true ELSE error('latency failed') END FROM http_latency_v1;
SQL
docker run --rm --network none -v "$repo:/src:ro" -v "$fixture:/fixture:ro" "$image" /src/deploy/observability/query-csv.sh /fixture/metrics /fixture/assert.sql
