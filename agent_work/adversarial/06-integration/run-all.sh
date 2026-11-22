#!/usr/bin/env bash
set -uo pipefail
p=agent_work/adversarial/06-integration
mkdir -p "$p/evidence"
: > "$p/evidence/run-all.txt"
setup=0
SECONDS=0
for driver in "$p"/a[0-9][0-9]-*.sh; do
  bash "$driver" | tee -a "$p/evidence/run-all.txt"
  rc=${PIPESTATUS[0]}
  if ((rc != 0 && rc != 1)); then setup=$((setup+1)); fi
done
held=$(grep -c '^HELD ' "$p/evidence/run-all.txt" || true)
broken=$(grep -c '^BROKEN ' "$p/evidence/run-all.txt" || true)
printf 'SUMMARY held=%s broken=%s setup_errors=%s elapsed_seconds=%s\n' "$held" "$broken" "$setup" "$SECONDS" | tee -a "$p/evidence/run-all.txt"
((setup == 0)) || exit 2
((broken == 0)) || exit 1
