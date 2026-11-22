#!/usr/bin/env bash
set -uo pipefail
p=agent_work/adversarial/06-integration
log="$p/evidence/loopback.log"
docker compose run --rm -T dev env GOMAXPROCS=2 GOFLAGS=-p=2 sh -c 'cd agent_work/adversarial/06-integration && go build -race -o evidence/checks . && ./evidence/checks real; r=$?; rm -f evidence/checks; exit "$r"' >"$log" 2>&1
rc=$?
if grep -q "WARNING: DATA RACE" "$log"; then printf "BROKEN C21,C22 race-detector: seed=0 race detector reported concurrent access; see evidence/loopback.log\n"; exit 1; fi
grep -E '^(HELD|BROKEN) ' "$log" || true
if ((rc != 0)) && ! grep -q '^BROKEN ' "$log"; then cat "$log" >&2; exit 2; fi
if grep -q '^BROKEN ' "$log"; then exit 1; fi
exit "$rc"
