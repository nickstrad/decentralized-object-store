#!/bin/bash
# Landing gates G0-G6 (with G5R, the real-process tier) for one chunk, run through Docker.
#
# Usage: agent_work/gates/gate.sh <chunk>
#   Called on the host: builds the dev image, then re-executes itself inside the `dev`
#   service of compose.yaml. Called inside the container: runs the gates and writes
#   agent_work/<chunk>/gates/result.json (plus one log per check, gitignored).
#
# Environment:
#   GATE_ONLY=G0,G3     run a subset (the result is then marked partial and does not count)
#   GATE_STAGE=build    (default) product Go must be comment-free (pre-prose rule), apart
#                       from the comments already in the baseline commit (earlier chunks'
#                       landed prose)
#   GATE_BASE=<commit>  the baseline for that rule (default: the merge base with main); it
#                       must be a strict ancestor of HEAD, so a build-stage run on main
#                       itself is refused (exit 2) rather than compared with itself
#   GATE_STAGE=final    after the prose stage: the comment check is replaced by prose-check.sh
#   FUZZ_SECONDS=30     per fuzz target in G3
#   GATE_SHORT=1        G1 runs `go test -short` (the CI fast lane: the end-to-end package
#                       trims its migration failure matrix to a smoke); such a result never
#                       counts for landing
#   GATE_NO_DOCKER=1    run where you are, without Docker; the result is marked
#                       in_container=false and never counts for landing
#   GATE_OUT=<dir>      where to write results (default agent_work/<chunk>/gates)
#
# A result counts for landing only when ALL of these hold: every gate ran and is green, the
# run was inside the dev container, the commit is a full SHA, the host wrapper saw a clean
# tree, the stage is valid, the fuzz budget is at least 30 s, G1 was not a short run and, at
# the build stage, the comment baseline is a named commit other than the gated one. The host wrapper supplies
# the commit, the clean-tree flag and the image ID; the file is evidence for an honest
# operator, not a tamper-proof certificate, and land.sh binds it to the branch tip.
#
# Exit status: 0 all requested gates green, 1 a gate is red, 2 usage or setup error.
set -u
set -o pipefail
unset FUZZ_ONLY
chunk=${1:-}
[ -n "$chunk" ] || { echo "usage: gate.sh <chunk>" >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd -P)
root=$(cd "$here/../.." && pwd -P)
cd "$root" || exit 2

in_container=false
if [ -f /.dockerenv ] && [ "${OBJSTORE_DEV_CONTAINER:-}" = 1 ]; then in_container=true; fi

# The comment baseline of a build-stage run: the product Go of the commit this branch
# started from, exported to $basedir. Comments that landed with earlier chunks are not this
# chunk's to answer for; everything the branch itself adds is. The baseline must be a
# strict ancestor of HEAD: a commit compared with itself would certify its own comments,
# so a build-stage run on main (or on a branch with no commits yet) is a setup error, not
# a pass. Needs git history, so it runs wherever git can read the checkout.
basedir=
base_sha=none
prepare_baseline() {
  local base=${GATE_BASE:-} head ref
  head=$(git rev-parse --verify -q 'HEAD^{commit}' 2>/dev/null) || {
    echo "gate: a build-stage run needs git history for its comment baseline, and git cannot read this checkout here." >&2
    echo "gate: run agent_work/gates/gate.sh from the host checkout (it supplies the baseline to the container)." >&2
    return 2; }
  if [ -z "$base" ]; then
    for ref in main origin/main; do
      if git rev-parse -q --verify "$ref^{commit}" >/dev/null 2>&1; then base=$(git merge-base HEAD "$ref" 2>/dev/null || true); break; fi
    done
  fi
  [ -n "$base" ] || { echo "gate: no comment baseline: there is no main branch to compare with; set GATE_BASE=<commit>" >&2; return 2; }
  base_sha=$(git rev-parse --verify -q "$base^{commit}") || { echo "gate: GATE_BASE '$base' is not a commit" >&2; return 2; }
  if [ "$base_sha" = "$head" ]; then
    echo "gate: HEAD is its own comment baseline (this is main, or a branch with no commits of its own)." >&2
    echo "gate: a build-stage run judges a chunk branch against the commit it started from. On main use" >&2
    echo "gate: GATE_STAGE=final together with ci-prose.sh, or name an earlier commit with GATE_BASE." >&2
    return 2
  fi
  git merge-base --is-ancestor "$base_sha" "$head" || { echo "gate: baseline $base_sha is not an ancestor of HEAD" >&2; return 2; }
  basedir=$(mktemp -d /tmp/gate-baseline.XXXXXX) || return 2
  git archive "$base_sha" -- . ':(exclude)kb' ':(exclude)agent_work' | tar -x -C "$basedir" || { echo "gate: cannot export the baseline $base_sha" >&2; return 2; }
  chmod -R a+rX "$basedir"
}
bin=
trap 'rm -rf "$basedir" "$bin"' EXIT

