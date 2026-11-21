#!/usr/bin/env bash
set -u
base=agent_work/adversarial/0b-simulator
mkdir -p "$base/evidence"
docker compose run --rm -T dev bash -c 'cd /src/agent_work/adversarial/0b-simulator && python3 reporting.py' >"$base/evidence/c7.txt" 2>"$base/evidence/c7.stderr"
rc=$?
cat "$base/evidence/c7.txt"
if ((rc>1)) || ! grep -q '^\(HELD\|BROKEN\) C7 cli-failure-report:' "$base/evidence/c7.txt"; then cat "$base/evidence/c7.stderr" >&2; exit 2; fi
exit "$rc"
