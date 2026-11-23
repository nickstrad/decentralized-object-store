#!/usr/bin/env bash
set -u
base=agent_work/adversarial/07-e2e
group=$1
pattern=$2
mkdir -p "$base/evidence"
python3 "$base/refresh-fixture.py" "$group" || exit 2
docker compose run --rm -T dev bash -c '
set -eu
cd /src/agent_work/adversarial/07-e2e
export GOMAXPROCS=4 E2E_TIER_R=1 DST_ARTIFACTS=/src/agent_work/adversarial/07-e2e/evidence/replays
exec go test -json -count=1 -parallel=2 -timeout=12m ./fixture -run "$1"
' bash "$pattern" >"$base/evidence/$group.json" 2>"$base/evidence/$group.stderr"
rc=$?
python3 "$base/report.py" "$base/evidence/$group.json" "$rc"