# ---------------------------------------------------------------- host side
if [ "$in_container" = false ] && [ "${GATE_NO_DOCKER:-}" != 1 ]; then
  sha=$(git rev-parse HEAD 2>/dev/null || echo unknown)
  dirty=false
  if [ -n "$(git status --porcelain 2>/dev/null | grep -v ' agent_work/[^/]*/gates/' )" ]; then dirty=true; fi
  mount=()
  if [ "${GATE_STAGE:-build}" = build ]; then
    prepare_baseline || exit 2
    mount=(-v "$basedir:/gate-baseline:ro" -e GATE_BASELINE=/gate-baseline -e GATE_BASE_SHA="$base_sha")
  fi
  echo "gate: building the dev image (G6)"
  docker compose build -q dev || { echo "gate: image build failed" >&2; exit 2; }
  image_id=$(docker image inspect objstore-dev:local --format '{{.Id}}' 2>/dev/null || echo unknown)
  docker compose run --rm -T "${mount[@]}" \
    -e GATE_SHA="$sha" -e GATE_DIRTY="$dirty" -e GATE_IMAGE_ID="$image_id" \
    -e GATE_ONLY="${GATE_ONLY:-}" -e GATE_STAGE="${GATE_STAGE:-build}" \
    -e FUZZ_SECONDS="${FUZZ_SECONDS:-30}" -e GATE_OUT="${GATE_OUT:-}" \
    -e GATE_SHORT="${GATE_SHORT:-}" \
    dev agent_work/gates/gate.sh "$chunk"
  rc=$?
  # G7, host side: the compose `cluster` profile must start three peers that join, serve an
  # object across nodes and refuse a stale conditional write. It needs the Docker daemon,
  # so it cannot run inside the dev container with the other gates; the host wrapper runs
  # it after a FULL container run and writes its verdict into the same result. Once the
  # node binary exists a full run without this check does not count for landing.
  outdir=${GATE_OUT:-agent_work/$chunk/gates}
  smoke=${GATE_CLUSTER_SMOKE-agent_work/06-integration/cluster-smoke.sh}
  if [ -z "${GATE_ONLY:-}" ] && [ -d cmd/objnode ] && [ -f "$outdir/result.json" ]; then
    t0=$SECONDS; status=pass; note=
    if [ -n "$smoke" ] && [ -x "$smoke" ]; then
      "$smoke" > "$outdir/G7-cluster.log" 2>&1 || status=fail
    else
      echo "no executable cluster smoke script at '$smoke'" > "$outdir/G7-cluster.log"; status=fail
    fi
    printf '%-3s %-14s %-4s %4ss %s\n' G7 cluster "$status" "$((SECONDS - t0))" "$note"
    [ "$status" = pass ] || tail -n 25 "$outdir/G7-cluster.log" | sed 's/^/      | /'
    tmp=$(mktemp) || exit 2
    jq --arg s "$status" --argjson d "$((SECONDS - t0))" \
      '.checks += [{gate:"G7", check:"cluster", status:$s, seconds:$d, note:""}]
       | if $s == "fail" then .overall = "fail" | .counts_for_landing = false else . end' \
      "$outdir/result.json" > "$tmp" && cat "$tmp" > "$outdir/result.json"
    jrc=$?; rm -f "$tmp"
    [ $jrc = 0 ] || { echo "gate: cannot record the cluster check" >&2; exit 2; }
    [ "$status" = pass ] || rc=1
    echo "gate: after the host-side cluster check: overall=$(jq -r .overall "$outdir/result.json") counts_for_landing=$(jq -r .counts_for_landing "$outdir/result.json")"
  fi
  exit $rc
