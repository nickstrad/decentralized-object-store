#!/bin/bash
# Landing gates G0-G6 for one chunk, run through Docker.
#
# Usage: agent_work/gates/gate.sh <chunk>
#   Called on the host: builds the dev image, then re-executes itself inside the `dev`
#   service of compose.yaml. Called inside the container: runs the gates and writes
#   agent_work/<chunk>/gates/result.json (plus one log per check, gitignored).
#
# Environment:
#   GATE_ONLY=G0,G3     run a subset (the result is then marked partial and does not count)
#   GATE_STAGE=build    (default) product Go must be comment-free (pre-prose rule)
#   GATE_STAGE=final    after the prose stage: the comment check is replaced by prose-check.sh
#   FUZZ_SECONDS=30     per fuzz target in G3
#   GATE_NO_DOCKER=1    run where you are, without Docker; the result is marked
#                       in_container=false and never counts for landing
#   GATE_OUT=<dir>      where to write results (default agent_work/<chunk>/gates)
#
# Exit status: 0 all requested gates green, 1 a gate is red, 2 usage or setup error.
set -u
chunk=${1:-}
[ -n "$chunk" ] || { echo "usage: gate.sh <chunk>" >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd -P)
root=$(cd "$here/../.." && pwd -P)
cd "$root" || exit 2

in_container=false
if [ -f /.dockerenv ] && [ "${OBJSTORE_DEV_CONTAINER:-}" = 1 ]; then in_container=true; fi

# ---------------------------------------------------------------- host side
if [ "$in_container" = false ] && [ "${GATE_NO_DOCKER:-}" != 1 ]; then
  sha=$(git rev-parse HEAD 2>/dev/null || echo unknown)
  dirty=false
  if [ -n "$(git status --porcelain 2>/dev/null | grep -v ' agent_work/[^/]*/gates/' )" ]; then dirty=true; fi
  echo "gate: building the dev image (G6)"
  docker compose build -q dev || { echo "gate: image build failed" >&2; exit 2; }
  image_id=$(docker image inspect objstore-dev:local --format '{{.Id}}' 2>/dev/null || echo unknown)
  exec docker compose run --rm -T \
    -e GATE_SHA="$sha" -e GATE_DIRTY="$dirty" -e GATE_IMAGE_ID="$image_id" \
    -e GATE_ONLY="${GATE_ONLY:-}" -e GATE_STAGE="${GATE_STAGE:-build}" \
    -e FUZZ_SECONDS="${FUZZ_SECONDS:-30}" -e GATE_OUT="${GATE_OUT:-}" \
    dev agent_work/gates/gate.sh "$chunk"
fi

