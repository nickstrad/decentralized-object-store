#!/bin/bash
# Land one chunk on main: refuse unless the landing evidence is in place, then merge --no-ff
# with the tour message and push.
#
# Usage (from the MAIN checkout): agent_work/orch/land.sh <chunk> <branch> <message-file>
# Requires, in the branch tip's tree:
#   agent_work/<chunk>/gates/result.json   counts_for_landing=true, overall=pass,
#                                          in_container=true, partial=false,
#                                          produced on a clean tree at a commit whose only
#                                          later commits touch agent_work/<chunk>/ or STATE/INDEX
#   agent_work/<chunk>/REVIEW-*.md         the newest starts with "VERDICT: PASS" (or NITS with
#                                          a "NITS CLOSED" line added by the orchestrator); if
#                                          the chunk went to arbitration, the newest
#                                          ARBITRATION-*.md is the verdict instead (it is final)
#   agent_work/adversarial/<chunk>/RESULTS.md
# After 0b has landed (tag freeze-0b exists): a chunk branch other than a simulator change
# request must not touch internal/env or the exported simulator packages.
set -eu
set -o pipefail
chunk=$1; branch=$2; msg=$(readlink -f "$3")
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || { echo "land: not on main" >&2; exit 2; }
[ -z "$(git status --porcelain)" ] || { echo "land: main checkout is dirty" >&2; exit 2; }
tip=$(git rev-parse "$branch")
git merge-base --is-ancestor main "$tip" || { echo "land: $branch is not rebased on main" >&2; exit 1; }
show() { git show "$tip:$1" 2>/dev/null; }
res=$(show "agent_work/$chunk/gates/result.json") || { echo "land: no gate result on the branch" >&2; exit 1; }
[ "$(jq -r '.overall + " " + (.in_container|tostring) + " " + (.partial|tostring) + " " + (.dirty_tree|tostring) + " " + (.counts_for_landing|tostring)' <<<"$res")" = "pass true false false true" ] \
  || { echo "land: gate result is not a clean full in-container pass that counts for landing" >&2; exit 1; }
gsha=$(jq -r .sha <<<"$res")
[[ "$gsha" =~ ^[0-9a-f]{40}$ ]] || { echo "land: gate result names no commit ('$gsha')" >&2; exit 1; }
git merge-base --is-ancestor "$gsha" "$tip" || { echo "land: gated commit $gsha is not on $branch" >&2; exit 1; }
changed=$(git diff --name-only "$gsha" "$tip") || { echo "land: cannot diff $gsha..$tip" >&2; exit 1; }
later=$(grep -vE "^agent_work/($chunk|adversarial/$chunk)/|^agent_work/INDEX.md$|^STATE.md$|^docs/knowledge/" <<<"$changed" || true)
[ -z "$later" ] || { echo "land: files changed after the gated commit $gsha:"; echo "$later"; exit 1; } >&2
notes=$(git ls-tree --name-only "$tip" "agent_work/$chunk/")
rev=$({ grep -E 'ARBITRATION-[0-9]+\.md$' <<<"$notes" || true; } | sort -V | tail -1)
[ -n "$rev" ] || rev=$({ grep -E 'REVIEW-[0-9]+\.md$' <<<"$notes" || true; } | sort -V | tail -1)
[ -n "$rev" ] || { echo "land: no review file" >&2; exit 1; }
verdict=$(show "$rev" | head -1)
case "$verdict" in
  "VERDICT: PASS") ;;
  "VERDICT: NITS") show "$rev" | grep -q '^NITS CLOSED' || { echo "land: $rev is NITS without a NITS CLOSED line" >&2; exit 1; } ;;
  *) echo "land: $rev says '$verdict'" >&2; exit 1 ;;
esac
show "agent_work/adversarial/$chunk/RESULTS.md" >/dev/null || { echo "land: no adversarial RESULTS.md" >&2; exit 1; }
if git rev-parse -q --verify refs/tags/freeze-0b >/dev/null && [[ "$chunk" != *sim-change* ]]; then
  frozen=$(git diff --name-only main "$tip" -- internal/env internal/sim) || exit 1
  frozen=$(grep -v '_test.go$' <<<"$frozen" || true)
  [ -z "$frozen" ] || { echo "land: $branch edits frozen simulator packages:"; echo "$frozen"; exit 1; } >&2
fi
git merge --no-ff -F "$msg" "$branch"
git push origin main
echo "land: $chunk merged at $(git rev-parse --short HEAD) (review: $rev $verdict)"
