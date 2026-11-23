#!/usr/bin/env bash
# Compile every mutation patch (inside the dev container): build, vet-free test compile of the
# touched packages. Prints OK/FAIL per patch. Run from the worktree root.
set -u
ROOT=$(pwd)
d=$(mktemp -d /tmp/a07cc.XXXXXX)
trap 'rm -rf "$d"' EXIT
git ls-files -z | grep -zv '^agent_work/\|^book/' | tar --null -T - -cf - | tar -xf - -C "$d"
cp -r agent_work/adversarial/07-e2e-opus/mutations "$d/.mut"
(cd "$d" && docker compose run --rm -T dev bash -c '
for p in .mut/*.patch; do
  rm -rf /tmp/cc && mkdir /tmp/cc && git ls-files >/dev/null 2>&1; tar --exclude=./.mut -cf - . | tar -xf - -C /tmp/cc
  if ! (cd /tmp/cc && git apply -p1 /src/$p); then echo "FAIL $p (patch)"; continue; fi
  if (cd /tmp/cc && go build ./... && go test -count=1 -run NONE_XYZ ./test/e2e ./internal/node ./internal/saga >/dev/null && go vet -tags e2efault ./internal/noderuntime ./internal/node >/dev/null) >/tmp/cc.log 2>&1; then echo "OK $p"; else echo "FAIL $p"; head -5 /tmp/cc.log; fi
done')
