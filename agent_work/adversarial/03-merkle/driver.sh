#!/usr/bin/env bash
set -u
base=agent_work/adversarial/03-merkle
mkdir -p "$base/evidence"
log="$base/evidence/$1.stderr"
out="$base/evidence/$1.output"
docker compose run --rm -T -e MERKLE_CHECK="${MERKLE_CHECK:-}" dev sh -c '
 cd agent_work/adversarial/03-merkle || exit 2
 trap "rm -f /tmp/merkle-checks" EXIT
 go build -o /tmp/merkle-checks . || exit 2
 /tmp/merkle-checks "$1"
' sh "$1" >"$out" 2>"$log"
rc=$?
cat "$out"
if (( rc > 1 )) || ! rg -q '^(HELD|BROKEN) ' "$out"; then
 echo "SETUP $1: Docker, compilation or execution failed; see $log"; exit 2
fi
exit "$rc"
