#!/bin/bash
# Testing the tester: plant one violation at a time in a throwaway copy of the tree and
# require the gate to go red on exactly the check that owns it. A clean copy must be green
# first, otherwise a red canary proves nothing.
#
# Usage: agent_work/gates/canaries.sh [name-regexp]
#   On the host it re-executes itself inside the dev container.
#   Writes agent_work/0a-foundation/gates/canaries.json. Exit 0 when every canary is caught.
set -u
here=$(cd "$(dirname "$0")" && pwd -P)
root=$(cd "$here/../.." && pwd -P)
cd "$root" || exit 2
if ! { [ -f /.dockerenv ] && [ "${OBJSTORE_DEV_CONTAINER:-}" = 1 ]; }; then
  docker compose build -q dev || exit 2
  exec docker compose run --rm -T dev agent_work/gates/canaries.sh "$@"
fi

only=${1:-.}
work=$(mktemp -d)
fake_sha=00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee
results='[]'
rc=0

fresh() { # fresh <name>: a clean copy of the tree at $work/<name>
  mkdir -p "$work/$1"
  tar -C "$root" --exclude=./.git --exclude=./kb --exclude=./_site --exclude='./agent_work/*/gates' -cf - . | tar -C "$work/$1" -xf -
}
run_gate() { # run_gate <name> <GATE_ONLY> [extra env assignments...]
  local name=$1 only_gates=$2; shift 2
  ( cd "$work/$name" && env GATE_SHA=$fake_sha GATE_DIRTY=false GATE_IMAGE_ID=sha256:canary \
      GATE_ONLY="$only_gates" GATE_OUT="$work/$name.out" "$@" agent_work/gates/gate.sh canary ) > "$work/$name.log" 2>&1
}
status_of() { jq -r --arg g "$2" --arg c "$3" '.checks[] | select(.gate==$g and .check==$c) | .status' "$work/$1.out/result.json" 2>/dev/null; }
record() { # record <name> <what was planted> <expected> <caught true|false>
  printf '%-22s %-7s %s\n' "$1" "$([ "$4" = true ] && echo CAUGHT || echo MISSED)" "$2 -> expect $3"
  [ "$4" = true ] || { rc=1; tail -n 25 "$work/$1.log" | sed 's/^/      | /'; }
  results=$(jq -c --arg n "$1" --arg p "$2" --arg e "$3" --argjson c "$4" '. + [{canary:$n, planted:$p, expects:$e, caught:$c}]' <<<"$results")
}
# canary <name> <gate> <check> <description> <GATE_ONLY> <plant-function>
canary() {
  local name=$1 gate=$2 chk=$3 desc=$4 only_gates=$5 plant=$6
  [[ "$name" =~ $only ]] || return 0
  fresh "$name"; ( cd "$work/$name" && $plant )
  run_gate "$name" "$only_gates"
  if [ "$(status_of "$name" "$gate" "$chk")" = fail ]; then record "$name" "$desc" "$gate/$chk red" true
  else record "$name" "$desc" "$gate/$chk red" false; fi
  rm -rf "$work/$name"
}

# A small deterministic package with full coverage that the test-kind canaries build on.
base_pkg() {
  mkdir -p internal/sim
  printf 'package sim\n\nfunc Double(x int) int {\n\treturn 2 * x\n}\n' > internal/sim/zz_canary.go
  printf 'package sim\n\nimport "testing"\n\nfunc TestDouble(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal("double")\n\t}\n}\n' > internal/sim/zz_canary_test.go
}

