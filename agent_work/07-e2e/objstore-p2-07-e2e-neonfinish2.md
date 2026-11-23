# Brief — Chunk 7 FINISH, stage 2 of 2: close the nits, the note, and two recorded harness findings

Seat: Gemini 4 Neon, **BUILDER** (fresh agent). You are in a git worktree, branch
`chunk/07-e2e`, and it is your current directory. Work only here.

## Why you, and what is different this time

The project owner ruled (2026-11-22) that Neon finishes Chunk 7 as its builder. For this
finish the usual "Neon writes prose only" boundary is LIFTED: you write and fix Go code and
tests in this worktree. Another seat (Fable) reviews your diff afterwards, and a third
(Opus) then tries to break the repaired tests with mutations. So the standard is: each
repaired check must demonstrably FAIL when the thing it guards is broken.

A previous Neon agent did stage 1 (REVIEW-1 findings 1–3). Its work is committed; read
section 11 of the build notes so you build on it and do not undo it. You are a builder,
not a reviewer: do not re-review stage 1. If you trip over something in it that is plainly
wrong, write one paragraph in your notes and move on.

## Read first

1. `agent_work/07-e2e/REVIEW-1.md` — findings 4, 5, 6, 7 and the note 8 are this stage.
2. `agent_work/07-e2e/objstore-p2-builder-rules.md` — the builder rules; they bind you as
   written for Opus, with the differences under "Rules" below.
3. `agent_work/07-e2e/build-notes.md` — sections 3–6 and 11.
4. `agent_work/adversarial/07-e2e/ATTACKS.md`, the two subsections "C7: unexpected errors
   are silently accepted for GET, tag and delete" and "C10: real cluster construction
   accepts a failed node launch". This is the record of an independent harness run that was
   stopped early; those two findings are yours to close. Do not edit anything under
   `agent_work/adversarial/`.
5. PLAN.md section 2.7 and 2.9; the code in `test/e2e/` and `internal/noderuntime/e2efault.go`.

## Work order (commit after each item)

### A. Finding 4 — the E4 canary matches any failure
`TestCanaryE4AMigrationThatSkipsDeletingTheSourceFails` passes `""` to
`expectCanaryFailure`. Match the planted reason. Then make the helper refuse an empty
match string (a canary that matches anything is a bug), and check every other caller.

### B. Finding 5 — counters and traffic that are never required to be non-zero
- E2 and E8: require the counted discards (`Health.Discards`) to be non-zero AND consistent
  with what was logged (`RecentDiscards`) — decide the exact relation from the product's
  contract (counted per node that saw the loss; the log is a bounded tail) and state it.
- E9: on SIGTERM require real traffic (`ok > 0`) and real in-flight load at the signal
  (`inflightOK + inflightFailed > 0`), so the scenario cannot pass with nothing in flight.
  If the scenario's timing cannot guarantee in-flight requests, fix the scenario so it can
  (hold the signal until requests are observably in flight), not the assertion.
- Then sweep the other scenarios (E1, E3, E4, E5, E6, E7 and the tier R variants) for the
  same shape — a loop, counter or table that may be empty while the test still passes —
  and close what you find. List each in the notes, including those you judged fine and why.
- **Proof it bites:** for the discard counter, a kept canary (a planted fault where
  discards are logged but not counted, or the nearest thing the test seams allow) that
  makes E2 or E8 fail for that reason. For E9 a self-test of the assertion is enough.

### C. Finding 6 — `s.Err()` instead of `s.Verify()`; `declared` never set
Use `s.Verify()` in `FuzzClusterNemesis` (check every other `s.Err()` in `test/e2e` and
`internal/nodetest` for the same slip and say what you found). Populate
`nemesisStats.declared` or remove it and its MEASURE line — pick one and say why.

### D. Finding 7 — the blocklist reload can miss an equal-size rewrite
`internal/noderuntime/e2efault.go` (build tag `e2efault`): read the file on each throttled
reload instead of trusting `(ModTime, Size)`; a missing file is an empty peer set; any other
read error must not be dropped silently (decide: keep the last good set and surface the
error the way this file already surfaces errors, and say so). Add a test under the tag that
rewrites the file twice with equal-length contents back to back and fails on the old code.
The default build must stay free of the hook: `TestDefaultBuildCarriesNoFaultInjection`
must still pass.

