# Brief — Chunk 7 FINISH, stage 1 of 2: close the three majors of REVIEW-1

Seat: Gemini 4 Neon, **BUILDER** (fresh agent). You are in a git worktree, branch
`chunk/07-e2e`, and it is your current directory. Work only here.

## Why you, and what is different this time

The project owner ruled (2026-11-22) that Neon finishes Chunk 7 as its builder. For this
finish the usual "Neon writes prose only" boundary is LIFTED: you write and fix Go code and
tests in this worktree. Another seat (Fable) reviews your diff afterwards, and a third
(Opus) then tries to break your repaired tests with mutations. So the standard is: each
repaired check must demonstrably FAIL when the thing it guards is broken.

You are a builder in this stage, not a reviewer. Do not write another review. Fix things.

## Read first

1. `agent_work/07-e2e/REVIEW-1.md` — the review you are closing. Findings 1, 2 and 3 are
   this stage. (Findings 4–8 are the next stage, done by another agent: leave them.)
2. `agent_work/07-e2e/objstore-p2-builder-rules.md` — the builder rules. They bind you
   as written for Opus, with the two exceptions listed under "Rules" below.
3. `agent_work/07-e2e/build-notes.md` — what the first builder built and claims
   (sections 2, 3 (E2, E7), 4 (canaries), 5, 6).
4. PLAN.md section 2.7 (the E1–E9 scenario claims) and 2.9; `agent_work/0b-simulator/build-notes.md`,
   "Semantics Wave B builders must know" (what `ErrTimeout` means on the simulator).
5. The code: `test/e2e/` (start with `e7_test.go`, `nemesis_test.go`, `scenario_e2_test.go`,
   `canary_test.go`, `simcluster_test.go`, `harness_test.go`).

## Work order (in this order; commit after each)

### A. Finding 1 — the fuzzed migrating bucket is always empty
`FuzzClusterNemesis` runs 9 nodes (3 per region) at `k=4, m=2`, so every PUT to the routed
bucket `m` fails placement and every fuzzed migration moves nothing.
- Fix the fuzz configuration so the routed bucket really holds objects and migrations really
  copy, verify and purge shards while faults run. Choose the node count and layout
  yourself and say why in the notes; think about what a single node fault inside the routed
  region does to placement (the review suggests 2+1 with 3 or 4 nodes per region). Keep one
  fuzz execution cheap: record executions per second before and after.
- Make the hole impossible to reopen silently. `nemesis.settle` currently accepts a
  placement failure on bucket `m` without notice. Add a check with teeth, for example:
  the runner counts acknowledged PUTs to bucket `m` and shards actually moved by completed
  migrations (`nemesisStats`), and a test over the seed corpus (and over the E7 scenario)
  fails when a run that completed a migration moved zero shards in total, or when no PUT to
  `m` was ever acknowledged in a fault-free prefix. Decide the exact form; it must not
  flake on a legitimately unlucky single input (assert over a corpus or over a fault-free
  warm-up, not on every fuzz input).
- **Proof it bites:** a kept canary test in `canary_test.go` style (a test-only switch that
  restores the old 9-node 4+2 options; the canary passes only when your new check fails,
  with the error text matched).
- Check the E7 scenario itself (`e7Options`) and tier R for the same hole and state in the
  notes what you found.

### B. Finding 2 — a timed-out migration start is treated as "nothing started"
`nemesis.finishMigration` returns nil on `env.ErrTimeout` / `env.ErrUnreachable` from
`StartMigration` and forgets the migration, though on the simulator a timeout means
"outcome unknown".
- After heal and convergence, when the start result was ambiguous, find out what really
  happened (the coordinator's saga engine, or the migration registration in converged
  metadata — use whatever the product honestly exposes; do not add product API unless
  there is no other way, and if you must, keep it minimal and say so). If a migration
  exists: await its terminal state and run the same post-migration checks as for an
  acknowledged start (`shardsOutside`, counters). Always refresh `n.region` from the
  converged route. Keep whatever protects a live coordinator from `opWipeNode` in force
  while the outcome is unknown.
- Review `ErrBusy` / `ErrUnavailable` on the same line: are those really "certainly not
  started"? Keep or change, and write the reasoning.
- **Proof it bites:** a deterministic test (fixed seed, explicit plan — not left to fuzz
  luck) in which `StartMigration` is applied on the coordinator but its reply is lost, so
  the caller sees a timeout while the migration runs to completion; the test asserts the
  runner tracked it (region refreshed, post-checks ran, stats counted). Plus a kept canary:
  a test-only switch restoring the old "forget it" behaviour makes that test fail for the
  stated reason.

