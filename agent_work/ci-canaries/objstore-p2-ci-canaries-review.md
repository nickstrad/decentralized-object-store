# Brief — REVIEW: two small changes to the gate tooling and CI

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch
`tooling/ci-canaries`). READ-ONLY: do not edit code, do not commit, no git write commands.
The ONLY file you create is `agent_work/ci-canaries/REVIEW-1.md`. Write it before you stop.

## What you are reviewing
Two tooling changes by the orchestrator seat (no product code):

A. Already on main, commit with subject "gates: plant build canaries in a directory the
   script creates; ..." (`git log --oneline -8 -- agent_work/gates/canaries.sh`, then
   `git show <sha> -- agent_work/gates/canaries.sh`). Background: the canary script
   plants one violation at a time in a throwaway copy of the tree and requires the gate
   to go red on the owning check. Twelve plant functions wrote into
   `internal/buildcheck/`, a placeholder package that an earlier landing removed, so the
   plants silently failed to write, 12 of 50 canaries were MISSED and CI stayed red. The
   fix makes each plant create the directory first.
B. The uncommitted working-tree change here (`git status`, `git diff`, plus the new file
   `.github/workflows/canaries.yml`): the "Gate canaries" step is removed from
   `.github/workflows/ci.yml` and moved to its own workflow that runs when the tester
   changes (path filter), weekly, and on manual dispatch, with its own concurrency group
   that never cancels a run. Reason: several canaries are full gate runs on the whole
   tree; with five protocol packages landed the step ran over 105 minutes inside a
   120-minute job (the gates themselves take about 14), and every later push cancelled
   the run, so CI never reported.

Read `agent_work/gates/canaries.sh` (all of it), `agent_work/gates/gate.sh` enough to
follow a canary, both workflow files, and `docs/knowledge/chunk-0a-foundation.md`.

## Questions (number your findings)
1. Change A: is it correct and complete — does every plant that writes into a directory
   now create it; can a plant still fail silently (a write error that leaves the copy
   clean, so the canary is scored on something else); should the script itself fail
   loudly when a plant function returns non-zero? Is any canary now caught for the wrong
   reason (for example because the new package directory itself trips a check)?
   You may run a few canaries through Docker to check:
   `agent_work/gates/canaries.sh '^(gofmt|vet|build|panic|comment)$'` (each takes well
   under a minute; do NOT run the full set — it takes hours).
2. Change B: is the workflow file valid GitHub Actions syntax and does the path filter
   cover everything that can change what the gates or canaries do (think: the gate
   scripts, the lint tools under `agent_work/gates/`, the Dockerfile and compose file,
   go.mod/go.sum, the workflow files themselves; anything else — for example
   `.github/scripts/`, `agent_work/orch/land.sh`, `agent_work/gates/panic-allowlist.txt`
   which builders append to)? What is lost by not running canaries on every push — name
   any canary whose outcome depends on the PRODUCT tree (not the tester) and could
   therefore rot unseen between tooling changes; is weekly plus on-demand enough for
   those, or should a cheap subset stay in `ci.yml`? Is `timeout-minutes: 350` inside
   GitHub's limit for hosted runners? Does removing the step from `ci.yml` leave the
   artifact upload or any other step referring to something that no longer exists?
3. Anything in either change that weakens a gate, or makes CI green on a tree the old
   set-up would have failed.

## Output: agent_work/ci-canaries/REVIEW-1.md
First line exactly `VERDICT: PASS`, `VERDICT: NITS` or `VERDICT: FAIL`. Then numbered
findings with severity (BLOCKER / MAJOR / NIT / NOTE), file and line, a concrete
reproduction or reasoning, and the smallest fix. Then what you verified by running versus
by reading. FAIL needs a BLOCKER or MAJOR. No style remarks.
