#!/usr/bin/env bash
# The interactive metrics stack behind `make metrics-up`, `metrics-urls`, `metrics-logs`,
# `metrics-down` and `load`: the three cluster nodes with the otel adapter on, the
# OpenTelemetry Collector, and Prometheus with its UI on 127.0.0.1:$PROMETHEUS_PORT.
#
# Usage: deploy/observability/metrics-stack.sh up|urls|logs|down
#        deploy/observability/metrics-stack.sh load [objload flags...]
#
# Environment:
#   METRICS_PROJECT   Compose project (default: ${COMPOSE_PROJECT_NAME:-objstore}-metrics, so
#                     the stack never touches the `make cluster-up` containers)
#   PROMETHEUS_PORT   host port of the Prometheus UI (9090)
#   METRICS_INTERVAL  how often each node exports metrics (1s; Prometheus scrapes every 1s)
#   METRICS_TIMEOUT   export timeout, below METRICS_INTERVAL (500ms)
# `up` needs bin/objnode and `load` bin/objload (`make build`).
set -euo pipefail
cd "$(dirname "$0")/../.."
project=${METRICS_PROJECT:-${COMPOSE_PROJECT_NAME:-objstore}-metrics}
port=${PROMETHEUS_PORT:-9090}
export PROMETHEUS_PORT=$port
compose=(docker compose -p "$project" -f compose.yaml -f compose.observability.yaml -f compose.metrics.yaml --profile cluster --profile observability)
nodes=(node-a1 node-b1 node-c1)
services=("${nodes[@]}" otel-collector prometheus)
prom=http://127.0.0.1:$port
window=15s

urlencode() {
  local LC_ALL=C s=$1 out= c i
  for ((i = 0; i < ${#s}; i++)); do
    c=${s:i:1}
    case $c in
      [a-zA-Z0-9.~_-]) out+=$c ;;
      *) out+=$(printf '%%%02X' "'$c") ;;
    esac
  done
  printf '%s' "$out"
}

# panel <index> <expression>: the query-string part of one graph panel over the last 5 minutes.
panel() { printf 'g%s.expr=%s&g%s.tab=graph&g%s.range_input=5m' "$1" "$(urlencode "$2")" "$1" "$1"; }

rate_expr="sum by (operation, status_class) (rate(objstore_http_requests_total[$window]))"
latency_expr="sum by (operation) (rate(objstore_http_request_duration_nanoseconds_total[$window])) / sum by (operation) (rate(objstore_http_request_duration_count_total[$window])) / 1e6"
inflight_expr="sum by (node_id) (objstore_http_requests_in_flight)"

urls() {
  echo "Prometheus           $prom"
  echo "Scrape targets       $prom/targets"
  echo "All three graphs     $prom/query?$(panel 0 "$rate_expr")&$(panel 1 "$latency_expr")&$(panel 2 "$inflight_expr")"
  echo "Request rate         $prom/query?$(panel 0 "$rate_expr")"
  echo "Mean latency (ms)    $prom/query?$(panel 0 "$latency_expr")"
  echo "Requests in flight   $prom/query?$(panel 0 "$inflight_expr")"
  echo
  echo "Node APIs for objload and objctl (inside the stack's network): node-a1:8080,node-b1:8080,node-c1:8080"
  local n a line=
  for n in "${nodes[@]}"; do
    a=$("${compose[@]}" port "$n" 8080 2>/dev/null) || a='(not running)'
    line+=" $n=$a"
  done
  echo "Node APIs on this host:$line"
  echo
  echo "Start a load:  make load            (RATE=, DURATION=, PROFILE=mixed; Ctrl-C stops it)"
  echo "Stop:          make metrics-down"
}

case ${1:-} in
up)
  [ -x bin/objnode ] || { echo "metrics-stack: bin/objnode is missing; run make build" >&2; exit 2; }
  "${compose[@]}" up -d "${services[@]}"
  echo "metrics-stack: waiting for three live members and for Prometheus to scrape all three nodes"
  # The nodes' image has curl and jq; node-a1 sits on both networks.
  if ! "${compose[@]}" exec -T node-a1 timeout 180 bash -c '
    until curl -sf http://node-a1:8080/_health 2>/dev/null | grep -q "\"Ready\":true,\"LiveMembers\":3"; do sleep 1; done
    until curl -sf "http://prometheus:9090/api/v1/query?query=count(objstore_http_requests_in_flight)" 2>/dev/null | jq -e ".data.result[0].value[1] == \"3\"" >/dev/null; do sleep 1; done
  '; then
    "${compose[@]}" logs --tail 20 "${services[@]}" >&2
    echo "metrics-stack: not ready after 180s; the stack is still up (make metrics-logs, make metrics-down)" >&2
    exit 1
  fi
  echo "metrics-stack: ready (project $project; nodes export every ${METRICS_INTERVAL:-1s}, Prometheus scrapes every 1s)"
  echo
  urls
  ;;
urls) urls ;;
logs) "${compose[@]}" logs -f --tail 50 "${services[@]}" ;;
down)
  "${compose[@]}" down --remove-orphans
  # Compose may create the observability file's obs-data volume; it is this project's own.
  docker volume rm "${project}_obs-data" >/dev/null 2>&1 || true
  ;;
load)
  shift
  [ -x bin/objload ] || { echo "metrics-stack: bin/objload is missing; run make build" >&2; exit 2; }
  tty=()
  [ -t 0 ] && [ -t 1 ] || tty=(-T)
  exec "${compose[@]}" run --rm --no-deps ${tty[@]+"${tty[@]}"} dev bin/objload "$@"
  ;;
*)
  echo "usage: metrics-stack.sh up|urls|logs|down|load [objload flags...]" >&2
  exit 2
  ;;
esac
