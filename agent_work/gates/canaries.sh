#!/bin/bash
# Testing the tester: plant one violation at a time in a throwaway copy of the tree and
# require the gate to go red on exactly the check that owns it, with the expected message in
# that check's log (so a compile error cannot pass for the planted rule). A clean copy must
# be green on every gate first, otherwise a red canary proves nothing.
#
# Usage: agent_work/gates/canaries.sh [name-regexp]
#   On the host it re-executes itself inside the dev container.
#   CANARY_FAST=1 runs only the canaries that need gates G0 and G6 (a few minutes in
#   all): the ones a push can break by changing the PRODUCT tree under the plants. The
#   full set contains several complete gate runs and takes hours on the grown tree; CI
#   runs it from its own workflow when the tester changes, weekly, and on demand.
#   Writes agent_work/0a-foundation/gates/canaries.json. Exit 0 when every canary is caught.
set -u
here=$(cd "$(dirname "$0")" && pwd -P)
root=$(cd "$here/../.." && pwd -P)
cd "$root" || exit 2
if ! { [ -f /.dockerenv ] && [ "${OBJSTORE_DEV_CONTAINER:-}" = 1 ]; }; then
  docker compose build -q dev || exit 2
  exec docker compose run --rm -T -e CANARY_FAST="${CANARY_FAST:-}" dev agent_work/gates/canaries.sh "$@"
fi

only=${1:-.}
fast=${CANARY_FAST:-}
work=$(mktemp -d)
fake_sha=00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee
fake_base=0ba5e0000ba5e0000ba5e0000ba5e0000ba5e000
fake_image=sha256:$(printf 'c%.0s' $(seq 64))
results='[]'
rc=0

fresh() { # fresh <name>: a clean copy of the tree at $work/<name>; a failed copy stops the run
  mkdir -p "$work/$1" || { echo "canaries: cannot create $work/$1" >&2; exit 2; }
  tar -C "$root" --exclude=./.git --exclude=./kb --exclude=./_site --exclude='./agent_work/*/gates' -cf - . | tar -C "$work/$1" -xf -
  local st=("${PIPESTATUS[@]}")
  [ "${st[0]}" = 0 ] && [ "${st[1]}" = 0 ] || { echo "canaries: copying the tree for $1 failed" >&2; exit 2; }
}
# plant_in <name> <plant-function>: run the plant in the copy with errexit, so a plant
# that cannot write (a missing directory, a renamed file) fails here and is reported as a
# MISSED canary with its own error, instead of leaving a clean copy that some canaries
# would then "catch" for the wrong reason. Never call this inside if/&&/||: errexit is
# switched off there. Sets plant_rc.
plant_in() {
  ( set -euo pipefail
    cd "$work/$1"
    "$2"
  ) > "$work/$1.plant.log" 2>&1
  plant_rc=$?
  if [ $plant_rc != 0 ]; then
    { echo "PLANT FAILED ($2, exit $plant_rc):"; cat "$work/$1.plant.log"; } > "$work/$1.log"
  fi
}
# The comment baseline of every canary run is the unmutated tree, so prose that has landed
# with earlier chunks does not turn the clean run red, and a comment canary is caught for
# the comment it plants and nothing else.
run_gate() { # run_gate <name> <GATE_ONLY> [extra env assignments...]
  local name=$1 only_gates=$2; shift 2
  ( cd "$work/$name" && env GATE_SHA=$fake_sha GATE_DIRTY=false GATE_IMAGE_ID=$fake_image \
      GATE_BASELINE="$root" GATE_BASE_SHA=$fake_base \
      GATE_ONLY="$only_gates" GATE_OUT="$work/$name.out" "$@" agent_work/gates/gate.sh canary ) > "$work/$name.log" 2>&1
}
status_of() { jq -r --arg g "$2" --arg c "$3" '.checks[] | select(.gate==$g and .check==$c) | .status' "$work/$1.out/result.json" 2>/dev/null; }
record() { # record <name> <what was planted> <expected> <caught true|false>
  printf '%-22s %-7s %s\n' "$1" "$([ "$4" = true ] && echo CAUGHT || echo MISSED)" "$2 -> expect $3"
  [ "$4" = true ] || { rc=1; tail -n 25 "$work/$1.log" | sed 's/^/      | /'; }
  results=$(jq -c --arg n "$1" --arg p "$2" --arg e "$3" --argjson c "$4" '. + [{canary:$n, planted:$p, expects:$e, caught:$c}]' <<<"$results")
}
# canary <name> <gate> <check> <log-pattern> <description> <GATE_ONLY> <plant-function>
canary() {
  local name=$1 gate=$2 chk=$3 pat=$4 desc=$5 only_gates=$6 plant=$7
  [[ "$name" =~ $only ]] || return 0
  if [ "$fast" = 1 ] && [ "$only_gates" != G0 ] && [ "$only_gates" != G6 ]; then return 0; fi
  fresh "$name"; plant_in "$name" "$plant"
  if [ $plant_rc != 0 ]; then
    record "$name" "$desc" "$gate/$chk red with /$pat/ (the plant itself failed)" false
    rm -rf "$work/$name" "$work/$name.out"; return 0
  fi
  run_gate "$name" "$only_gates"
  if [ "$(status_of "$name" "$gate" "$chk")" = fail ] && grep -qE -- "$pat" "$work/$name.out/$gate-$chk.log"; then
    record "$name" "$desc" "$gate/$chk red with /$pat/" true
  else record "$name" "$desc" "$gate/$chk red with /$pat/" false; fi
  rm -rf "$work/$name" "$work/$name.out"
}
# counts <name> <description> <expect counts true|false> [env...]: a full run of every gate
counts() {
  local name=$1 desc=$2 want=$3; shift 3
  [[ "$name" =~ $only ]] || return 0
  [ "$fast" != 1 ] || return 0
  fresh "$name"; run_gate "$name" "" "$@"
  local j="$work/$name.out/result.json"
  if [ "$(jq -r '.overall' "$j" 2>/dev/null)" = pass ] && [ "$(jq -r '.counts_for_landing' "$j")" = "$want" ]; then
    record "$name" "$desc" "all gates green, counts_for_landing=$want" true
  else record "$name" "$desc" "all gates green, counts_for_landing=$want" false; fi
  rm -rf "$work/$name" "$work/$name.out"
}

