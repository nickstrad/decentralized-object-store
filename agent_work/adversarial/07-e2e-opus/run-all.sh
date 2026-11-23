#!/usr/bin/env bash
# Run every driver in order (one heavy Docker run at a time), collect the HELD/BROKEN lines into
# evidence/run-all.txt and print a summary. Run from anywhere; exit 0 all held, 1 any broken, 2 setup.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
cd "$HERE/../../.." || exit 2
mkdir -p "$HERE/evidence"
out=$HERE/evidence/run-all.txt
: > "$out"
start=$(date +%s)
worst=0
for drv in "$HERE"/a[0-9][0-9]-*.sh; do
  echo "# $(basename "$drv") start $(date -u +%H:%M:%S)" >> "$out"
  bash "$drv" 2>"$HERE/evidence/$(basename "$drv" .sh).stderr" | tee -a "$out"
  rc=${PIPESTATUS[0]}
  echo "# $(basename "$drv") exit $rc $(date -u +%H:%M:%S)" >> "$out"
  [ $rc -gt $worst ] && worst=$rc
done
held=$(grep -c '^HELD ' "$out"); broken=$(grep -c '^BROKEN ' "$out"); setup=$(grep -c '^SETUP ' "$out")
echo "SUMMARY held=$held broken=$broken setup_errors=$setup wall=$(( $(date +%s) - start ))s" | tee -a "$out"
[ "$setup" -gt 0 ] && exit 2
[ "$broken" -gt 0 ] && exit 1
exit 0
