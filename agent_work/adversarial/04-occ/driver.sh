#!/usr/bin/env bash
set -u
base=agent_work/adversarial/04-occ
group=$1
mkdir -p "$base/evidence"
docker compose run --rm -T -e CHECK="${CHECK:-}" dev bash -c '
set -eu
cd /src/agent_work/adversarial/04-occ
tmp=$(mktemp -d .build.XXXXXX)
trap '\''rm -rf "$tmp"'\'' EXIT
if ! go build -o "$tmp/checks" . >"$tmp/build.log" 2>&1; then cat "$tmp/build.log" >&2; exit 2; fi
timeout 300 "$tmp/checks" "$1"
' bash "$group" >"$base/evidence/$group.txt" 2>"$base/evidence/$group.stderr"
rc=$?
cat "$base/evidence/$group.txt"
if ((rc>1)); then cat "$base/evidence/$group.stderr" >&2; exit 2; fi
if ((rc==1)) && ! rg -q "^BROKEN " "$base/evidence/$group.txt"; then cat "$base/evidence/$group.stderr" >&2; exit 2; fi
exit "$rc"
