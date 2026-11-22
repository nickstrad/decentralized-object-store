#!/usr/bin/env bash
set -uo pipefail
p=agent_work/adversarial/06-integration
log="$p/evidence/simulator.log"
docker compose run --rm -T dev sh -c 'cd agent_work/adversarial/06-integration && go build -o evidence/checks . && ./evidence/checks sim; r=$?; rm -f evidence/checks; exit "$r"' >"$log" 2>&1
rc=$?
grep -E '^(HELD|BROKEN) ' "$log" || true
if ((rc != 0)) && ! grep -q '^BROKEN ' "$log"; then cat "$log" >&2; exit 2; fi
if grep -q '^BROKEN ' "$log"; then exit 1; fi
exit "$rc"
