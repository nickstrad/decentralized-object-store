# Shared helpers for the 07-e2e-opus drivers. Source from a driver; never run directly.
# Every Go command runs in the repo's Docker `dev` service. Mutations are applied to a
# scratch copy of the tracked tree under /tmp; the worktree itself is never modified.
set -u
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
HERE=$ROOT/agent_work/adversarial/07-e2e-opus
MUT=$HERE/mutations
EVID=$HERE/evidence
mkdir -p "$EVID"
BROKEN=0
SETUP=0
SCRATCHES=()

cleanup_scratches() {
  local d
  for d in "${SCRATCHES[@]:-}"; do
    [ -n "$d" ] && [ -d "$d" ] && rm -rf "$d"
  done
}
trap cleanup_scratches EXIT

held()   { echo "HELD $1 $2: $3"; }
broken() { echo "BROKEN $1 $2: $3"; BROKEN=1; }
setup()  { echo "SETUP $1 $2: $3"; SETUP=1; }

finish() {
  if [ "$SETUP" = 1 ]; then exit 2; fi
  if [ "$BROKEN" = 1 ]; then exit 1; fi
  exit 0
}

# mkscratch: copy the tracked tree (minus agent_work and book) to a fresh /tmp dir; prints it.
mkscratch() {
  local d
  d=$(mktemp -d /tmp/a07opus.XXXXXX) || return 1
  SCRATCHES+=("$d")
  (cd "$ROOT" && git ls-files -z | grep -zv '^agent_work/\|^book/' | tar --null -T - -cf - ) | tar -xf - -C "$d" || return 1
  echo "$d"
}

# apply_patch DIR PATCH...: apply patch files (relative to mutations/) to a scratch dir.
apply_patch() {
  local d=$1; shift
  local p
  for p in "$@"; do
    patch -s -p1 -d "$d" < "$MUT/$p" >/dev/null || { echo "patch $p failed" >&2; return 1; }
  done
}

# indocker DIR LOG CMD: run CMD (bash -c) in the dev service with DIR mounted at /src.
# Extra environment: pass as ENVS="-e A=1 -e B=2". Returns the command's exit status.
indocker() {
  local d=$1 log=$2; shift 2
  # shellcheck disable=SC2086
  (cd "$d" && timeout "${DOCKER_TIMEOUT:-1500}" docker compose run --rm -T ${ENVS:-} dev bash -c "$*") >"$log" 2>&1 </dev/null
}

# classify LOG RC WANT: echo KILLED|SURVIVED|SETUP. WANT is an extended regex for the
# expected failure reason; KILLED needs a FAIL and the reason in the log.
classify() {
  local log=$1 rc=$2 want=$3
  if grep -qE 'same seed gave different digests|\[build failed\]|\[setup failed\]|cannot find package|undefined:|syntax error|patch .* failed|no such service|Cannot connect to the Docker' "$log"; then
    echo SETUP; return
  fi
  if [ "$rc" = 124 ]; then echo TIMEOUT; return; fi
  if [ "$rc" = 0 ]; then
    if grep -q 'no tests to run' "$log"; then echo SETUP; return; fi
    echo SURVIVED; return
  fi
  if grep -qE -- "$want" "$log"; then echo KILLED; else echo OTHERFAIL; fi
}

# firstfail LOG: the failed top-level tests and the first test failure message, on one line.
firstfail() {
  local tests msg
  tests=$(grep -E '^--- FAIL: ' "$1" | sed 's/^--- FAIL: \([^ ]*\).*/\1/' | sort -u | tr '\n' ',' | sed 's/,$//')
  msg=$(grep -m1 -E '_test\.go:[0-9]+: .*(fail|FAIL|check|want|must|disallowed|not within|left|shard|lost|wrong)' "$1" | sed 's/^[[:space:]]*//' | tr -s ' ' | cut -c1-230)
  [ -z "$msg" ] && msg=$(grep -m1 -E 'panic:|_test\.go:[0-9]+: ' "$1" | sed 's/^[[:space:]]*//' | cut -c1-230)
  echo "[$tests] $msg"
}

# mutation_check CLAIM NAME PATCHES TESTRUN WANT [extra go test args]
# Applies PATCHES (space separated) to a fresh scratch tree, runs `go test ./test/e2e -run TESTRUN`
# and reports HELD when the run fails with WANT, BROKEN when it passes (the mutation survived).
mutation_check() {
  local claim=$1 name=$2 patches=$3 run=$4 want=$5; shift 5
  local extra="$*"
  local d log rc verdict
  d=$(mkscratch) || { setup "$claim" "$name" "could not create scratch tree"; return; }
  # shellcheck disable=SC2086
  if ! apply_patch "$d" $patches; then setup "$claim" "$name" "patch did not apply"; return; fi
  log=$EVID/$name.log
  indocker "$d" "$log" "go test ${GOTAGS:-} ${GOPKG:-./test/e2e} -count=1 -run '$run' $extra"; rc=$?
  verdict=$(classify "$log" "$rc" "$want")
  case $verdict in
    KILLED) held "$claim" "$name" "mutation [$patches] killed by -run '$run': $(firstfail "$log")";;
    SURVIVED) broken "$claim" "$name" "mutation [$patches] SURVIVED go test ./test/e2e -run '$run' $extra (patch mutations/$patches)";;
    OTHERFAIL) held "$claim" "$name" "mutation [$patches] failed -run '$run' but not with /$want/ (see evidence/$name.log): $(firstfail "$log")";;
    TIMEOUT) held "$claim" "$name" "mutation [$patches] made -run '$run' exceed the driver timeout (hang counts as caught; evidence/$name.log)";;
    *) setup "$claim" "$name" "setup error, see evidence/$name.log: $(firstfail "$log")";;
  esac
  rm -rf "$d"
}