plant_gofmt()    { printf 'package buildcheck\nfunc   zzUnformatted( ) int {return 1}\n' > internal/buildcheck/zz_canary.go; }
plant_timenow()  { mkdir -p internal/sim; printf 'package sim\n\nimport "time"\n\nfunc zzNow() int64 {\n\treturn time.Now().UnixNano()\n}\n' > internal/sim/zz_canary.go; }
plant_alias()    { mkdir -p internal/sim; printf 'package sim\n\nimport clock "time"\n\nvar zzNow = clock.Now\n' > internal/sim/zz_canary.go; }
plant_goroutine(){ mkdir -p internal/sim; printf 'package sim\n\nfunc zzSpawn(f func()) {\n\tgo f()\n}\n' > internal/sim/zz_canary.go; }
plant_require()  { printf '\nrequire golang.org/x/sys v0.30.0\n' >> go.mod; }
plant_rapid()    { printf 'package buildcheck\n\nimport "pgregory.net/rapid"\n\nvar zzGen = rapid.Int()\n' > internal/buildcheck/zz_canary.go; }
plant_tagged()   { printf '//go:build never\n\npackage buildcheck\n\nimport _ "github.com/klauspost/reedsolomon"\n' > internal/buildcheck/zz_canary.go; }
plant_gowork()   { printf 'go 1.26.0\n\nuse .\n' > go.work; }
plant_panic()    { printf 'package buildcheck\n\nfunc zzBoom() {\n\tpanic("unlisted")\n}\n' > internal/buildcheck/zz_canary.go; }
plant_stale()    { printf 'internal/buildcheck/nowhere.go\tGone\tan entry that matches no panic\n' >> agent_work/gates/panic-allowlist.txt; }
plant_comment()  { printf 'package buildcheck\n\n// zzNote is a comment written before the prose stage.\nconst zzNote = 1\n' > internal/buildcheck/zz_canary.go; }
plant_docpath()  { printf '\nSee /root/Software/decentralized-object-store for the checkout.\n' >> README.md; }
plant_gopath()   { printf 'package buildcheck\n\nconst zzWhere = "/root/briefs/objstore-p2-x.md"\n' > internal/buildcheck/zz_canary.go; }
plant_coverage() { base_pkg; printf '\nfunc Untested(x int) int {\n\tif x > 3 {\n\t\treturn x - 1\n\t}\n\tif x > 2 {\n\t\treturn x - 2\n\t}\n\tif x > 1 {\n\t\treturn x - 3\n\t}\n\treturn x\n}\n' >> internal/sim/zz_canary.go; }
plant_unitfail() { base_pkg; printf '\nfunc TestZZFails(t *testing.T) {\n\tt.Fatal("planted")\n}\n' >> internal/sim/zz_canary_test.go; }
plant_prop() {
  base_pkg
  cat > internal/sim/zz_prop_test.go <<'EOF'
package sim

import (
	"flag"
	"testing"

	"pgregory.net/rapid"
)

func TestPropZZOnlyAtGateBudget(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		_ = rapid.Int().Draw(rt, "x")
		if f := flag.Lookup("rapid.checks"); f != nil && f.Value.String() == "2000" {
			rt.Fatalf("planted: fails only at the gate's 2000-check budget")
		}
	})
}
EOF
}
plant_fuzz() {
  base_pkg
  cat > internal/sim/zz_fuzz_test.go <<'EOF'
package sim

import "testing"

func FuzzZZCanary(f *testing.F) {
	f.Add([]byte("seed"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 0 && b[0] != 's' {
			t.Fatal("planted: any mutated input fails")
		}
	})
}
EOF
}
plant_race() {
  mkdir -p internal/node
  printf 'package node\n\nvar Shared int\n' > internal/node/zz_canary.go
  cat > internal/node/zz_canary_test.go <<'EOF'
package node

import (
	"sync"
	"testing"
)

func TestZZRace(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				Shared++
			}
		}()
	}
	wg.Wait()
}
EOF
}
plant_dst() {
  base_pkg
  cat > internal/sim/zz_dst_test.go <<'EOF'
package sim

import (
	"os"
	"testing"
)

func TestZZOnlyAtGateSeeds(t *testing.T) {
	if os.Getenv("DST_SEEDS") == "500" && os.Getenv("DST_SEED_BASE") != "" {
		t.Fatal("planted: fails only under the gate's 500-seed sweep")
	}
}
EOF
}