### C. Finding 3 — E2 never requires a partition-time read or write to succeed
PLAN 2.7 E2: during a region partition both sides keep accepting metadata writes and any
object read/write the surviving shards allow; anything else is `ErrUnavailable`.
- In `scenarioE2` (both tiers share it — keep tier R passing; mind its smaller cluster and
  2+1 layout where that applies), assert what MUST succeed, derived from layout and side
  sizes rather than hard-coded: a GET of a pre-cut key on a side that holds at least `k`
  of its shards succeeds with the right bytes; a PUT issued to a side with at least the
  nodes placement and the ack quorum need succeeds; and the scenario saw a non-zero number
  of each. Where a side cannot serve, `ErrUnavailable` stays the only allowed answer.
  Be exact about timing: only require success once the side's membership view has settled
  to the split (the scenario already has `splitViews`); do not paper over a real product
  refusal by loosening the rule — if the product refuses something the plan says it should
  serve, STOP on that point, write it up in the notes as a suspected product defect with
  the seed and replay command, and go on with the rest.
- **Proof it bites:** a kept canary reproducing the review's reproduction: a planted fault
  that makes object PUT and GET answer `ErrUnavailable` while the cluster is split (tags
  and ACLs still work) must make E2 fail for that reason (error text matched).

### D. Keep the tree green, then write it down
Run through Docker only (`docker compose run --rm -T dev ...`), and record command and
result for each in the notes:
- `gofmt -l .` (empty), `go vet ./...`, `go build ./...`
- `go test -count=1 ./test/e2e` and `go test -count=1 ./internal/node/... ./internal/nodetest/... ./internal/noderuntime/...`
- `DST_SEEDS=50 go test -count=1 ./test/e2e -run 'TestE2|TestE7|TestCanary'`
- `go test ./test/e2e -run '^FuzzClusterNemesis$' -count=1` (seed corpus) and
  `go test ./test/e2e -run '^$' -fuzz '^FuzzClusterNemesis$' -fuzztime 120s -fuzzminimizetime 5s`
- `E2E_TIER_R=1 go test ./test/e2e -run 'TestE2.*R$' -count=1 -v -timeout 20m`
- `go test -race -count=1 ./test/e2e -run 'TestE2|TestE7|TestCanary'`
- `agent_work/gates/gate.sh` is NOT yours to run (about two hours; the orchestrator runs it
  between stages). But `GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e` is quick: run it last
  and record the result.
If the new fuzz configuration or the stricter E2 exposes a real failure (a seed that
fails), that is a finding, not an obstacle: pin the seed in `test/e2e/testdata/seeds.txt`
if the suite has that convention for it, keep the replay command, diagnose it as far as
you can, and write it in the notes under "Failures found". Never weaken a check to make a
seed pass, and never delete a failing seed.

Append to `agent_work/07-e2e/build-notes.md` a new section **"11. Finish round, stage 1
(Neon): REVIEW-1 majors"**: per finding — what was wrong, what you changed (files,
functions), the test that proves the repair, the canary that proves the test bites and the
exact text it matches, numbers measured (fuzz executions per second before/after, objects
and shards moved per fuzz run, E2 partition-time PUTs/GETs that succeeded per seed on
each tier), anything you could not do, and requests to the orchestrator. Plain and exact.

## Rules (builder rules apply, with these two differences)

- You are Neon, not Opus; where the rules say "Opus builder", read "you".
- The rule "NO COMMENTS in any `.go` file" STAYS for this stage: write no comments now.
  Comments are written in the later prose pass, once the code is final.
- Everything else holds: Go standard library only; no new dependency; tests before the fix
  where practical (the canary and the failing assertion first, then the repair); no sleeps
  or wall clock on tier S; determinism rules; no dropped errors; the frozen simulator
  packages (`internal/env`, `internal/sim`, `internal/sim/simtest`) are not edited.
- Do not touch: PLAN.md, STATE.md, README.md, docs/, book/, kb/, .github/, Dockerfile,
  compose.yaml, agent_work/gates/, agent_work/orch/, agent_work/adversarial/,
  agent_work/INDEX.md, REVIEW-1.md.
- Product code (`internal/`, `cmd/`) should not need to change in this stage. If you are
  convinced it must, make the smallest change, with a test, and flag it at the top of your
  notes section.
- Git: commit in this worktree as you go, one logical change per commit, subject
  `e2e: <imperative summary>` (or `agent_work: ...` for notes), body saying what the tests
  prove, and end each commit message with the trailer line `Built-By: Gemini 4 Neon`.
  Never push, merge, rebase, reset, or switch branches. Finish with `git status` clean.
- Machine: 4 cores, 7 GB, shared. One Docker test run at a time.
- Do not claim anything you did not run. If a run fails and you cannot fix it, say so.

## When you are done

Final message: the commit list (`git log --oneline e3b05da..HEAD`), one line per finding
(closed / partly / not), and any failure found. Then the marker line your launcher asks for.
