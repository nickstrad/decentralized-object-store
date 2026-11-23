# Brief — Chunk 7 FINISH, fix round: close the findings of FABLE-REVIEW.md

Seat: Gemini 4 Neon, **BUILDER** (fresh agent). You are in a git worktree, branch
`chunk/07-e2e`, and it is your current directory. Work only here.

By owner ruling Neon is finishing Chunk 7 as its builder (code and tests, not only prose),
Fable reviews, and Opus then attacks the repaired tests with mutations. Two earlier Neon
agents did the finish (build notes, sections 11 and 12). The review of their work is
`agent_work/07-e2e/FABLE-REVIEW.md`: verdict FAIL with one major, two minors and three
nits. This is the ONE fix round. You are a builder: fix the findings; do not re-review.

## Read first

1. `agent_work/07-e2e/FABLE-REVIEW.md` — all of it. Findings 1–6 and note B are your work.
2. `agent_work/07-e2e/objstore-p2-builder-rules.md` — the builder rules (read "Opus" as
   "you"; the no-comments rule stays: write no comments in `.go` files).
3. `agent_work/07-e2e/build-notes.md`, sections 11 and 12 (what was done and measured).
4. The code named in each finding.

## Work order (commit after each)

1. **Finding 1 (major).** Make the E7 migrated-shards check exact per migration; seed 102
   must pass alone (`go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$' -seed=102
   -count=1`), and a kept canary must show the check still bites on E7 (not only on the
   fuzz configuration). If your change alters what E7 seeds do, re-check both pinned seeds
   in `test/e2e/testdata/seeds.txt`: the lone-wipe canary seed must still be caught by its
   canary, and seed 1 must still fail when the fix of commit `f70e2c2` is undone (try it
   in a scratch copy, e.g. `git stash` is NOT allowed — copy the tree to `/tmp` or revert
   the two files by hand and restore them; leave the tree clean). If seed 1 no longer
   reaches that path, find and pin a seed that does, or state plainly that the unit tests
   alone hold the fix.
2. **Finding 2 (minor).** Test the active-saga wipe guard so that removing it fails a
   test, or remove it with the reasoning in the notes. Replace or repair the subtest that
   passes on the old code.
3. **Finding 3 (minor).** Narrow the voiding of availability reads if it can be done
   cheaply and exactly; measure `availability_gets` over the 500-seed E7 sweep at
   `d32405c~1`, at `d32405c` and after your change (the first two from a scratch copy);
   add the sweep-level floor, safe for a single-seed run.
4. **Findings 4, 5, 6 (nits)** as written in the review. For finding 4 run
   `DST_SEEDS=500 go test -count=1 ./test/e2e -run 'TestE2'` and tier R E2 three times
   (`E2E_TIER_R=1 go test ./test/e2e -run 'TestE2.*R$' -count=3 -timeout 30m`) before you
   keep the stricter rule.
5. **Note B.** One paragraph in the limits of the build notes (section 8).
6. **Verify, through Docker only, one run at a time**, and record command and result:
   `gofmt -l .`; `go vet ./...`; `go vet -tags e2efault ./...`; `go build ./...`;
   `go test -count=1 ./test/e2e ./internal/storage/... ./internal/node/...`;
   `DST_SEEDS=500 go test -count=1 ./test/e2e -run 'TestE2|TestE7|TestCanary|Nemesis|Fuzz' -timeout 60m`;
   `go test ./test/e2e -run '^$' -fuzz '^FuzzClusterNemesis$' -fuzztime 120s -fuzzminimizetime 5s`;
   `go test -race -count=1 ./test/e2e -run 'TestE2|TestE7|TestCanary|Nemesis'`;
   last `GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e`, then `git checkout -- agent_work/07-e2e/gates`
   if that run changed tracked files there. The full gate is the orchestrator's.
7. Append to `agent_work/07-e2e/build-notes.md` a section **"13. Finish round, fix round
   (Neon): FABLE-REVIEW findings"**: per finding what you changed, the test, the canary or
   mutation that proves it bites (with the matched text), the numbers, anything not done
   and why. If you disagree with a finding, say so there with your evidence and do the
   part you agree with; the orchestrator decides.

## Rules

- Go standard library only; no new dependency; failing test or canary first where
  practical; no sleeps or wall clock on tier S; no dropped errors; the frozen simulator
  packages are not edited. A failing seed is a finding: pin it, keep the replay command,
  never loosen a check to make it pass.
- Product code should not need to change, except the tests of finding 6 and — only if
  those tests show a wrong answer — the order of rows in the two tables.
- Do not touch: PLAN.md, STATE.md, README.md, docs/, book/, kb/, .github/, Dockerfile,
  compose.yaml, agent_work/gates/, agent_work/orch/, agent_work/adversarial/,
  agent_work/INDEX.md, REVIEW-1.md, FABLE-REVIEW.md.
- Git: commit as you go, one logical change per commit, subject `e2e: <imperative>` (or
  `storage: ...`, `agent_work: ...`), body saying what the tests prove, ending with the
  trailer line `Built-By: Gemini 4 Neon`. Never push, merge, rebase, reset, stash or
  switch branches. Finish with `git status` clean.
- Machine: 4 cores, 7 GB, shared. Do not claim anything you did not run.

## When you are done

Final message: the commit list, one line per finding 1–6 (closed / partly / disputed),
any failure found. Then the marker line your launcher asks for.
