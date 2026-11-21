#!/usr/bin/env bash
set -u
base=agent_work/adversarial/0b-simulator
mkdir -p "$base/evidence"
: > "$base/evidence/run-all.txt"
setup=0
start=$SECONDS
for script in "$base"/a[0-9][0-9]-*.sh; do
 bash "$script" >>"$base/evidence/run-all.txt"
 rc=$?
 if ((rc>1)); then setup=$((setup+1)); fi
done
cat "$base/evidence/run-all.txt"
held=$(grep -c '^HELD ' "$base/evidence/run-all.txt" || true)
broken=$(grep -c '^BROKEN ' "$base/evidence/run-all.txt" || true)
printf 'SUMMARY held=%s broken=%s setup_errors=%s elapsed_seconds=%s\n' "$held" "$broken" "$setup" "$((SECONDS-start))" | tee -a "$base/evidence/run-all.txt"
if ((setup)); then exit 2; fi
if ((broken)); then exit 1; fi
