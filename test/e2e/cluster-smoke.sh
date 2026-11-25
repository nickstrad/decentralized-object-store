#!/bin/bash
# Start the compose cluster profile (three objnode peers in three regions), PUT an
# object through one node, GET it through another, check conditional writes get 412,
# tear the cluster down, and exit non-zero on any failure.
set -euo pipefail
cd "$(dirname "$0")/../.."
if [[ $# -gt 1 || (${1:-} != "" && ${1:-} != --csv) ]]; then
  echo 'Usage: cluster-smoke.sh [--csv]' >&2
  exit 2
fi
project="objstore-smoke-$$-$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')"
compose=(docker compose -p "$project" --profile cluster)
nodes=(node-a1 node-b1 node-c1)
run_dir=
if [[ ${1:-} == --csv ]]; then
  compose+=(-f compose.yaml -f test/e2e/compose.csv.yaml)
  mkdir -p test-artifacts
  run_dir=$(mktemp -d test-artifacts/cluster-csv.XXXXXXXX)
  printf 'Run artifacts: %s\n' "$run_dir"
fi
work=$(mktemp -d)
cleanup() {
  local rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "SMOKE: failed (exit $rc); node logs follow" >&2
    "${compose[@]}" logs --no-color --tail 40 "${nodes[@]}" >&2 || true
  fi
  "${compose[@]}" down --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$work"
  exit "$rc"
}
trap cleanup EXIT
fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }
step() { echo "SMOKE: $*"; }
status() { curl -s -o "$work/body" -w '%{http_code}' "$@"; }
sha256() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi | cut -d' ' -f1
}
csv_ready() {
  "${compose[@]}" exec -T "$1" sh -ec 'set -- /tmp/cluster-metrics/*.csv; test "$#" -ge 2'
}
wait_for() {
  local what=$1 deadline=$((SECONDS + $2)); shift 2
  until "$@"; do
    [ "$SECONDS" -lt "$deadline" ] || fail "timed out waiting for $what"
    sleep 1
  done
}

step "starting ${nodes[*]}"
"${compose[@]}" up -d "${nodes[@]}"
ports=()
for n in "${nodes[@]}"; do
  hp=$("${compose[@]}" port "$n" 8080 | tail -n1)
  [ -n "$hp" ] || fail "no host port published for $n"
  ports+=("${hp##*:}")
done
A=${ports[0]} B=${ports[1]} C=${ports[2]}
step "project $project; host ports a1=$A b1=$B c1=$C"

for p in "${ports[@]}"; do
  wait_for "health on :$p" 300 bash -c "curl -sf http://127.0.0.1:$p/_health >/dev/null"
done
step "all three nodes answer /_health"

alive3() { [ "$(curl -sf "http://127.0.0.1:$1/_cluster/members" | jq '[.[] | select(.State == "alive")] | length')" = 3 ]; }
for p in "${ports[@]}"; do
  wait_for "three alive members seen from :$p" 120 alive3 "$p"
done
step "every node sees three alive members in regions $(curl -sf http://127.0.0.1:$A/_cluster/members | jq -r '[.[].Region] | join(",")')"

code=$(status -X PUT "http://127.0.0.1:$A/smoke")
[ "$code" = 200 ] || fail "create bucket: $code $(cat "$work/body")"
bucket_seen() { [ "$(status "http://127.0.0.1:$1/smoke")" = 200 ]; }
for p in "$B" "$C"; do wait_for "bucket on :$p" 60 bucket_seen "$p"; done
step "bucket created on node-a1 is visible on node-b1 and node-c1"

head -c 1500000 /dev/urandom > "$work/object"
want=$(sha256 "$work/object")
code=$(curl -s -o "$work/body" -D "$work/headers" -w '%{http_code}' -X PUT --data-binary @"$work/object" "http://127.0.0.1:$A/smoke/obj")
[ "$code" = 200 ] || fail "put: $code $(cat "$work/body")"
etag=$(grep -i '^etag:' "$work/headers" | tr -d '\r' | cut -d' ' -f2)
version=$(grep -i '^x-object-version:' "$work/headers" | tr -d '\r' | cut -d' ' -f2)
[ -n "$etag" ] && [ -n "$version" ] || fail "put response without ETag or version"
step "PUT 1500000 bytes on node-a1: ETag $etag version $version"

read_on_c1() {
  [ "$(curl -s -o "$work/got" -w '%{http_code}' "http://127.0.0.1:$C/smoke/obj")" = 200 ] &&
    [ "$(sha256 "$work/got")" = "$want" ]
}
wait_for "the object read back on node-c1" 60 read_on_c1
step "GET on node-c1 returns the same 1500000 bytes (sha256 $want)"

seen_on_b1() { [ "$(status -I "http://127.0.0.1:$B/smoke/obj")" = 200 ]; }
wait_for "the object on node-b1" 60 seen_on_b1
code=$(status -X PUT -H 'If-None-Match: *' --data-binary 'x' "http://127.0.0.1:$B/smoke/obj")
[ "$code" = 412 ] || fail "create-only put over an existing object: $code $(cat "$work/body")"
step "create-only PUT (If-None-Match: *) on node-b1 over the object: 412"
code=$(status -X PUT -H "If-Match: $etag" --data-binary 'second' "http://127.0.0.1:$B/smoke/obj")
[ "$code" = 200 ] || fail "put matching the current etag: $code $(cat "$work/body")"
code=$(status -X PUT -H "If-Match: $etag" --data-binary 'third' "http://127.0.0.1:$B/smoke/obj")
[ "$code" = 412 ] || fail "put with a replaced etag: $code $(cat "$work/body")"
step "PUT If-Match the current ETag: 200; the same ETag again: 412"

discards=$(curl -sf http://127.0.0.1:$A/_health | jq '.Discards')
step "health on node-a1 reports $discards last-writer-wins discards"
if [[ -n "$run_dir" ]]; then
  sleep 1 # Give the collectors time to sample the completed workload.
  for n in "${nodes[@]}"; do wait_for "CSV samples on $n" 10 csv_ready "$n"; done
  "${compose[@]}" stop "${nodes[@]}"
  for n in "${nodes[@]}"; do
    mkdir "$run_dir/$n"
    "${compose[@]}" cp "$n:/tmp/cluster-metrics/." "$run_dir/$n"
    printf 'Query %s with make query-csv CSV_DIR=%s/%s\n' "$n" "$run_dir" "$n"
  done
fi
step "PASS"