# A small deterministic package with full coverage that the test-kind canaries build on.
base_pkg() {
  mkdir -p internal/sim
  printf 'package sim\n\nfunc Double(x int) int {\n\treturn 2 * x\n}\n' > internal/sim/zz_canary.go
  printf 'package sim\n\nimport "testing"\n\nfunc TestDouble(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal("double")\n\t}\n}\n' > internal/sim/zz_canary_test.go
}

plant_gofmt()    { mkdir -p internal/buildcheck; printf 'package buildcheck\nfunc   zzUnformatted( ) int {return 1}\n' > internal/buildcheck/zz_canary.go; }
plant_timenow()  { mkdir -p internal/sim; printf 'package sim\n\nimport "time"\n\nfunc zzNow() int64 {\n\treturn time.Now().UnixNano()\n}\n' > internal/sim/zz_canary.go; }
plant_alias()    { mkdir -p internal/sim; printf 'package sim\n\nimport clock "time"\n\nvar zzNow = clock.Now\n' > internal/sim/zz_canary.go; }
plant_goroutine(){ mkdir -p internal/sim; printf 'package sim\n\nfunc zzSpawn(f func()) {\n\tgo f()\n}\n' > internal/sim/zz_canary.go; }
plant_require()  { printf '\nrequire golang.org/x/sys v0.30.0\n' >> go.mod; }
plant_rapid()    { mkdir -p internal/buildcheck; printf 'package buildcheck\n\nimport "pgregory.net/rapid"\n\nvar zzGen = rapid.Int()\n' > internal/buildcheck/zz_canary.go; }
plant_tagged()   { mkdir -p internal/buildcheck; printf '//go:build never\n\npackage buildcheck\n\nimport _ "github.com/klauspost/reedsolomon"\n' > internal/buildcheck/zz_canary.go; }
plant_gowork()   { printf 'go 1.26.0\n\nuse .\n' > go.work; }
plant_panic()    { mkdir -p internal/buildcheck; printf 'package buildcheck\n\nfunc zzBoom() {\n\tpanic("unlisted")\n}\n' > internal/buildcheck/zz_canary.go; }
plant_stale()    { printf 'internal/buildcheck/nowhere.go\tGone\tan entry that matches no panic\n' >> agent_work/gates/panic-allowlist.txt; }
plant_comment()  { mkdir -p internal/buildcheck; printf 'package buildcheck\n\n// zzNote is a comment written before the prose stage.\nconst zzNote = 1\n' > internal/buildcheck/zz_canary.go; }
plant_docpath()  { printf '\nSee /root/Software/decentralized-object-store for the checkout.\n' >> README.md; }
plant_gopath()   { mkdir -p internal/buildcheck; printf 'package buildcheck\n\nconst zzWhere = "/root/briefs/objstore-p2-x.md"\n' > internal/buildcheck/zz_canary.go; }
plant_import()   { mkdir -p internal/sim; printf 'package sim\n\nimport "sync"\n\nvar zzMu sync.Mutex\n' > internal/sim/zz_canary.go; }
plant_reflect()  { mkdir -p internal/sim; printf 'package sim\n\nimport "reflect"\n\nfunc zzKeys(m map[string]int) []reflect.Value {\n\treturn reflect.ValueOf(m).MapKeys()\n}\n' > internal/sim/zz_canary.go; }
plant_helper() {
  mkdir -p internal/sim internal/zzhelper
  printf 'package zzhelper\n\nimport "time"\n\nfunc Now() int64 {\n\treturn time.Now().UnixNano()\n}\n' > internal/zzhelper/zz.go
  printf 'package sim\n\nimport "github.com/nickstrad/decentralized-object-store/internal/zzhelper"\n\nfunc zzNow() int64 {\n\treturn zzhelper.Now()\n}\n' > internal/sim/zz_canary.go
}
plant_tagclock() { mkdir -p internal/sim; printf 'package sim\n' > internal/sim/zz_base.go; printf '//go:build missingtag\n\npackage sim\n\nimport "time"\n\nvar zzAttack = time.Now()\n' > internal/sim/zz_canary.go; }
plant_tagonly()  { mkdir -p internal/sim; find internal/sim -maxdepth 1 -type f -name '*.go' -delete; printf '//go:build missingtag\n\npackage sim\n\nimport "time"\n\nvar zzAttack = time.Now()\n' > internal/sim/zz_canary.go; }
plant_ptrfmt()   { mkdir -p internal/sim; printf 'package sim\n\nimport "fmt"\n\nfunc zzAddr(p *int) string {\n\treturn fmt.Sprintf("%%v", p)\n}\n' > internal/sim/zz_canary.go; }
plant_sortties() { mkdir -p internal/sim; printf 'package sim\n\nimport "sort"\n\nfunc zzKeys(m map[string]int) []string {\n\tvar a []string\n\tfor k := range m {\n\t\ta = append(a, k)\n\t}\n\tsort.Slice(a, func(i, j int) bool { return false })\n\treturn a\n}\n' > internal/sim/zz_canary.go; }
plant_asm()      { mkdir -p internal/buildcheck; printf 'package buildcheck\n\nfunc zzTicks() uint64\n' > internal/buildcheck/zz_canary.go; printf '#include "textflag.h"\n\nTEXT ·zzTicks(SB), NOSPLIT, $0-8\n\tRDTSC\n\tSHLQ $32, DX\n\tORQ DX, AX\n\tMOVQ AX, ret+0(FP)\n\tRET\n' > internal/buildcheck/zz_canary_amd64.s; }
plant_nestedmod(){ mkdir -p extras; printf 'module example.org/extra\n\ngo 1.26.0\n\nrequire example.org/evil v1.0.0\n' > extras/go.mod; printf 'package extra\n' > extras/x.go; }
plant_shadow()   { mkdir -p internal/buildcheck; printf '//go:build never\n\npackage buildcheck\n\nfunc panic(any) {}\n' > internal/buildcheck/zz_shadow.go; printf 'package buildcheck\n\nfunc zzBoom() {\n\tpanic("unlisted")\n}\n' > internal/buildcheck/zz_canary.go; }
plant_fakedir()  { mkdir -p internal/buildcheck; printf 'package buildcheck\n\n//go: this is prose dressed as a directive\nconst zzNote = 1\n' > internal/buildcheck/zz_canary.go; }
plant_generated(){ mkdir -p internal/buildcheck; printf '// Code generated by a human who wanted to write prose. DO NOT EDIT.\n\npackage buildcheck\n\nconst zzNote = 1\n' > internal/buildcheck/zz_canary.go; }
plant_vet()      { mkdir -p internal/buildcheck; printf 'package buildcheck\n\nimport "fmt"\n\nfunc zzVet() string {\n\treturn fmt.Sprintf("%%d", "not a number")\n}\n' > internal/buildcheck/zz_canary.go; }
plant_build()    { mkdir -p internal/buildcheck; printf 'package buildcheck\n\nfunc zzBroken() int {\n\treturn zzUndefined\n}\n' > internal/buildcheck/zz_canary.go; }
plant_badlink()  { mkdir -p docs; printf '# Front page\n\n[missing](nowhere.md)\n' > docs/index.md; }
plant_symlink()  { mkdir -p book/chapters; ln -s ../../STATE.md book/chapters/zz-private.md; }
plant_unnamed() {
  base_pkg
  cat > internal/sim/zz_prop_test.go <<'EOT'
package sim

import (
	"flag"
	"testing"

	"pgregory.net/rapid"
)

func TestZZInvariantWithoutThePropName(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		_ = rapid.Int().Draw(rt, "x")
		if f := flag.Lookup("rapid.checks"); f != nil && f.Value.String() == "2000" {
			rt.Fatalf("planted: fails only at the gate's 2000-check budget")
		}
	})
}
EOT
}
plant_unifuzz() {
  base_pkg
  cat > internal/sim/zz_fuzz_test.go <<'EOT'
package sim

import "testing"

func FuzzÉcanary(f *testing.F) {
	f.Add([]byte("seed"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 0 && b[0] != 's' {
			t.Fatal("planted: any mutated input fails")
		}
	})
}
EOT
}
plant_indirect() {
  base_pkg
  mkdir -p internal/zzbridge internal/zzscenario
  printf 'package zzbridge\n\nimport "github.com/nickstrad/decentralized-object-store/internal/sim"\n\nfunc Twice(x int) int {\n\treturn sim.Double(x)\n}\n' > internal/zzbridge/zz.go
  printf 'package zzscenario\n' > internal/zzscenario/zz.go
  cat > internal/zzscenario/zz_test.go <<'EOT'
package zzscenario

import (
	"os"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/zzbridge"
)

func TestZZIndirectScenario(t *testing.T) {
	if zzbridge.Twice(2) == 4 && os.Getenv("DST_SEEDS") == "500" {
		t.Fatal("planted: fails only under the gate's 500-seed sweep")
	}
}
EOT
}
# One uncovered statement per existing line of the package (never fewer than 8), so the
# planted function drags coverage under the floor however large the real package has grown.
plant_coverage() {
  local n; n=$(cat internal/sim/*.go 2>/dev/null | wc -l)
  base_pkg
  [ "$n" -ge 8 ] || n=8
  { printf '\nfunc Untested(x int) int {\n'; for _ in $(seq "$n"); do printf '\tx++\n'; done; printf '\treturn x\n}\n'; } >> internal/sim/zz_canary.go
}
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

# 0. The clean tree must be green on EVERY gate and count for landing, or nothing below
#    means anything. This is also the positive control for counts_for_landing.
counts clean "nothing (well-formed commit, clean tree, in the container)" true

# Prose that landed with an earlier chunk (a comment present in the baseline too) must not
# turn a build-stage run red: the comment rule is about what this chunk adds.
if [[ landed-prose =~ $only ]] && [ "$fast" != 1 ]; then
  fresh landed-prose; plant_in landed-prose plant_comment
  cp -a "$work/landed-prose" "$work/landed-prose.base"
  [ $plant_rc != 0 ] || run_gate landed-prose "" GATE_BASELINE="$work/landed-prose.base"
  j="$work/landed-prose.out/result.json"
  if [ $plant_rc = 0 ] && [ "$(jq -r '.overall' "$j" 2>/dev/null)" = pass ] && [ "$(jq -r '.counts_for_landing' "$j")" = true ]; then record landed-prose "a comment that is also in the baseline commit" "all gates green, counts_for_landing=true" true
  else record landed-prose "a comment that is also in the baseline commit" "all gates green, counts_for_landing=true" false; fi
  rm -rf "$work/landed-prose" "$work/landed-prose.out" "$work/landed-prose.base"
fi

#      name             gate check          log pattern                    planted                                                    gates plant
canary gofmt            G0 gofmt           'zz_canary.go'                 "an unformatted Go file"                                   G0 plant_gofmt
canary vet              G0 vet             'Sprintf format'               "a go vet failure"                                         G0 plant_vet
canary build            G0 build           'undefined: zzUndefined'       "a compile error"                                          G0 plant_build
canary time-now         G0 detlint         'wallclock'                    "time.Now in a simulator package"                          G0 plant_timenow
canary time-alias       G0 detlint         'wallclock'                    "time.Now through an aliased import and a function value"  G0 plant_alias
canary goroutine        G0 detlint         'concurrency'                  "a go statement in a simulator package"                    G0 plant_goroutine
canary banned-import    G0 detlint         ' import: '                    "package sync imported by a simulator package"             G0 plant_import
canary reflect          G0 detlint         ' import: '                    "map keys read through reflect"                            G0 plant_reflect
canary helper-package   G0 detlint         'zzhelper.*wallclock'          "a clock hidden in a helper package the simulator imports" G0 plant_helper
canary tagged-clock     G0 detlint         'buildtag'                     "a clock behind a build tag in a simulator package"        G0 plant_tagclock
canary tagged-only      G0 detlint         'buildtag'                     "the same clock as the ONLY file of a simulator package"  G0 plant_tagonly
canary pointer-format   G0 detlint         'ptrfmt'                       "a pointer formatted with %v"                              G0 plant_ptrfmt
canary sort-ties        G0 detlint         'maprange'                     "map keys sorted with a comparator that leaves ties"       G0 plant_sortties
canary second-require   G0 deps            'must require exactly'         "a second require line in go.mod"                          G0 plant_require
canary rapid-in-product G0 deps            'pgregory.net/rapid'           "rapid imported from a non-test file"                      G0 plant_rapid
canary tagged-import    G0 deps            'klauspost/reedsolomon'        "an outside import hidden behind a build tag"              G0 plant_tagged
canary go-work          G0 deps            'go.work present'              "a go.work file"                                           G0 plant_gowork
canary nested-module    G0 deps            'go.mod outside the root'      "a nested module with its own requirement"                 G0 plant_nestedmod
canary assembly         G0 deps            'assembly, C or object files'  "an assembly file reading the CPU timestamp counter"       G0 plant_asm
canary panic            G0 panics          ' panic: '                     "a panic missing from the allowlist"                       G0 plant_panic
canary panic-shadow     G0 panics          ' panic: '                     "a build-excluded func named panic hiding a real panic"    G0 plant_shadow
canary stale-allow      G0 panics          'stale-allow'                  "an allowlist entry that matches no panic"                 G0 plant_stale
canary comment          G0 comments        ' comment: '                   "a comment in pre-prose Go"                                G0 plant_comment
canary fake-directive   G0 comments        ' comment: '                   "prose written as a //go: directive"                       G0 plant_fakedir
canary generated-header G0 comments        ' comment: '                   "prose written as a generated-code header"                 G0 plant_generated
canary doc-path         G0 selfcontained   'machine-ref'                  "a /root/ path in the README"                              G0 plant_docpath
canary go-path          G0 selfcontained   'machine references in Go'     "a machine path in a Go string"                            G0 plant_gopath
canary broken-link      G0 selfcontained   'broken-link'                  "a front page linking to a missing page"                   G0 plant_badlink
canary chapter-symlink  G0 deps            'symlinks in the product tree' "a chapter that is a symlink to STATE.md"                  G0 plant_symlink
canary unit-fail        G1 unit            'planted'                      "a failing unit test"                                      G1 plant_unitfail
canary coverage         G1 unit            'under the 85% floor'          "a floor package under 85% coverage"                       G1 plant_coverage
canary property         G2 property        'planted'                      "a property that fails only at 2,000 checks"               G2 plant_prop
canary unnamed-property G2 property        'planted'                      "the same property without Prop or Laws in its name"       G2 plant_unnamed
canary fuzz             G3 fuzz            'FUZZ FAIL'                    "a fuzz target that fails on any mutated input"            G3 plant_fuzz
canary unicode-fuzz     G3 fuzz            'FUZZ FAIL'                    "the same target under a non-ASCII name"                   G3 plant_unifuzz
canary race             G4 race            'DATA RACE'                    "a data race"                                              G4 plant_race
canary dst-seeds        G5 dst             'planted'                      "a test that fails only under the 500-seed sweep"          G5 plant_dst
canary indirect-dst     G5 dst             'planted'                      "a scenario that reaches the simulator through a bridge"   G5 plant_indirect

# Results that must never count for landing, each on an otherwise green full run.
counts dirty-tree     "a dirty tree reported by the host wrapper"   false GATE_DIRTY=true
counts dirty-unknown  "no clean-tree flag at all (gate called directly)" false GATE_DIRTY=
counts short-fuzz     "a fuzz budget under 30 s"                    false FUZZ_SECONDS=5
counts bad-sha        "a commit that is not a full SHA"             false GATE_SHA=not-a-commit
if [[ partial-run =~ $only ]]; then
  fresh partial-run; run_gate partial-run G0,G6
  j="$work/partial-run.out/result.json"
  if [ "$(jq -r '.overall' "$j" 2>/dev/null)" = pass ] && [ "$(jq -r '.counts_for_landing' "$j")" = false ]; then record partial-run "a green run of a subset of gates" "does not count" true
  else record partial-run "a green run of a subset of gates" "does not count" false; fi
  rm -rf "$work/partial-run" "$work/partial-run.out"
fi
# A result produced outside the container must say so, fail G6 and not count.
if [[ outside-container =~ $only ]] && [ "$fast" != 1 ]; then
  fresh outside-container
  run_gate outside-container "" OBJSTORE_DEV_CONTAINER=0 GATE_NO_DOCKER=1
  j="$work/outside-container.out/result.json"
  if [ "$(jq -r '.in_container' "$j" 2>/dev/null)" = false ] && [ "$(jq -r '.counts_for_landing' "$j")" = false ] \
     && [ "$(status_of outside-container G6 docker)" = fail ]; then record outside-container "a full gate run outside the dev container" "in_container=false, G6 red, does not count" true
  else record outside-container "a full gate run outside the dev container" "in_container=false, G6 red, does not count" false; fi
  rm -rf "$work/outside-container" "$work/outside-container.out"
fi
# An image ID that is not an image digest fails G6.
if [[ bad-image =~ $only ]]; then
  fresh bad-image; run_gate bad-image G6 GATE_IMAGE_ID=invented-image
  if [ "$(status_of bad-image G6 docker)" = fail ]; then record bad-image "an invented image ID" "G6 red" true
  else record bad-image "an invented image ID" "G6 red" false; fi
  rm -rf "$work/bad-image" "$work/bad-image.out"
fi
# A build-stage run whose comment baseline is the gated commit itself certifies nothing:
# the gate must stop, not pass. A baseline that names no commit must not count.
if [[ self-baseline =~ $only ]]; then
  fresh self-baseline; plant_in self-baseline plant_comment
  grc=-1; [ $plant_rc != 0 ] || { run_gate self-baseline G0 GATE_BASELINE="$work/self-baseline" GATE_BASE_SHA=$fake_sha; grc=$?; }
  if [ $grc = 2 ] && [ ! -e "$work/self-baseline.out/result.json" ]; then record self-baseline "a commit used as its own comment baseline" "gate exits 2, no result" true
  else record self-baseline "a commit used as its own comment baseline" "gate exits 2, no result" false; fi
  rm -rf "$work/self-baseline" "$work/self-baseline.out"
fi
counts unnamed-baseline "a comment baseline that names no commit" false GATE_BASE_SHA=some-directory
# A misspelt stage must stop the gate, not switch the comment rule off.
if [[ stage-typo =~ $only ]]; then
  fresh stage-typo; plant_in stage-typo plant_comment
  grc=-1; [ $plant_rc != 0 ] || { run_gate stage-typo G0 GATE_STAGE=buidl; grc=$?; }
  if [ $grc = 2 ] && [ ! -e "$work/stage-typo.out/result.json" ]; then record stage-typo "GATE_STAGE=buidl with a comment in pre-prose Go" "gate exits 2, no result" true
  else record stage-typo "GATE_STAGE=buidl with a comment in pre-prose Go" "gate exits 2, no result" false; fi
  rm -rf "$work/stage-typo" "$work/stage-typo.out"
fi

mkdir -p agent_work/0a-foundation/gates
if [ "$only" = . ] && [ "$fast" != 1 ]; then
  jq -n --arg at "$(date -u +%FT%TZ)" --arg go "$(go version)" --argjson r "$results" \
    '{ran_at:$at, go_version:$go, canaries:$r, all_caught:([$r[].caught]|all)}' > agent_work/0a-foundation/gates/canaries.json
fi
rm -rf "$work"
echo "canaries: $(jq '[.[]|select(.caught)]|length' <<<"$results") of $(jq length <<<"$results") caught"
exit $rc
