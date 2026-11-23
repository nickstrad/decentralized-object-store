# Brief — Chunk 7 FINISH: fix the findings of the Opus adversarial pass

Seat: Gemini 4 Neon, **BUILDER** (fresh agent). You are in a git worktree, branch
`chunk/07-e2e`, and it is your current directory. Work only here.

By owner ruling Neon finishes Chunk 7 as its builder (code and tests, product code
included), Fable reviews, and Opus ran the adversarial pass. That pass is in
`agent_work/adversarial/07-e2e-opus/` (`ATTACKS.md`, drivers, `mutations/`, `tools/`,
`evidence/`). Its verdict is FINDINGS: two majors, three minors. The chunk cannot land with
a major open. This brief is the one fix round for them. You are a builder: fix; do not
re-review. F1 is a PRODUCT defect in `internal/node` and is the main work; do it first and
do it carefully.

## Read first

1. `agent_work/adversarial/07-e2e-opus/ATTACKS.md` — findings F1–F5 and notes N1–N6.
2. `agent_work/07-e2e/objstore-p2-builder-rules.md` (read "Opus" as "you"; NO comments in
   `.go` files — prose comes later).
3. For F1: `internal/node/migrate.go` (Copy, Verify, the catch-up, Retire),
   `internal/node/inventory.go` (`expectedAll`, the orphan path), `docs/knowledge/chunk-06-integration.md`
   (the loss-declaration rule and the migration safety rule already in force), and
   `agent_work/adversarial/07-e2e-opus/tools/opus_sched_test.go.txt` (the reproduction).
4. `agent_work/07-e2e/build-notes.md` sections 3, 8 and 11–14.

## F1 (MAJOR, product): a migration whose coordinator stalls longer than OrphanGrace mid-copy completes and loses acknowledged objects

Reproduced by the orchestrator as well (seeds 31337, 3, 4 fail):
copy `agent_work/adversarial/07-e2e-opus/tools/opus_sched_test.go.txt` to
`test/e2e/opus_sched_test.go` in a scratch copy, then
`OPUS_NOPART=1 OPUS_ONLY=lost-reply-then-pause-past-heal-bound OPUS_SEEDS=31337,3,4 go test ./test/e2e -count=1 -run '^TestOpusLostReplyCoordinatorSchedules$'`.

What happens: the copy lands shards on the destination, the coordinator freezes before the
staged record that makes those shards "expected" has replicated, the destination collects
them as orphans after OrphanGrace, the coordinator resumes, stages, passes verify (which
compares manifest roots and never looks at shards), flips, and Retire swaps the live
manifests onto a version whose shards are gone. An acknowledged object written with no
fault is lost.

**The rule the fix must establish:** a migration never makes live (never flips to, never
swaps a manifest onto) a version for which the destination does not verifiably hold its
shards — every stripe, enough shards that the object is as durable as a fresh write
(state the exact threshold you choose and why; "at least k" is the floor, all k+m is the
natural target, with repair of what is missing) — however long any node, the coordinator
included, was stalled, crashed or cut off at any point of the saga. If the shards are not
there, the saga re-copies them or compensates; it never completes onto them.

Design is yours, with these constraints:

- **No check-then-act gap.** A presence check that passes just before the destination's
  orphan timer fires proves nothing. Whatever confirms the shards must also be what stops
  the destination from collecting them afterwards (for example: a holder confirms a shard
  only once its own inventory counts that shard as expected because it has the staged
  record; or the confirmation itself pins it). Say in the notes how your design closes the
  gap, and test the interleaving (confirm, then the grace expiring, then the flip).
- The copy intent written before the first shard is put may be used to protect shards on
  the destination, but by itself it is not enough: a coordinator frozen before the intent
  replicates leaves the destination with shards and no intent. The verify-side rule above
  is the safety net and is required; intent-side protection is optional.
- Keep everything already in force: the migration safety rule before retiring the source
  (every live peer agrees on route, flip owner and the bucket's manifests; a purge deletes
  only versions with a recorded death marker), the loss-declaration rule, exactly-once
  request callbacks, the determinism lint (no clocks, goroutines or map-order dependence
  in `internal/node`), wire-code blocks (node and object API 160–191; say which code you
  add, if any), and the panic allowlist.
- Do not paper over it by lengthening OrphanGrace or shortening anything in the tests.

Tests (failing first, in this order):

1. A deterministic regression test in `internal/node` (or `test/e2e` if it needs the
   cluster harness) that drives the schedule — objects acknowledged, migration started,
   coordinator paused past OrphanGrace right after the copy's shard writes, resumed — over
   at least the seeds 31337, 3, 4 and 9 more, and requires: the saga ends `completed` or
   `compensated`; every acknowledged object is live and readable byte-for-byte afterwards;
   no loss record; no shard of the bucket outside its final region. Show it FAILS on the
   unfixed tree (paste the message).
2. The same with the pause placed after staging and before verify, after verify and
   before the flip, and after the flip and before retire; and with a destination holder
   (not the coordinator) paused past OrphanGrace instead.
3. A kept planted-fault canary proving the new rule bites: with the new check disabled
   (through an existing test hook such as `WrapMigration` — do not add a product switch
   that turns safety off), the regression schedule must be caught by the suite.
4. Add the long-stall fault to what the suite exercises from now on: either a nemesis op
   or a dedicated scenario test in `test/e2e` that pauses a migration's coordinator for
   longer than OrphanGrace (ATTACKS.md "Why the suite cannot see it"). It must run at
   default seeds in a few seconds.

