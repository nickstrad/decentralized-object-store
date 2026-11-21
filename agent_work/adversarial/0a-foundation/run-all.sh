#!/usr/bin/env bash
set -u
here=agent_work/adversarial/0a-foundation
mkdir -p "$here/evidence"
start=$SECONDS
rc=0
: > "$here/evidence/run-all.txt"
for script in "$here"/a[0-9][0-9]-*.sh; do
  bash "$script" > "$here/evidence/$(basename "$script").txt" 2>&1
  status=$?
  cat "$here/evidence/$(basename "$script").txt" >> "$here/evidence/run-all.txt"
  grep -E '^(HELD|BROKEN) ' "$here/evidence/$(basename "$script").txt" || true
  if [ "$status" -gt 1 ]; then
    echo "SETUP ERROR: $script (see evidence/$(basename "$script").txt)" >&2
    rc=2
  elif [ "$status" = 1 ] && [ "$rc" = 0 ]; then rc=1; fi
done
held=$(grep -c '^HELD ' "$here/evidence/run-all.txt" || true)
broken=$(grep -c '^BROKEN ' "$here/evidence/run-all.txt" || true)
echo "SUMMARY held=$held broken=$broken seconds=$((SECONDS-start)) exit=$rc" | tee "$here/evidence/summary.txt"
exit "$rc"
