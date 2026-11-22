#!/usr/bin/env bash
set -uo pipefail
p=agent_work/adversarial/06-integration
log="$p/evidence/cli.log"
docker compose run --rm -T dev env GOMAXPROCS=2 GOFLAGS=-p=2 sh -c 'go build -o agent_work/adversarial/06-integration/evidence/objnode ./cmd/objnode && cd agent_work/adversarial/06-integration && go build -o evidence/checks . && ./evidence/checks cli; r=$?; rm -f evidence/checks evidence/objnode; exit "$r"' >"$log" 2>&1
rc=$?
grep -E '^(HELD|BROKEN) ' "$log" || true
if ((rc != 0)) && ! grep -q '^BROKEN ' "$log"; then cat "$log" >&2; exit 2; fi
if grep -q '^BROKEN ' "$log"; then exit 1; fi
exit "$rc"
