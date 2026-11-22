# Brief — REVIEW 2: the canary script and CI workflows, after the fixes

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch
`tooling/ci-canaries`). READ-ONLY: the ONLY file you create is
`agent_work/ci-canaries/REVIEW-2.md`.

`agent_work/ci-canaries/REVIEW-1.md` failed two tooling changes (brief:
`agent_work/ci-canaries/objstore-p2-ci-canaries-review.md`). The answer is ONE commit:
`git show HEAD~1` (subject "gates, ci: make canary plants fail loudly; ..."), i.e.
`git diff 61ab0198559edb9eae784114901b8336e0531d9a~1..61ab0198559edb9eae784114901b8336e0531d9a -- agent_work/gates .github` — check with `git log --oneline -4`.

Decide for each REVIEW-1 finding (1–4): FIXED, PARTLY FIXED or NOT FIXED — from the code,
and by running where cheap. In particular:
- Finding 1: `plant_in` runs each plant with `set -euo pipefail` in a subshell and is
  never called inside `if`/`&&`/`||` (errexit would be off there) — verify every call
  site. Does a failing plant now produce a MISSED record with the plant's error and a
  non-zero exit of the script? Prove it without editing the worktree: copy the tree to a
  directory under /tmp, break one plant there (for example make `plant_gofmt` write into
  a directory that does not exist, without the `mkdir`), and run
  `agent_work/gates/canaries.sh '^gofmt$'` in that copy through Docker. Does `fresh`
  stop the run on a failed copy? Does any plant now fail under errexit on a CORRECT tree
  (a command whose non-zero status was harmless before)?
- Finding 2: path filter and `pull_request` trigger in `.github/workflows/canaries.yml`;
  valid workflow syntax (no YAML features GitHub Actions rejects).
- Finding 3: `CANARY_FAST=1` — is the variable really passed into the container; does the
  fast mode run exactly the G0/G6 canaries and skip every full gate run; is the claim in
  the script header true; does the fast step in `ci.yml` run after the gate step so a
  red tree is reported by the gate first? The orchestrator's local fast run caught 33 of
  33 in about 11 minutes on a loaded 4-core machine; you may run a few yourself
  (`CANARY_FAST=1 agent_work/gates/canaries.sh '^(tagged-only|panic-shadow|chapter-symlink)$'`),
  not the whole set.
- Finding 4: `plant_tagonly` now deletes the other top-level files of `internal/sim` in
  the copy first. Is the canary then caught for the RIGHT reason (the build-tag rule on a
  directory with no buildable file), and not because the copy no longer compiles?
Then: any new BLOCKER or MAJOR introduced (a gate weakened, CI green on a tree the old
set-up would have failed, a canary that can no longer fail)?

Output `agent_work/ci-canaries/REVIEW-2.md`: first line exactly `VERDICT: PASS`,
`VERDICT: NITS` or `VERDICT: FAIL`; the finding table with one line of evidence each; new
findings numbered with severity, file and line, reproduction, smallest fix; what you ran
versus read. FAIL needs a BLOCKER or MAJOR.