# 0. The clean tree must be green on G0, or nothing below means anything.
if [[ clean =~ $only ]]; then
  fresh clean; run_gate clean G0
  if [ "$(jq -r .overall "$work/clean.out/result.json" 2>/dev/null)" = pass ]; then record clean "nothing" "G0 green" true
  else record clean "nothing" "G0 green" false; fi
  rm -rf "$work/clean"
fi

canary gofmt        G0 gofmt         "an unformatted Go file"                                  G0 plant_gofmt
canary time-now     G0 detlint       "time.Now in a simulator package"                         G0 plant_timenow
canary time-alias   G0 detlint       "time.Now through an aliased import and a function value" G0 plant_alias
canary goroutine    G0 detlint       "a go statement in a simulator package"                   G0 plant_goroutine
canary second-require G0 deps        "a second require line in go.mod"                         G0 plant_require
canary rapid-in-product G0 deps      "rapid imported from a non-test file"                     G0 plant_rapid
canary tagged-import G0 deps         "an outside import hidden behind a build tag"             G0 plant_tagged
canary go-work      G0 deps          "a go.work file"                                          G0 plant_gowork
canary panic        G0 panics        "a panic missing from the allowlist"                      G0 plant_panic
canary stale-allow  G0 panics        "an allowlist entry that matches no panic"                G0 plant_stale
canary comment      G0 comments      "a comment in pre-prose Go"                               G0 plant_comment
canary doc-path     G0 selfcontained "a /root/ path in the README"                             G0 plant_docpath
canary go-path      G0 selfcontained "a machine path in a Go string"                           G0 plant_gopath
canary unit-fail    G1 unit          "a failing unit test"                                     G1 plant_unitfail
canary coverage     G1 unit          "a floor package under 85% coverage"                      G1 plant_coverage
canary property     G2 property      "a property that fails only at 2,000 checks"              G2 plant_prop
canary fuzz         G3 fuzz          "a fuzz target that fails on any mutated input"           G3 plant_fuzz
canary race         G4 race          "a data race"                                             G4 plant_race
canary dst-seeds    G5 dst           "a test that fails only under the 500-seed sweep"         G5 plant_dst

# A result produced outside the container must say so and must not count for landing.
if [[ outside-container =~ $only ]]; then
  fresh outside-container
  run_gate outside-container "" OBJSTORE_DEV_CONTAINER=0 GATE_NO_DOCKER=1 FUZZ_SECONDS=2
  j="$work/outside-container.out/result.json"
  if [ "$(jq -r '.in_container' "$j" 2>/dev/null)" = false ] && [ "$(jq -r '.counts_for_landing' "$j")" = false ] \
     && [ "$(status_of outside-container G6 docker)" = fail ]; then record outside-container "a full gate run outside the dev container" "in_container=false, G6 red, does not count" true
  else record outside-container "a full gate run outside the dev container" "in_container=false, G6 red, does not count" false; fi
  rm -rf "$work/outside-container"
fi
# A partial run (GATE_ONLY) must never count either.
if [[ partial-run =~ $only ]]; then
  fresh partial-run; run_gate partial-run G0,G6
  j="$work/partial-run.out/result.json"
  if [ "$(jq -r '.overall' "$j" 2>/dev/null)" = pass ] && [ "$(jq -r '.counts_for_landing' "$j")" = false ]; then record partial-run "a green run of a subset of gates" "does not count" true
  else record partial-run "a green run of a subset of gates" "does not count" false; fi
  rm -rf "$work/partial-run"
fi

mkdir -p agent_work/0a-foundation/gates
if [ "$only" = . ]; then
  jq -n --arg at "$(date -u +%FT%TZ)" --arg go "$(go version)" --argjson r "$results" \
    '{ran_at:$at, go_version:$go, canaries:$r, all_caught:([$r[].caught]|all)}' > agent_work/0a-foundation/gates/canaries.json
fi
rm -rf "$work"
echo "canaries: $(jq '[.[]|select(.caught)]|length' <<<"$results") of $(jq length <<<"$results") caught"
exit $rc