# ---------------------------------------------------------------- gate side
sha=${GATE_SHA:-unknown}
stage=${GATE_STAGE:-build}
out=${GATE_OUT:-agent_work/$chunk/gates}
mkdir -p "$out" || exit 2
rm -f "$out"/G*.log "$out/result.json"
module=$(go list -m 2>/dev/null)
[ -n "$module" ] || { echo "gate: not in a Go module" >&2; exit 2; }
if [[ "$sha" =~ ^[0-9a-f]{12,} ]]; then seed=$((16#${sha:0:12})); else seed=1; fi
bin=$(mktemp -d)
started=$(date -u +%FT%TZ)
checks_json='[]'
overall=pass

want() { [ -z "${GATE_ONLY:-}" ] || [[ ",${GATE_ONLY}," == *",$1,"* ]]; }

# check <gate> <name> <command...>: run one check, log it, record pass/fail.
check() {
  local gate=$1 name=$2; shift 2
  local log="$out/$gate-$name.log" t0=$SECONDS status=pass
  if ! "$@" >"$log" 2>&1; then status=fail; overall=fail; fi
  local note; note=$(grep -m1 '^NOTE: ' "$log" | cut -c7- || true)
  printf '%-3s %-14s %-4s %4ss %s\n' "$gate" "$name" "$status" "$((SECONDS - t0))" "$note"
  [ "$status" = fail ] && tail -n 15 "$log" | sed 's/^/      | /'
  checks_json=$(jq -c --arg g "$gate" --arg n "$name" --arg s "$status" --argjson d "$((SECONDS - t0))" --arg note "$note" \
    '. + [{gate:$g, check:$n, status:$s, seconds:$d, note:$note}]' <<<"$checks_json")
}

# Directories of the root module's own Go files (nested modules and tool trees excluded).
go_dirs() { go list -f '{{.Dir}}' ./... ; }
# Every .go file that belongs to the product tree, whatever its build tags.
product_go_files() {
  find . -name '*.go' -not -path './agent_work/*' -not -path './kb/*' -not -path './.git/*' \
    -not -path '*/testdata/*' -not -path './_site/*' | sort
}
# Packages that must be deterministic and single-threaded (PLAN 2.1, 2.5). simtest is test
# plumbing (flags, environment, re-executing the test binary) and is deliberately outside.
detlint_dirs() {
  local d
  [ -d internal/env ] && echo internal/env
  if [ -d internal/sim ]; then
    find internal/sim -type d -not -path '*/testdata*' -not -path 'internal/sim/simtest*' | sort
  fi
  for d in gossip erasure merkle occ saga; do [ -d "internal/$d" ] && echo "internal/$d/..."; done
  return 0
}

# ---- G0 hygiene
g0_tools()  { (cd agent_work/gates/detlint && go build -o "$bin/" ./...) && ls "$bin"; }
g0_gofmt()  { local o; o=$(gofmt -l $(go_dirs) agent_work/gates/detlint/*.go agent_work/gates/detlint/cmd 2>&1); echo "$o"; [ -z "$o" ]; }
g0_vet()    { go vet ./...; }
g0_build()  { go build ./...; }
g0_detlint() {
  local dirs; dirs=$(detlint_dirs)
  [ -n "$dirs" ] || { echo "NOTE: no simulator or protocol packages yet"; return 0; }
  "$bin/detlint" -root "$root" -allow agent_work/gates/detlint-allow.txt $dirs
}
g0_panics() {
  [ -d internal ] || { echo "NOTE: no internal/ yet"; return 0; }
  "$bin/paniccheck" -root "$root" -allow agent_work/gates/panic-allowlist.txt internal
}
g0_deps() {
  local rc=0 req bad
  [ ! -e go.work ] || { echo "go.work present: workspaces can smuggle modules in"; rc=1; }
  [ ! -e vendor ] || { echo "vendor/ present"; rc=1; }
  req=$(go mod edit -json | jq -c '[.Require[]?.Path]')
  [ "$req" = '["pgregory.net/rapid"]' ] || { echo "go.mod must require exactly pgregory.net/rapid, has: $req"; rc=1; }
  [ "$(go mod edit -json | jq -c '[(.Replace // [])[], (.Tool // [])[]] | length')" = 0 ] || { echo "go.mod has replace or tool directives"; rc=1; }
  bad=$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./... | grep -v "^$module\(/\|$\)" || true)
  [ -z "$bad" ] || { echo "non-test build reaches packages outside the standard library and this module:"; echo "$bad"; rc=1; }
  # Every file, whatever its build tags: product files import the standard library and this
  # module only; test files may also import rapid.
  product_go_files | "$bin/importcheck" -module "$module" -test-extra pgregory.net/rapid || rc=1
  return $rc
}
g0_comments() {
  if [ "$stage" != build ]; then echo "NOTE: stage=$stage, comment rule is checked by prose-check.sh"; return 0; fi
  "$bin/nocomments" -root "$root"
}
g0_selfcontained() {
  local rc=0 hits
  hits=$(product_go_files | xargs -r grep -nE '/root/|/home/[a-z]|\bvm-1\b|objstore-p2-|/root/briefs' 2>/dev/null || true)
  [ -z "$hits" ] || { echo "machine references in Go files:"; echo "$hits"; rc=1; }
  hits=$(find cmd -name 'help.txt' 2>/dev/null | xargs -r grep -nE '/root/|/home/[a-z]|\bvm-1\b|objstore-p2-' || true)
  [ -z "$hits" ] || { echo "machine references in CLI help:"; echo "$hits"; rc=1; }
  python3 agent_work/gates/site_lint.py "$root" || rc=1
  return $rc
}

# ---- G1 unit + coverage floor
floor_pkgs='internal/sim internal/gossip internal/erasure internal/merkle internal/occ internal/saga'
g1_unit() {
  local rc=0 p line pct
  go test -count=1 -timeout 30m -cover ./... 2>&1 | tee "$bin/cover.txt"
  [ "${PIPESTATUS[0]}" = 0 ] || rc=1
  for p in $floor_pkgs; do
    [ -d "$p" ] || continue
    line=$(grep -E "^ok[[:space:]]+$module/$p[[:space:]]" "$bin/cover.txt" || true)
    pct=$(sed -n 's/.*coverage: \([0-9.]*\)% of statements.*/\1/p' <<<"$line")
    if [ -z "$pct" ]; then echo "COVERAGE $p: no figure"; rc=1
    elif awk -v p="$pct" 'BEGIN{exit !(p+0 < 85)}'; then echo "COVERAGE $p: $pct% is under the 85% floor"; rc=1
    else echo "COVERAGE $p: $pct% (floor 85%)"; fi
  done
  return $rc
}

# ---- G2 rapid properties, 2,000 checks, seed from the commit
g2_property() {
  local pkgs rc=0 p
  pkgs=$(go list -f '{{.ImportPath}} {{.TestImports}} {{.XTestImports}}' ./... | grep 'pgregory.net/rapid' | cut -d' ' -f1)
  [ -n "$pkgs" ] || { echo "NOTE: no package imports rapid"; return 0; }
  echo "NOTE: rapid seed $seed, 2000 checks"
  for p in $pkgs; do
    go test -count=1 -timeout 30m "$p" -run 'Laws|Prop' -rapid.checks=2000 -rapid.seed="$seed" || rc=1
  done
  return $rc
}

# ---- G3 fuzz, short
g3_fuzz() { FUZZ_SECONDS=${FUZZ_SECONDS:-30} agent_work/gates/fuzz-short.sh; }

# ---- G4 race
g4_race() {
  local rc=0 p
  go test -count=1 -timeout 45m -race ./... || rc=1
  for p in internal/node internal/storage test/e2e; do
    [ -d "$p" ] || continue
    go test -timeout 45m -race -count=3 "./$p/..." || rc=1
  done
  return $rc
}

# ---- G5 DST seeds: pinned seeds + 500 from the commit, replay meta-tests, canaries
g5_dst() {
  local pkgs rc=0 p
  pkgs=$(go list -f '{{.ImportPath}} {{.Imports}} {{.TestImports}} {{.XTestImports}}' ./... | grep "$module/internal/sim" | cut -d' ' -f1)
  pkgs=$(printf '%s\n%s\n' "$pkgs" "$(go list ./internal/sim/... 2>/dev/null)" | sort -u | grep -v '^$' || true)
  [ -n "$pkgs" ] || { echo "NOTE: no simulation packages yet"; return 0; }
  echo "NOTE: 500 seeds from base $seed"
  for p in $pkgs; do
    DST_SEEDS=500 DST_SEED_BASE="$seed" go test -count=1 -timeout 60m "$p" || rc=1
  done
  return $rc
}

# ---- G6 Docker environment
g6_docker() {
  echo "in_container=$in_container image=${GATE_IMAGE_ID:-unknown} $(go version)"
  [ "$in_container" = true ] || { echo "the gate did not run inside the dev container"; return 1; }
  [ -n "${GATE_IMAGE_ID:-}" ] && [ "${GATE_IMAGE_ID:-}" != unknown ] || { echo "no image ID recorded"; return 1; }
}

echo "gate: chunk=$chunk sha=$sha stage=$stage in_container=$in_container seed=$seed"
if want G0; then
  check G0 tools g0_tools
  check G0 gofmt g0_gofmt
  check G0 vet g0_vet
  check G0 build g0_build
  check G0 detlint g0_detlint
  check G0 panics g0_panics
  check G0 deps g0_deps
  check G0 comments g0_comments
  check G0 selfcontained g0_selfcontained
fi
want G1 && check G1 unit g1_unit
want G2 && check G2 property g2_property
want G3 && check G3 fuzz g3_fuzz
want G4 && check G4 race g4_race
want G5 && check G5 dst g5_dst
want G6 && check G6 docker g6_docker

partial=false; [ -z "${GATE_ONLY:-}" ] || partial=true
counts=false
if [ "$overall" = pass ] && [ "$in_container" = true ] && [ "$partial" = false ] && [ "$sha" != unknown ] \
   && [ "${GATE_DIRTY:-false}" = false ]; then counts=true; fi
jq -n --arg chunk "$chunk" --arg sha "$sha" --arg stage "$stage" --arg started "$started" \
  --arg finished "$(date -u +%FT%TZ)" --arg go "$(go version)" --arg image "${GATE_IMAGE_ID:-unknown}" \
  --arg overall "$overall" --argjson in_container "$in_container" --argjson partial "$partial" \
  --argjson dirty "${GATE_DIRTY:-false}" --argjson counts "$counts" --argjson seed "$seed" \
  --argjson fuzz "${FUZZ_SECONDS:-30}" --argjson checks "$checks_json" \
  '{chunk:$chunk, sha:$sha, stage:$stage, started:$started, finished:$finished, go_version:$go,
    image_id:$image, in_container:$in_container, partial:$partial, dirty_tree:$dirty,
    seed_from_sha:$seed, fuzz_seconds:$fuzz, overall:$overall, counts_for_landing:$counts,
    checks:$checks}' > "$out/result.json"
rm -rf "$bin"
echo "gate: overall=$overall counts_for_landing=$counts -> $out/result.json"
[ "$overall" = pass ]
