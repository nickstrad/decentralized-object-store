#!/usr/bin/env bash
set -u
base=agent_work/adversarial/05-saga
mode=$1
log=$(mktemp "$base/evidence/setup-$mode.XXXXXX")
trap 'rm -f "$log"' EXIT
docker compose run --rm -T dev sh -c 'cd /src/agent_work/adversarial/05-saga && go run . "$1"' sh "$mode" >"$log" 2>&1
rc=$?
sed -n '/^HELD /p; /^BROKEN /p' "$log"
if grep -q '^BROKEN ' "$log"; then exit 1; fi
if [ "$rc" -ne 0 ] || ! grep -q '^HELD ' "$log"; then
 cat "$log" >&2
 exit 2
fi
exit 0
