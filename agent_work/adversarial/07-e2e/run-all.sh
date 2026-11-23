#!/usr/bin/env bash
set -u
base=agent_work/adversarial/07-e2e
mkdir -p "$base/evidence"
: > "$base/evidence/run-all.txt"
setup=0
start=$SECONDS
for driver in "$base"/a[0-9][0-9]-*.sh; do
 bash "$driver" > "$base/evidence/driver-result.txt"
 rc=$?
 cat "$base/evidence/driver-result.txt" | tee -a "$base/evidence/run-all.txt"
 if ((rc>1)); then setup=$((setup+1)); fi
done
python3 -c 'from pathlib import Path; Path("agent_work/adversarial/07-e2e/evidence/driver-result.txt").unlink(missing_ok=True)'
held=$(rg -c '^HELD ' "$base/evidence/run-all.txt" || true)
broken=$(rg -c '^BROKEN ' "$base/evidence/run-all.txt" || true)
held=${held:-0}; broken=${broken:-0}
printf 'SUMMARY held=%s broken=%s setup_errors=%s seconds=%s\n' "$held" "$broken" "$setup" "$((SECONDS-start))" | tee -a "$base/evidence/run-all.txt"
if ((setup)); then exit 2; elif ((broken)); then exit 1; fi
