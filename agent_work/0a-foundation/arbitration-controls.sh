#!/bin/bash
# Positive and negative controls for arbitration items A-C (ARBITRATION-1.md), run by the
# orchestrator against the tooling at HEAD. Item D is covered by the detlint unit test
# (testdata/mod/maprange/outervar). Builds a throwaway repository under /tmp whose main
# already carries commented simulator code (a later chunk's situation), then checks what
# the gate, the direct-container entry point and ci-prose.sh do. Prints one line per
# control: OK or WRONG. Exit 0 when every control is OK.
# Usage (from the worktree root): agent_work/0a-foundation/arbitration-controls.sh
set -u
src=$(pwd -P)
t=$(mktemp -d /tmp/arb-controls.XXXXXX)
trap 'docker run --rm -v "$t:/w" objstore-dev:local sh -c "rm -rf /w/* /w/.[!.]*" >/dev/null 2>&1; rm -rf "$t"' EXIT
rc=0
ctl() { # ctl <name> <want-exit> <got-exit> [<log> <pattern>]
  local ok=true
  [ "$2" = "$3" ] || ok=false
  if [ $# -ge 5 ] && ! grep -qE -- "$5" "$4"; then ok=false; fi
  if $ok; then echo "OK    $1 (exit $3)"; else echo "WRONG $1: want exit $2${5:+ and /$5/}, got $3"; [ $# -ge 4 ] && tail -n 12 "$4" | sed 's/^/      | /'; rc=1; fi
}
git archive HEAD -- . ':(exclude)kb' | tar -x -C "$t"
cd "$t" || exit 2
git init -q -b main . && git config user.email c@example.org && git config user.name controls
echo 'agent_work/ctl/' >> .git/info/exclude
mkdir -p internal/sim
printf 'package sim\n\n// Double is prose that landed with an earlier chunk.\nfunc Double(x int) int {\n\treturn 2 * x\n}\n' > internal/sim/sim.go
printf 'package sim\n\nimport "testing"\n\nfunc TestDouble(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal("double")\n\t}\n}\n' > internal/sim/sim_test.go
git add -A && git commit -q -m "base: main with landed prose"
base=$(git rev-parse HEAD)

# A: a build-stage run on main must stop, even with an unauthorized comment committed there.
printf 'package sim\n\n// Triple was never written by the prose seat.\nfunc Triple(x int) int {\n\treturn 3 * x\n}\n' > internal/sim/triple.go
git add -A && git commit -q -m "main: an unauthorized comment"
GATE_ONLY=G0 agent_work/gates/gate.sh ctl > "$t/../a1.$$" 2>&1; ctl "A1 build stage on main is refused" 2 $? "$t/../a1.$$" 'its own comment baseline'
[ ! -e agent_work/ctl/gates/result.json ]; ctl "A1 and writes no result" 0 $?
bad=$(git rev-parse HEAD)
git reset -q --hard "$base"

# A/B positive: a correct branch on commented main is green; negative: its own comment is red.
git checkout -q -b chunk/good
printf 'package sim\n\nfunc Half(x int) int {\n\treturn x / 2\n}\n' > internal/sim/half.go
git add -A && git commit -q -m "chunk: uncommented code"
GATE_ONLY=G0 agent_work/gates/gate.sh ctl > "$t/../a2.$$" 2>&1; ctl "A2 correct branch on commented main: G0 green from the host" 0 $? "$t/../a2.$$" 'overall=pass'
docker compose run --rm -T -e GATE_ONLY=G0 dev agent_work/gates/gate.sh ctl > "$t/../b1.$$" 2>&1; ctl "B1 same branch, gate called directly in the container: G0 green" 0 $? "$t/../b1.$$" 'overall=pass'
printf 'package sim\n\n// Quarter is a comment the branch adds before its prose stage.\nfunc Quarter(x int) int {\n\treturn x / 4\n}\n' > internal/sim/quarter.go
git add -A && git commit -q -m "chunk: a premature comment"
GATE_ONLY=G0 agent_work/gates/gate.sh ctl > "$t/../a3.$$" 2>&1; ctl "A3 branch adds its own comment: G0 red from the host" 1 $? "$t/../a3.$$" 'comments +fail'
docker compose run --rm -T -e GATE_ONLY=G0 dev agent_work/gates/gate.sh ctl > "$t/../b2.$$" 2>&1; ctl "B2 same, called directly in the container: G0 red" 1 $? "$t/../b2.$$" 'comments +fail'
git checkout -q -f main

# C: an unknown push boundary checks everything since the bootstrap commit, or fails closed.
git reset -q --hard "$bad"
echo state > STATE.md && git add -A && git commit -q -m "main: an operational commit on top"
CI_PROSE_BOOTSTRAP=$base agent_work/gates/ci-prose.sh 0000000000000000000000000000000000000000 HEAD > "$t/../c1.$$" 2>&1; ctl "C1 first push hiding a comment under an operational commit is caught" 1 $? "$t/../c1.$$" 'has comments'
agent_work/gates/ci-prose.sh 0000000000000000000000000000000000000000 HEAD > "$t/../c2.$$" 2>&1; ctl "C2 no bootstrap commit in the history: fails closed" 2 $? "$t/../c2.$$" 'no boundary to trust'
CI_PROSE_BOOTSTRAP=$base agent_work/gates/ci-prose.sh "" "$base" > "$t/../c3.$$" 2>&1; ctl "C3 nothing new since the bootstrap commit: clean" 0 $? "$t/../c3.$$" 'nothing new'
git reset -q --hard "$base"
echo state > STATE.md && git add -A && git commit -q -m "main: an operational commit"
CI_PROSE_BOOTSTRAP=$base agent_work/gates/ci-prose.sh 0000000000000000000000000000000000000000 HEAD > "$t/../c4.$$" 2>&1; ctl "C4 first push of an honest operational commit: clean" 0 $? "$t/../c4.$$" 'prose-check: clean'
rm -f "$t"/../*.$$
cd "$src"
# B in a linked worktree: git cannot read the checkout inside the container -> setup error, not a false comment finding.
if [ -f .git ]; then
  docker compose run --rm -T -e GATE_ONLY=G0 -e GATE_OUT=/tmp/ctl-out dev agent_work/gates/gate.sh ctl > /tmp/b3.$$ 2>&1; ctl "B3 linked worktree, called directly in the container: setup error with guidance" 2 $? /tmp/b3.$$ 'from the host checkout'
  rm -f /tmp/b3.$$
fi
exit $rc
