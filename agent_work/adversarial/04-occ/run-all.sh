#!/usr/bin/env bash
set -u
base=agent_work/adversarial/04-occ
mkdir -p "$base/evidence"
out="$base/evidence/run-all.txt"
: > "$out"
setup=0
started=$SECONDS
for driver in "$base"/a[0-9][0-9]-*.sh; do
 tmp=$(mktemp "$base/.result.XXXXXX")
 bash "$driver" >"$tmp"
 rc=$?
 cat "$tmp" | tee -a "$out"
 rm -f "$tmp"
 if ((rc>1)); then setup=$((setup+1)); fi
done
held=$(grep -c '^HELD ' "$out" || true)
broken=$(grep -c '^BROKEN ' "$out" || true)
printf 'SUMMARY held=%s broken=%s setup_errors=%s elapsed_seconds=%s\n' "$held" "$broken" "$setup" "$((SECONDS-started))" | tee -a "$out"
if ((setup)); then exit 2; fi
if ((broken)); then exit 1; fi
exit 0
