#!/bin/bash
# Prose provenance for everything a push put on main (CI), or for any first-parent range.
#
# Usage: agent_work/gates/ci-prose.sh <before> [<head>]      (default head: HEAD)
#
# Walks the first-parent commits of before..head:
#   a merge commit   must be a chunk landing: its side branch is rebased on the first parent,
#                    the merge itself carries no change the branch does not have (its tree
#                    equals the branch tip's), and prose-check.sh passes over the branch;
#   any other commit is checked by prose-check.sh as a range of its own, so a direct push
#                    cannot add a comment or touch reader-facing prose without the trailer.
# <before> that is empty, all zeros or unknown (first push, forced push, manual run) falls
# back to the parent of head. Exit 0 clean, 1 findings, 2 setup error.
set -u
set -o pipefail
before=${1:-}; head=${2:-HEAD}
here=$(cd "$(dirname "$0")" && pwd -P)
cd "$here/../.." || exit 2
head=$(git rev-parse --verify -q "$head^{commit}") || { echo "ci-prose: bad head" >&2; exit 2; }
if [[ -z "$before" || "$before" =~ ^0+$ ]] || ! git rev-parse --verify -q "$before^{commit}" >/dev/null \
   || ! git merge-base --is-ancestor "$before" "$head"; then
  before=$(git rev-parse --verify -q "$head^") || { echo "ci-prose: $head has no parent, nothing to check"; exit 0; }
fi
commits=$(git rev-list --reverse --first-parent "$before..$head") || exit 2
[ -n "$commits" ] || { echo "ci-prose: empty range"; exit 0; }
rc=0
for c in $commits; do
  short=$(git rev-parse --short "$c")
  if git rev-parse -q --verify "$c^2" >/dev/null; then
    if git rev-parse -q --verify "$c^3" >/dev/null; then echo "ci-prose: $short is an octopus merge"; rc=1; continue; fi
    git merge-base --is-ancestor "$c^1" "$c^2" || { echo "ci-prose: merge $short: its branch is not rebased on main"; rc=1; continue; }
    git diff --quiet "$c^2" "$c" || { echo "ci-prose: merge $short carries changes its branch does not have"; rc=1; }
    echo "ci-prose: landing $short, branch $(git rev-parse --short "$c^1")..$(git rev-parse --short "$c^2")"
    "$here/prose-check.sh" "$c^1" "$c^2"; r=$?
  else
    echo "ci-prose: direct commit $short"
    "$here/prose-check.sh" "$c^" "$c"; r=$?
  fi
  [ $r = 0 ] || { [ $r = 1 ] && rc=1 || exit 2; }
done
exit $rc
