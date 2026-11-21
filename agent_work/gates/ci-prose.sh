#!/bin/bash
# Prose provenance for everything a push put on main (CI), or for any first-parent range.
#
# Usage: agent_work/gates/ci-prose.sh <before> [<head>]      (default head: HEAD)
#
# Walks the first-parent commits of before..head:
#   a merge commit    must be a chunk landing: its side branch is rebased on the first
#                     parent, the merge itself carries no change the branch does not have
#                     (its tree equals the branch tip's), and prose-check.sh passes over the
#                     branch;
#   other commits     each unbroken run of direct commits is one prose-check.sh range, so a
#                     direct push cannot add a comment or touch reader-facing prose without
#                     the trailer, wherever in the push it sits.
# When <before> cannot be trusted as the boundary (empty, all zeros, unknown, or not an
# ancestor of head: a first push, a forced push, a manual run) the WHOLE history since the
# bootstrap commit below is checked. The bootstrap commit is the last one made before any
# tooling or product code existed; everything after it is subject to the prose rule. If the
# bootstrap commit is not an ancestor of head either, there is no boundary to trust and the
# check fails closed.
# Exit 0 clean, 1 findings, 2 setup error (including: no trustworthy boundary).
set -u
set -o pipefail
bootstrap=${CI_PROSE_BOOTSTRAP:-a78cffc3369e50b1b040a6a8ee4d13c4127c755d}
before=${1:-}; head=${2:-HEAD}
here=$(cd "$(dirname "$0")" && pwd -P)
cd "$here/../.." || exit 2
head=$(git rev-parse --verify -q "$head^{commit}") || { echo "ci-prose: bad head" >&2; exit 2; }
if [[ -z "$before" || "$before" =~ ^0+$ ]] || ! git rev-parse --verify -q "$before^{commit}" >/dev/null \
   || ! git merge-base --is-ancestor "$before" "$head"; then
  echo "ci-prose: '$before' is not a usable boundary; checking the whole history since the bootstrap commit"
  before=$(git rev-parse --verify -q "$bootstrap^{commit}") \
    || { echo "ci-prose: bootstrap commit $bootstrap is not in this repository; no boundary to trust" >&2; exit 2; }
  git merge-base --is-ancestor "$before" "$head" \
    || { echo "ci-prose: bootstrap commit $bootstrap is not an ancestor of $head; no boundary to trust" >&2; exit 2; }
fi
before=$(git rev-parse "$before^{commit}")
commits=$(git rev-list --reverse --first-parent "$before..$head") || exit 2
[ -n "$commits" ] || { echo "ci-prose: nothing new since $(git rev-parse --short "$before")"; exit 0; }
rc=0
run_start=$before   # the commit just before the current unbroken run of direct commits
run_end=
check() { # check <base> <head> <label>
  echo "ci-prose: $3 $(git rev-parse --short "$1")..$(git rev-parse --short "$2")"
  "$here/prose-check.sh" "$1" "$2"; local r=$?
  [ $r = 0 ] || { [ $r = 1 ] && rc=1 || exit 2; }
}
flush() { [ -z "$run_end" ] || check "$run_start" "$run_end" "direct commits"; run_end=; }
for c in $commits; do
  if git rev-parse -q --verify "$c^2" >/dev/null; then
    flush
    short=$(git rev-parse --short "$c")
    if git rev-parse -q --verify "$c^3" >/dev/null; then echo "ci-prose: $short is an octopus merge"; rc=1
    elif ! git merge-base --is-ancestor "$c^1" "$c^2"; then echo "ci-prose: merge $short: its branch is not rebased on main"; rc=1
    else
      git diff --quiet "$c^2" "$c" || { echo "ci-prose: merge $short carries changes its branch does not have"; rc=1; }
      check "$c^1" "$c^2" "landing $short, branch"
    fi
    run_start=$c
  else
    run_end=$c
  fi
done
flush
exit $rc