fi

# ---------------------------------------------------------------- gate side
sha=${GATE_SHA:-unknown}
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || sha=unknown
stage=${GATE_STAGE:-build}
case "$stage" in build|final) ;; *) echo "gate: GATE_STAGE must be build or final, got '$stage'" >&2; exit 2 ;; esac
# A build-stage run that includes G0 needs the comment baseline. The host wrapper and the
# canaries hand one in; called directly (inside the container, or with GATE_NO_DOCKER) the
# gate works it out from git itself, and stops if it cannot.
if [ "$stage" = build ] && { [ -z "${GATE_ONLY:-}" ] || [[ ",${GATE_ONLY}," == *",G0,"* ]]; }; then
  if [ -z "${GATE_BASELINE:-}" ]; then
    prepare_baseline || exit 2
    GATE_BASELINE=$basedir; GATE_BASE_SHA=$base_sha
  fi
  [ -d "$GATE_BASELINE" ] || { echo "gate: GATE_BASELINE is not a directory: $GATE_BASELINE" >&2; exit 2; }
  [ "${GATE_BASE_SHA:-none}" != "$sha" ] || { echo "gate: the comment baseline is the gated commit itself ($sha); nothing can be certified against itself" >&2; exit 2; }
fi
fuzz_seconds=${FUZZ_SECONDS:-30}
[[ "$fuzz_seconds" =~ ^[0-9]+$ ]] || { echo "gate: FUZZ_SECONDS must be a whole number" >&2; exit 2; }
short=false; short_flag=()
if [ "${GATE_SHORT:-}" = 1 ]; then short=true; short_flag=(-short); fi
dirty=${GATE_DIRTY:-true}
[ "$dirty" = false ] || dirty=true
out=${GATE_OUT:-agent_work/$chunk/gates}
mkdir -p "$out" || exit 2
rm -f "$out"/G*.log "$out/result.json"
module=$(go list -m 2>/dev/null)
[ -n "$module" ] || { echo "gate: not in a Go module" >&2; exit 2; }
if [ "$sha" != unknown ]; then seed=$((16#${sha:0:12})); else seed=1; fi
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
# Files and directories that are not part of the product tree.
not_product=(-not -path './agent_work/*' -not -path './kb/*' -not -path './.git/*' -not -path './.kb/*' -not -path './_site/*')
# Every .go file that belongs to the product tree, whatever its build tags.
product_go_files() {
  find . -name '*.go' -not -path './agent_work/*' -not -path './kb/*' -not -path './.git/*' \
    -not -path '*/testdata/*' -not -path './_site/*' | sort
}
# Packages that must be deterministic and single-threaded (PLAN 2.1, 2.5). simtest is test
# plumbing (flags, environment, re-executing the test binary) and is deliberately outside.
# So are _test.go files: a test may read the wall clock for a timeout; test-side
# nondeterminism that reaches a simulation is caught by the replay meta-tests in G5.
# The list is every directory under the roots that holds a Go file (found on disk, so a
# directory the go tool sees no package in, such as one whose only file is behind a build
# tag, is still linted) plus everything in this module those packages import, directly or
# not, so a helper package cannot hide a clock.
detlint_dirs() {
  local d roots=() dirs pkgs deps
  for d in env sim gossip erasure merkle occ saga node; do [ -d "internal/$d" ] && roots+=("internal/$d"); done
  [ ${#roots[@]} -gt 0 ] || return 0
  dirs=$(find "${roots[@]}" -type f -name '*.go' -not -path '*/testdata/*' -not -path 'internal/sim/simtest/*' -printf '%h\n' | sort -u)
  [ -n "$dirs" ] || return 0
  pkgs=$(sed 's#^#./#' <<<"$dirs")
  # Directories with no buildable package make go list fail; -e keeps the rest listed.
  deps=$(go list -e -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' $pkgs) || return 1
  { echo "$dirs"; grep "^$module/" <<<"$deps" | sed "s#^$module/##"; } | sort -u
}

# ---- G0 hygiene
g0_tools()  { (cd agent_work/gates/detlint && go build -o "$bin/" ./...) && ls "$bin"; }
g0_gofmt()  { local o dirs; dirs=$(go_dirs) || return 1; o=$(gofmt -l $dirs agent_work/gates/detlint 2>&1); echo "$o"; [ -z "$o" ]; }
g0_vet()    { go vet ./...; }
g0_build()  { go build ./...; }
g0_detlint() {
  local dirs; dirs=$(detlint_dirs) || { echo "cannot list the packages to lint"; return 1; }
  [ -n "$dirs" ] || { echo "NOTE: no simulator or protocol packages yet"; return 0; }
  "$bin/detlint" -root "$root" -allow agent_work/gates/detlint-allow.txt $dirs
}
g0_panics() {
  [ -d internal ] || { echo "NOTE: no internal/ yet"; return 0; }
  "$bin/paniccheck" -root "$root" -allow agent_work/gates/panic-allowlist.txt internal
}
g0_deps() {
  local rc=0 req bad deps
  [ ! -e go.work ] || { echo "go.work present: workspaces can smuggle modules in"; rc=1; }
  [ ! -e vendor ] || { echo "vendor/ present"; rc=1; }
  bad=$(find . -name go.mod -not -path ./go.mod "${not_product[@]}")
  [ -z "$bad" ] || { echo "go.mod outside the root, agent_work/ and kb/ (a nested module hides code from every check):"; echo "$bad"; rc=1; }
  bad=$(find . -type f \( -name '*.s' -o -name '*.S' -o -name '*.c' -o -name '*.h' -o -name '*.cc' -o -name '*.cpp' -o -name '*.m' -o -name '*.syso' -o -name '*.swig' \) "${not_product[@]}" -not -path '*/testdata/*')
  [ -z "$bad" ] || { echo "assembly, C or object files in the product tree (no check can read them):"; echo "$bad"; rc=1; }
  bad=$(find . -type l "${not_product[@]}")
  [ -z "$bad" ] || { echo "symlinks in the product tree:"; echo "$bad"; rc=1; }
  req=$(go mod edit -json | jq -c '[.Require[]?.Path]') || rc=1
  [ "$req" = '["pgregory.net/rapid"]' ] || { echo "go.mod must require exactly pgregory.net/rapid, has: $req"; rc=1; }
  [ "$(go mod edit -json | jq -c '[(.Replace // [])[], (.Tool // [])[]] | length')" = 0 ] || { echo "go.mod has replace or tool directives"; rc=1; }
  deps=$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./...) || { echo "go list -deps failed"; return 1; }
  bad=$(grep -v "^$module\(/\|$\)" <<<"$deps" || true)
  [ -z "$bad" ] || { echo "non-test build reaches packages outside the standard library and this module:"; echo "$bad"; rc=1; }
  # Every file, whatever its build tags: product files import the standard library and this
  # module only; test files may also import rapid.
  product_go_files | "$bin/importcheck" -module "$module" -test-extra pgregory.net/rapid || rc=1
  return $rc
}
g0_comments() {
  if [ "$stage" != build ]; then echo "NOTE: stage=$stage, comment rule is checked by prose-check.sh"; return 0; fi
  echo "NOTE: comments compared with baseline ${GATE_BASE_SHA:-unnamed}"
  "$bin/nocomments" -root "$root" -baseline "$GATE_BASELINE"
}
g0_selfcontained() {
  local rc=0 hits
  hits=$(product_go_files | xargs -r grep -nE '/root/|/home/[a-z]|\bvm-1\b|objstore-p2-|/root/briefs' 2>/dev/null || true)
  [ -z "$hits" ] || { echo "machine references in Go files:"; echo "$hits"; rc=1; }
  hits=$(find cmd -name 'help.txt' 2>/dev/null | xargs -r grep -nE '/root/|/home/[a-z]|\bvm-1\b|objstore-p2-' || true)
  [ -z "$hits" ] || { echo "machine references in CLI help:"; echo "$hits"; rc=1; }
  python3 agent_work/gates/site_lint.py "$root" || rc=1
  python3 agent_work/gates/site_lint.py --selftest || { echo "site-lint self-test failed"; rc=1; }
  return $rc
}

# ---- G1 unit + coverage floor
floor_pkgs='internal/sim internal/gossip internal/erasure internal/storage internal/merkle internal/occ internal/saga internal/node internal/noderuntime internal/storage/httpapi'
g1_unit() {
  local rc=0 p line pct
  [ "$short" = false ] || echo "NOTE: short run (go test -short); does not count for landing"
  go test -count=1 -timeout 30m "${short_flag[@]}" -cover ./... > "$bin/cover.txt" 2>&1 || rc=1
  cat "$bin/cover.txt"
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

# ---- G2 rapid properties, 2,000 checks, seed from the commit.
# Every test of every rapid-importing package runs at the gate budget: selecting by name
# ('Laws|Prop') would let a property with another name skip the budget unnoticed.
g2_property() {
  local all pkgs rc=0 p
  all=$(go list -f '{{.ImportPath}} {{.TestImports}} {{.XTestImports}}' ./...) || { echo "go list failed"; return 1; }
  pkgs=$(grep 'pgregory.net/rapid' <<<"$all" | cut -d' ' -f1 || true)
  [ -n "$pkgs" ] || { echo "NOTE: no package imports rapid"; return 0; }
  echo "NOTE: rapid seed $seed, 2000 checks, $(wc -w <<<"$pkgs") packages"
  for p in $pkgs; do
    go test -count=1 -timeout 60m "$p" -rapid.checks=2000 -rapid.seed="$seed" || rc=1
  done
  return $rc
}

# ---- G3 fuzz, short
g3_fuzz() { FUZZ_SECONDS=$fuzz_seconds agent_work/gates/fuzz-short.sh; }

# ---- G4 race
g4_race() {
  local rc=0 p
  go test -count=1 -timeout 45m -race ./... || rc=1
  for p in internal/node internal/noderuntime internal/storage cmd/objnode test/e2e; do # internal/storage/... includes httpapi
    [ -d "$p" ] || continue
    go test -timeout 45m -race -count=3 "./$p/..." || rc=1
  done
  return $rc
}

# ---- G5 DST seeds: pinned seeds + 500 from the commit, replay meta-tests, canaries.
# The whole module runs under the sweep variables: picking packages by their imports would
# miss a scenario that reaches the simulator through another package.
g5_dst() {
  [ -d internal/sim ] || { echo "NOTE: no simulator yet"; return 0; }
  echo "NOTE: 500 seeds from base $seed"
  DST_SEEDS=500 DST_SEED_BASE="$seed" go test -count=1 -timeout 120m ./...
}

# ---- G5R the end-to-end scenarios on real objnode processes (tier R). The tests skip
# unless E2E_TIER_R=1, so a run in which any selected test skipped, or none passed, is red:
# a silently skipped tier must not read as a pass. They build objnode themselves (race
# detector on, fault hook compiled in) and are bound by real timeouts, so nothing else
# heavy may run beside them; the gate runs its checks one at a time.
g5r_tierr() {
  [ -d test/e2e ] || { echo "NOTE: no end-to-end suite yet"; return 0; }
  local rc=0 passed skipped waited=0 limit load
  # These tests are bound by real one-second request timeouts and the nodes are race
  # builds. Run straight after the hour-long sweep they failed twice (two PUTs answered
  # 503 on a shard request timeout; the second time with the load already low), and they
  # passed every time they ran alone or after ten minutes of heavy tests. The cause after
  # sustained load is not established (the hypervisor taking CPU is suspected, not
  # measured). So the step runs first among the test gates, and before it the gate flushes
  # dirty pages and waits, for at most five minutes, for the one-minute load to fall under
  # three quarters of the cores. This is scheduling and a wait, not a retry: the tests
  # still run exactly once, and a failure is still a red gate.
  sync
  limit=$(( $(nproc) * 75 ))
  while :; do
    load=$(awk '{printf "%d", $1 * 100}' /proc/loadavg)
    [ "$load" -ge "$limit" ] && [ "$waited" -lt 300 ] || break
    sleep 10; waited=$((waited + 10))
  done
  echo "settled after ${waited}s (one-minute load $(cut -d' ' -f1 /proc/loadavg), $(nproc) cores)"
  E2E_TIER_R=1 go test ./test/e2e -run 'R$|TierR|DefaultBuild|CanaryE9' -count=1 -v -timeout 40m > "$bin/tierr.txt" 2>&1 || rc=1
  cat "$bin/tierr.txt"
  passed=$(grep -cE '^--- PASS: ' "$bin/tierr.txt" || true)
  skipped=$(grep -cE '^[[:space:]]*--- SKIP: ' "$bin/tierr.txt" || true)
  [ "$skipped" = 0 ] || { echo "TIER R: $skipped tests skipped"; rc=1; }
  [ "$passed" -gt 0 ] || { echo "TIER R: no test passed"; rc=1; }
  echo "NOTE: $passed real-process tests passed, $skipped skipped, settle wait ${waited}s"
  return $rc
}

# ---- G6 Docker environment
g6_docker() {
  echo "in_container=$in_container image=${GATE_IMAGE_ID:-unknown} $(go version)"
  [ "$in_container" = true ] || { echo "the gate did not run inside the dev container"; return 1; }
  [[ "${GATE_IMAGE_ID:-}" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "no image ID recorded"; return 1; }
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
# G5R runs before the long test gates, not after them: see g5r_tierr.
want G5R && check G5R tierR g5r_tierr
want G1 && check G1 unit g1_unit
want G2 && check G2 property g2_property
want G3 && check G3 fuzz g3_fuzz
want G4 && check G4 race g4_race
want G5 && check G5 dst g5_dst
want G6 && check G6 docker g6_docker

partial=false; [ -z "${GATE_ONLY:-}" ] || partial=true
counts=false
if [ "$overall" = pass ] && [ "$in_container" = true ] && [ "$partial" = false ] && [ "$sha" != unknown ] \
   && [ "$dirty" = false ] && [ "$fuzz_seconds" -ge 30 ] && [ "$short" = false ] \
   && { [ "$stage" = final ] || [[ "${GATE_BASE_SHA:-}" =~ ^[0-9a-f]{40}$ ]]; }; then counts=true; fi
jq -n --arg chunk "$chunk" --arg sha "$sha" --arg stage "$stage" --arg started "$started" \
  --arg finished "$(date -u +%FT%TZ)" --arg go "$(go version)" --arg image "${GATE_IMAGE_ID:-unknown}" \
  --arg overall "$overall" --argjson in_container "$in_container" --argjson partial "$partial" \
  --argjson dirty "$dirty" --argjson counts "$counts" --argjson seed "$seed" \
  --argjson fuzz "$fuzz_seconds" --argjson short "$short" --argjson checks "$checks_json" --arg cbase "${GATE_BASE_SHA:-none}" \
  '{chunk:$chunk, sha:$sha, stage:$stage, comment_baseline:$cbase, started:$started, finished:$finished, go_version:$go,
    image_id:$image, in_container:$in_container, partial:$partial, dirty_tree:$dirty,
    seed_from_sha:$seed, fuzz_seconds:$fuzz, short:$short, overall:$overall, counts_for_landing:$counts,
    checks:$checks}' > "$out/result.json"
echo "gate: overall=$overall counts_for_landing=$counts -> $out/result.json"
[ "$overall" = pass ]
