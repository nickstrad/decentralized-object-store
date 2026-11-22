#!/bin/bash
# Start the compose cluster profile (three objnode peers in three regions), PUT an
# object through one node, GET it through another, check conditional writes get 412,
# tear the cluster down, and exit non-zero on any failure.
set -euo pipefail
cd "$(dirname "$0")/../.."
compose=(docker compose --profile cluster)
nodes=(node-a1 node-b1 node-c1)
ports=(18081 18082 18083)
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
wait_for() {
  local what=$1 deadline=$((SECONDS + $2)); shift 2
  until "$@"; do
    [ "$SECONDS" -lt "$deadline" ] || fail "timed out waiting for $what"
    sleep 1
  done
}

step "starting ${nodes[*]}"
"${compose[@]}" up -d "${nodes[@]}"

for p in "${ports[@]}"; do
  wait_for "health on :$p" 300 bash -c "curl -sf http://127.0.0.1:$p/_health >/dev/null"
done
step "all three nodes answer /_health"

alive3() { [ "$(curl -sf "http://127.0.0.1:$1/_cluster/members" | jq '[.[] | select(.State == "alive")] | length')" = 3 ]; }
for p in "${ports[@]}"; do
  wait_for "three alive members seen from :$p" 120 alive3 "$p"
done
step "every node sees three alive members in regions $(curl -sf http://127.0.0.1:18081/_cluster/members | jq -r '[.[].Region] | join(",")')"

code=$(status -X PUT "http://127.0.0.1:18081/smoke")
[ "$code" = 200 ] || fail "create bucket: $code $(cat "$work/body")"
bucket_seen() { [ "$(status "http://127.0.0.1:$1/smoke")" = 200 ]; }
for p in 18082 18083; do wait_for "bucket on :$p" 60 bucket_seen "$p"; done
step "bucket created on node-a1 is visible on node-b1 and node-c1"

head -c 1500000 /dev/urandom > "$work/object"
want=$(sha256sum "$work/object" | cut -d' ' -f1)
code=$(curl -s -o "$work/body" -D "$work/headers" -w '%{http_code}' -X PUT --data-binary @"$work/object" "http://127.0.0.1:18081/smoke/obj")
[ "$code" = 200 ] || fail "put: $code $(cat "$work/body")"
etag=$(grep -i '^etag:' "$work/headers" | tr -d '\r' | cut -d' ' -f2)
version=$(grep -i '^x-object-version:' "$work/headers" | tr -d '\r' | cut -d' ' -f2)
[ -n "$etag" ] && [ -n "$version" ] || fail "put response without ETag or version"
step "PUT 1500000 bytes on node-a1: ETag $etag version $version"

read_on_c1() {
  [ "$(curl -s -o "$work/got" -w '%{http_code}' "http://127.0.0.1:18083/smoke/obj")" = 200 ] &&
    [ "$(sha256sum "$work/got" | cut -d' ' -f1)" = "$want" ]
}
wait_for "the object read back on node-c1" 60 read_on_c1
step "GET on node-c1 returns the same 1500000 bytes (sha256 $want)"

seen_on_b1() { [ "$(status -I "http://127.0.0.1:18082/smoke/obj")" = 200 ]; }
wait_for "the object on node-b1" 60 seen_on_b1
code=$(status -X PUT -H 'If-None-Match: *' --data-binary 'x' "http://127.0.0.1:18082/smoke/obj")
[ "$code" = 412 ] || fail "create-only put over an existing object: $code $(cat "$work/body")"
step "create-only PUT (If-None-Match: *) on node-b1 over the object: 412"
code=$(status -X PUT -H "If-Match: $etag" --data-binary 'second' "http://127.0.0.1:18082/smoke/obj")
[ "$code" = 200 ] || fail "put matching the current etag: $code $(cat "$work/body")"
code=$(status -X PUT -H "If-Match: $etag" --data-binary 'third' "http://127.0.0.1:18082/smoke/obj")
[ "$code" = 412 ] || fail "put with a replaced etag: $code $(cat "$work/body")"
step "PUT If-Match the current ETag: 200; the same ETag again: 412"

discards=$(curl -sf http://127.0.0.1:18081/_health | jq '.Discards')
step "health on node-a1 reports $discards last-writer-wins discards"
step "PASS"
