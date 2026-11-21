#!/usr/bin/env bash
set -u
base=agent_work/adversarial/0b-simulator
mkdir -p "$base/evidence"
group=$1
# Build/run inside the required container; all temporary files are beneath the allowed directory.
docker compose run --rm -T dev bash -c '
 set -eu
 cd /src/agent_work/adversarial/0b-simulator
 tmp=$(mktemp -d .build.XXXXXX)
 trap '\''rm -rf "$tmp"'\'' EXIT
 if ! go build -o "$tmp/attack" . >"$tmp/build.log" 2>&1; then cat "$tmp/build.log" >&2; exit 2; fi
 timeout 300 "$tmp/attack" "$1"
' bash "$group" >"$base/evidence/$group.txt" 2>"$base/evidence/$group.stderr"
rc=$?
cat "$base/evidence/$group.txt"
if ((rc>1)); then cat "$base/evidence/$group.stderr" >&2; exit 2; fi
exit "$rc"
