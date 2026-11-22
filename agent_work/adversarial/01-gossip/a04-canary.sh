#!/usr/bin/env bash
set -u
base=agent_work/adversarial/01-gossip
# The production-only API cannot disable indirect probes; use the existing test-only switch.
docker compose run --rm -T dev bash -c 'unset DST_SEED; DST_SEEDS=12 DST_SEED_BASE=2001 go test ./internal/gossip -run "^TestCanaryNoIndirectProbe$" -count=1' >"$base/evidence/canary.stdout" 2>"$base/evidence/canary.stderr"
rc=$?
if ((rc==0)); then
 echo 'HELD C17 indirect-disabled-canary: existing canary passed on 12 fresh seeds 2001..2012 plus 3 pinned seeds, confirming suspicions when indirect probes are disabled'
 exit 0
fi
if ((rc==1)) && rg -q -- '--- FAIL:' "$base/evidence/canary.stdout"; then
 echo 'BROKEN C17 indirect-disabled-canary: existing canary failed for seeds 2001..2012; see evidence/canary.stdout for exact seed'
 exit 1
fi
cat "$base/evidence/canary.stderr" >&2
exit 2