### E. Recorded harness finding C7 — the nemesis runner accepts any error on GET, tag and delete
`(*nemesis).settle` validates terminal errors for PUT only. Validate every operation kind:
each pending call must end in success or an error that is allowed for that operation in the
state the cluster was in (write the allowed set per operation in the notes, from PLAN 2.6
and the product's sentinels; an unknown error, `ErrCorrupt`, or wrong bytes is a failure).
Add self-tests in the style of `oracle_self_test.go`: for each operation kind a planted
completed call with a disallowed error makes `settle` fail; with an allowed error it
passes. If the stricter rule turns up real failures on existing seeds, that is a finding:
pin the seed, keep the replay command, diagnose as far as you can, write it under
"Failures found", and never loosen the rule to make it pass.

### F. Recorded harness finding C10 — the real-cluster constructor accepts a node that failed to start
`newRealCluster` must fail when any intended `objnode` process is not alive and ready by
the end of start-up (a bind failure must surface as a start-up error naming the node and
its log tail), and must leave no stray process behind when it fails. If port allocation
can collide (the record shows node 8's HTTP port equal to node 7's transport port), fix
the allocation so one cluster never hands out the same port twice, and say how a collision
with another process is handled. Add a tier R test with a planted failed launch (for
example a wrapper binary or an occupied port) that fails on the old constructor.

### G. The note (finding 8)
- Seed shares: the note points out that E4 on tier S still writes 8 × 128 KiB objects plus
  about 100 × 64 KiB traffic writes per seed, which is why E4 gets only 25 seeds at
  `DST_SEEDS=500`. Cut tier S E4's object sizes (E1 already covers multi-megabyte and
  multi-stripe objects; keep at least one multi-stripe object in E4 so shard copy of more
  than one stripe is exercised) and use the saved time to raise E4's seed share. Keep
  `test/e2e` at `DST_SEEDS=500` within its present wall time (about 25 minutes) or less.
  Record before/after: seconds per seed, seeds at 500, wall time. Do not change the other
  shares; whether the plan's wording changes is the orchestrator's decision.
- At the default `go test ./test/e2e` every scenario runs one unpinned seed. State that in
  the notes; change nothing unless it is free.
- In the notes' limits section, cite Chunk 4's earlier statement of the lone-holder limit
  (`docs/knowledge/chunk-04-occ.md`) next to the E7 fault-budget narrowing. Notes only.
- ACL and quota writes not reaching `CommitSink`: no change; one sentence in the notes.

### H. Keep the tree green, then write it down
Run through Docker only (`docker compose run --rm -T dev ...`), one run at a time, and
record command and result for each in the notes:
- `gofmt -l .` (empty), `go vet ./...`, `go vet -tags e2efault ./...`, `go build ./...`
- `go test -count=1 ./test/e2e ./internal/node/... ./internal/nodetest/... ./internal/noderuntime/... ./cmd/objnode/...`
- `go test -count=1 -tags e2efault ./internal/noderuntime`
- `DST_SEEDS=500 go test -count=1 ./test/e2e -timeout 60m` (wall time recorded)
- `go test ./test/e2e -run '^$' -fuzz '^FuzzClusterNemesis$' -fuzztime 120s -fuzzminimizetime 5s`
- `E2E_TIER_R=1 go test ./test/e2e -run 'R$|TierR|DefaultBuild|CanaryE9' -count=1 -v -timeout 40m`
- `go test -race -count=1 ./test/e2e ./internal/noderuntime ./cmd/objnode`
- last: `GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e`. The full gate (about two hours) is
  the orchestrator's to run, not yours.

Append to `agent_work/07-e2e/build-notes.md` a new section **"12. Finish round, stage 2
(Neon): REVIEW-1 nits and note, recorded harness findings"**: per item — what was wrong,
what you changed (files, functions), the test that proves it, the canary or self-test that
proves the test bites and the text it matches, numbers measured, anything you could not do,
failures found, and requests to the orchestrator. Plain and exact.

## Rules (builder rules apply, with these differences)

- You are Neon, not Opus; where the rules say "Opus builder", read "you".
- The rule "NO COMMENTS in any `.go` file" STAYS for this stage: write no comments now.
  Comments are written in the later prose pass, once the code is final.
- Go standard library only; no new dependency; the failing test or canary first, then the
  repair, where practical; no sleeps or wall clock on tier S; determinism rules; no dropped
  errors; the frozen simulator packages (`internal/env`, `internal/sim`,
  `internal/sim/simtest`) are not edited.
- Do not touch: PLAN.md, STATE.md, README.md, docs/, book/, kb/, .github/, Dockerfile,
  compose.yaml, agent_work/gates/, agent_work/orch/, agent_work/adversarial/,
  agent_work/INDEX.md, REVIEW-1.md.
- Product code outside `internal/noderuntime/e2efault.go` should not need to change. If you
  are convinced it must, make the smallest change, with a test, and flag it at the top of
  your notes section.
- Git: commit in this worktree as you go, one logical change per commit, subject
  `e2e: <imperative summary>` (or `noderuntime: ...`, `agent_work: ...`), body saying what
  the tests prove, each message ending with the trailer line `Built-By: Gemini 4 Neon`.
  Never push, merge, rebase, reset, or switch branches. Finish with `git status` clean.
- Machine: 4 cores, 7 GB, shared. One Docker test run at a time.
- Do not claim anything you did not run. If a run fails and you cannot fix it, say so.

## When you are done

Final message: the commit list for this stage, one line per item A–G (closed / partly /
not), and any failure found. Then the marker line your launcher asks for.
