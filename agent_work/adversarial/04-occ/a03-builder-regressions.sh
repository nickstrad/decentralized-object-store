#!/usr/bin/env bash
set -u
base=agent_work/adversarial/04-occ
mkdir -p "$base/evidence"
docker compose run --rm -T dev bash -c '
set -u
export DST_SEEDS=8 DST_SEED_BASE=4701 DST_ARTIFACTS=/src/agent_work/adversarial/04-occ/evidence/dst
unset DST_SEED
# Run from the harness directory so property-test failure files stay in scope.
tmp=$(mktemp -d /src/agent_work/adversarial/04-occ/.tests.XXXXXX)
trap '\''rm -rf "$tmp"'\'' EXIT
if ! go test -c -o "$tmp/occ.test" ./internal/occ; then exit 2; fi
cp -R /src/internal/occ/testdata "$tmp/testdata"
cd "$tmp"
timeout 480 go tool test2json -p github.com/nickstrad/object-store/internal/occ "$tmp/occ.test" -test.v=test2json -test.run="^Test" -test.count=1 -rapid.checks=200 -rapid.seed=4701
' >"$base/evidence/builder.jsonl" 2>"$base/evidence/builder.stderr"
rc=$?
if ((rc>1)); then cat "$base/evidence/builder.stderr" >&2; exit 2; fi
python3 "$base/supplement.py" >"$base/evidence/builder.txt"
rc=$?
cat "$base/evidence/builder.txt"
exit "$rc"
