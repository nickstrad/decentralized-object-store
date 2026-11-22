#!/usr/bin/env bash
set -u
base=agent_work/adversarial/02-erasure
mkdir -p "$base/evidence"
docker compose run --rm -T dev go test -count=1 -v -run '^TestCanaryChecksumSkip(BreaksCorruptionProperty|FailsStripedDST)$' ./internal/storage >"$base/evidence/canaries.log" 2>&1
rc=$?
if ((rc==0)) && grep -q -- '--- PASS: TestCanaryChecksumSkipBreaksCorruptionProperty' "$base/evidence/canaries.log" && grep -q -- '--- PASS: TestCanaryChecksumSkipFailsStripedDST' "$base/evidence/canaries.log"; then
 echo 'HELD C29 checksum-canaries: Both existing test-only checksum-disabled canaries execute and detect their planted faults.'
elif grep -q -- '--- FAIL: TestCanary' "$base/evidence/canaries.log"; then
 echo 'BROKEN C29 checksum-canaries: Existing checksum canary failed; see evidence/canaries.log for its reproduction seed.'
 exit 1
else
 cat "$base/evidence/canaries.log" >&2
 exit 2
fi
