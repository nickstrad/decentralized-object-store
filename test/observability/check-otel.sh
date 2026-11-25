#!/usr/bin/env bash
# Default is the real objnode test. --fixture sends controlled metrics.
set -euo pipefail
cd "$(dirname "$0")/../.."
mode=${1:-node}
if [[ "$mode" != node && "$mode" != --fixture ]]; then echo 'usage: check-otel.sh [--fixture]' >&2; exit 2; fi
project=${COMPOSE_PROJECT_NAME:-objstore-obs-$$}
export COMPOSE_PROJECT_NAME="$project"
compose=(docker compose -p "$project" -f compose.yaml -f compose.observability.yaml --profile observability)
logs="test-artifacts/otel/$project/${mode#--}"
mkdir -p "$logs"
export OBS_NODE_BINARY="/src/test-artifacts/otel/$project/objnode"
cleanup() {
  rc=$?
  trap - EXIT
  "${compose[@]}" logs --no-color otel-collector prometheus obs-node > "$logs/services.log" 2>&1 || true
  # Remove only this project's services and its disposable node data, never Go cache volumes.
  "${compose[@]}" down --remove-orphans > "$logs/cleanup.log" 2>&1 || true
  docker volume rm "${project}_obs-data" >> "$logs/cleanup.log" 2>&1 || true
  exit "$rc"
}
trap cleanup EXIT
"${compose[@]}" build dev > "$logs/build.log" 2>&1
"${compose[@]}" run --rm -T --no-deps otel-collector validate --config=/etc/otelcol-contrib/config.yaml </dev/null > "$logs/validate.log" 2>&1
services=(otel-collector prometheus)
if [[ "$mode" == node ]]; then
  "${compose[@]}" run --rm -T --no-deps dev go build -o "$OBS_NODE_BINARY" ./cmd/objnode </dev/null
  services+=(obs-node)
fi
"${compose[@]}" up -d "${services[@]}"
# One post-start deadline includes readiness and the test; no silent enabled-test skip.
"${compose[@]}" run --rm -T --no-deps -e OBS_FIXTURE="$([[ "$mode" == --fixture ]] && echo 1 || echo 0)" -e OBS_INTEGRATION="$([[ "$mode" == node ]] && echo 1 || echo 0)" dev timeout -k 5s 60s bash -euc '
  until curl -fsS http://otel-collector:9464/metrics >/dev/null && curl -fsS http://prometheus:9090/-/ready >/dev/null && curl -fsS http://prometheus:9090/api/v1/targets | jq -e '\''.data.activeTargets | any(.health == "up")'\'' >/dev/null; do sleep 0.2; done
  if [[ "$OBS_INTEGRATION" == 1 ]]; then
    until curl -fsS http://obs-node:8080/_health >/dev/null; do sleep 0.2; done
    go test -json -count=1 -timeout 55s -run "^TestObjnodeOTelPrometheus$" ./test/observability
  else
    go test -json -count=1 -timeout 55s -run "^TestOTelCollectorFixture$" ./internal/telemetry
  fi
' </dev/null | tee "$logs/test.jsonl"
testname=TestObjnodeOTelPrometheus
[[ "$mode" == node ]] || testname=TestOTelCollectorFixture
jq -se --arg name "$testname" 'any(.[]; .Test == $name and .Action == "pass") and (all(.[]; .Action != "fail" and .Action != "skip"))' "$logs/test.jsonl" >/dev/null