## F2 (MAJOR, suite): the printed REPLAY command does not replay a seed outside shard 0

`sweep` puts seed i in subtest `shard=<i mod 4>`; with `-seed` the single seed lands in
`shard=0`, so the printed `-run` pattern for shards 1–3 matches nothing and the replay
reports `ok`. Fix it so that every printed REPLAY line re-runs exactly the failing seed
and fails the same way, for every shard and for E5's cells as well (check what E5 prints).
`internal/sim/simtest` is frozen: fix it on the `test/e2e` side (how seeds map to
subtests, or what the suite prints). Test: plant a failure on every seed of one scenario
under `DST_SEEDS=8` (a test-only hook, as the canaries do), capture each printed REPLAY
line, run it, and require each to fail with the planted message; keep that as a test (it
may re-execute the test binary as the existing canary helpers do). Also assert that a
`-seed` run which matches no seed subtest FAILS instead of passing empty.

## F3 (minor, oracle): migration manifest swaps bypass the commit sink

Make the harness oracle see versions that Retire swaps in (report them to the commit sink,
or teach the oracle from replicated state) so that the "version committed after its shards
were deleted" check runs for a swap and a loss of a migrated object is judged, not reported
as "a version no node ever committed". Show the oracle now flags F1's event directly on
the unfixed product (in a scratch copy) — it is the check built for it.

## F4 (minor, suite): a purge that does nothing passes tier S

On tier S check the source region right after the saga ends and before GCGrace can have
collected anything (or with collection held off), so `mutations/p11g-retire-purge-noop.patch`
is killed on tier S. Apply the patch in a scratch copy and show the kill.

## F5 (minor, suite): the loss-declaration oracle is never exercised by a scenario

Add a scenario (or extend E3/E6) that produces absence records short of a loss — one holder
of a stripe loses its shard and records the absence while k others still verify — and
require: no loss declared, object readable, shard rebuilt. Then
`mutations/p2f-loss-declared-on-one-absence.patch` must be killed by it; show the kill.

## Notes N1–N6

No action required. If N4 (the 60 s "no shard outside its region" bound depends on
OrphanGrace) changes with your F1 fix, say how.

## Verify (through Docker only, one heavy run at a time; record command, result, time)

`gofmt -l .`; `go vet ./...`; `go vet -tags e2efault ./...`; `go build ./...`;
`go test -count=1 ./internal/node/... ./internal/saga/... ./internal/storage/... ./test/e2e`;
`go test -short -count=1 ./test/e2e` (state the wall time: the CI fast lane runs this);
`DST_SEEDS=500 go test -count=1 ./internal/node -timeout 90m` (the full-node sweep — the
product changed);
`DST_SEEDS=500 go test -count=1 ./test/e2e -run 'TestE4|TestE5|TestE7|TestCanary|Nemesis|Fuzz' -timeout 90m`;
`go test ./test/e2e -run '^$' -fuzz '^FuzzClusterNemesis$' -fuzztime 120s -fuzzminimizetime 5s`;
`go test -race -count=1 ./internal/node/... ./test/e2e`;
`E2E_TIER_R=1 go test ./test/e2e -run 'TestE4.*R$|TestE5.*R$' -count=1 -timeout 30m`;
the Opus drivers that target what you changed, from the worktree root:
`agent_work/adversarial/07-e2e-opus/a02-ambiguous-start.sh` (its F1 lines must now read
HELD; the drivers copy the tree themselves — do not edit anything under
`agent_work/adversarial/`); last `GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e`, then
`git checkout -- agent_work/07-e2e/gates` if tracked files there changed. The full gate is
the orchestrator's. If the coverage of `internal/node` printed by
`go test -cover ./internal/node` falls under 85%, add tests until it does not.

## Notes to write

Append to `agent_work/07-e2e/build-notes.md` a section **"15. Adversarial fix round
(Neon): ATTACKS findings F1–F5"**: per finding what changed, the design of the F1 fix (the
rule, the threshold, how the check-then-act gap is closed, what happens to the saga when
shards are missing, any new wire code or record key), the failing-first evidence, the
canary, the numbers, anything not done and why. State the F1 guarantee in one exact
sentence the chapter can use, and what remains a stated limit (OQ-13: a coordinator that
never returns). If you disagree with a finding, say so with evidence and do the part you
agree with; the orchestrator decides.

## Rules

- Go standard library only; no new dependency; no sleeps or wall clock on tier S or in
  `internal/node`; no dropped errors; the frozen simulator packages (`internal/env`,
  `internal/sim` and below) are not edited. A failing seed is a finding: pin it with its
  replay command; never loosen a check to make it pass.
- Do not touch: PLAN.md, STATE.md, README.md, docs/, book/, kb/, .github/, Dockerfile,
  compose.yaml, agent_work/gates/, agent_work/orch/, agent_work/adversarial/,
  agent_work/INDEX.md, REVIEW-1.md, FABLE-REVIEW.md.
- Git: commit as you go, one logical change per commit, subject `node: <imperative>`,
  `e2e: ...` or `agent_work: ...`, body saying what the tests prove, ending with the
  trailer line `Built-By: Gemini 4 Neon`. Never push, merge, rebase, reset, stash or switch
  branches. Finish with `git status` clean.
- Machine: 4 cores, 7 GB, shared. Do not claim anything you did not run.

## When you are done

Final message: the commit list, one line per finding F1–F5 (closed / partly / disputed),
any failure found. Then the marker line your launcher asks for.
