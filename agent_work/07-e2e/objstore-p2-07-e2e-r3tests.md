# Brief — Chunk 7 FINISH: the two missing tests of the migration shard-verification rule, and a port race in a test helper

Seat: the chunk's builder (fresh agent). You are in a git worktree, branch `chunk/07-e2e`,
and it is your current directory. Work only here. Small and bounded: tests only, expect
about an hour. Product code is NOT to change unless a new test shows a wrong answer — then
stop, describe it in the notes, and fix only that.

## Read first

1. `agent_work/07-e2e/FABLE-REVIEW.md`, "Round 3": findings R3-1 and R3-2 are this brief.
2. `agent_work/07-e2e/build-notes.md` section 15 (the rule and its threshold).
3. `internal/node/migrate.go` (`verifyDest`, `verifyTargets`, `Retire`, `Root`),
   `internal/node/inventory.go` (`verifyShards`), and the existing tests in
   `internal/node/dst_migstall_test.go` (reuse its harness and hooks).
4. `agent_work/07-e2e/objstore-p2-builder-rules.md` (NO comments in `.go` files).

## R3-1: the shard threshold

Today every stall test removes all destination shards of a version at once, so lowering the
threshold in `verifyTargets` from the quorum to one verified shard (`if verified < quorum`
→ `if verified < 1`) passes every test. Add, in `internal/node`, on at least three seeds
each, deterministic tests in which, after the copy and before the verify step:

- (a) a staged version keeps some but FEWER THAN k verified destination shards (remove or
  corrupt the others on their holders' disks): the saga must end `compensated`, the object
  must stay live and readable byte-for-byte from the source region, no loss record;
- (b) it keeps AT LEAST k but FEWER THAN the quorum: the verify step must not pass until
  the missing shards have been rebuilt on the destination; the saga then ends `completed`
  and every stripe holds at least the quorum of verified shards in the destination. Assert
  the waiting, not only the end state (for example: at the moment the flip step first
  runs, the quorum is verified).

## R3-2: the re-check in `Retire`

Removing the `verifyDest` call from `Retire` alone passes every test. Add tests in which
destination holders of a staged version are wiped (or its shards removed) AFTER the verify
step has passed and BEFORE `Retire` swaps manifests: (a) enough to fall under the quorum
but not under k — `Retire` must not swap until the shards are rebuilt, then completes;
(b) under k — the version must never go live; the object stays readable (from the source
through the catch-up re-copy) and the saga still reaches `completed`, with no loss record.
If you conclude instead that the check in `Retire` is unreachable because the catch-up's
own verification always runs first, prove that with a test and say so in the notes; do not
remove product code in this brief.

## R3-3: a port race in the loopback test helper (found by the full gate's race step)

The full gate's race step failed once, in `internal/noderuntime`:

    --- FAIL: TestConformanceOnLoopback/conditional-write-race
        conformance_test.go:299: node: start 127.0.0.1:40303: listen :40303: listen udp :40303: bind: address already in use
    leakcheck: goroutines leaked: 6 still running after 5s (Runtime.loop, readDatagrams, accept of the two nodes already started)

Cause (orchestrator's diagnosis; check it): `freeAddr` in
`internal/noderuntime/runtime_net_test.go` (and its twin `loopbackAddr` in `hazards_test.go`)
finds a free port by binding and RELEASING it; `startReal` picks all its addresses first and
binds them later. Anything on the machine may take a released port in between — the race
step runs packages in parallel, and `test/e2e` starts real processes that open sockets in
the same ephemeral range. This is a fault of the test helper, not of the runtime. Also,
`startReal` registers its cleanup only after every node has started, so a failed start
leaks the nodes started before it (the leakcheck lines).

Fix, in test files only:
- `startReal` (and any other helper in `internal/noderuntime` tests that starts a runtime
  on an address from `freeAddr`/`loopbackAddr`) must survive a lost port: when a `Start`
  fails with `address already in use`, close every runtime it has started, pick fresh
  addresses for the WHOLE cluster (the seed list names them) and try again, a bounded
  number of times (the existing retry in `cmd/objnode/main_test.go`,
  `TestServeAndShutdownOnCancel`, is the house pattern). Any other start error fails the
  test at once, as today. After the bound, fail with the last error.
- Runtimes started before a failure are always closed (no leak on any failure path).
- One helper, not two copies, if that is a small change.
- A deterministic test of the retry: occupy the UDP side of an address the helper is about
  to use (a hook or an injected address picker — test-only, unexported), and show the
  cluster still starts, on other ports, and that nothing leaks. Show also that a start
  error that is NOT address-in-use still fails at once.
- Look at `test/e2e/realcluster_test.go` (`allocPorts` or its current name, about line
  130) and `cmd/objnode/main_test.go`: say in the notes, for each, whether a released port
  taken by someone else is survived there (retry present) or not. Fix only if it is not.
Proof that it bites: with the retry removed (scratch copy), the new test fails. Then run
`go test -race -count=20 ./internal/noderuntime -run 'TestConformanceOnLoopback'` once and
record the result.
One commit, subject `noderuntime: ...`, body saying what the test proves.

## Proof that the tests bite (required, in a scratch copy under /tmp, never in the tree)

- With `if verified < quorum {` changed to `if verified < 1 {`: the R3-1 tests fail.
- With the `g.verifyDest(m, id, ...)` call in `Retire` replaced by an immediate success:
  the R3-2 tests fail.
Paste the failing lines into the notes.

## Verify (through Docker only, one run at a time; record command, result, time)

`gofmt -l .`; `go vet ./...`; `go test -count=1 -cover ./internal/node` (coverage must stay
at or above 85%); `DST_SEEDS=50 go test -count=1 ./internal/node -run 'TestMigration'`;
`go test -race -count=1 ./internal/node -run 'TestMigration'`;
`GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e`, then `git checkout -- agent_work/07-e2e/gates`
if tracked files there changed.

## Rules

- Go standard library only; no sleeps or wall clock; no dropped errors; frozen simulator
  packages not edited; a failing seed is a finding — pin it, never loosen a check.
- Do not touch: PLAN.md, STATE.md, README.md, docs/, book/, kb/, .github/, Dockerfile,
  compose.yaml, agent_work/gates/, agent_work/orch/, agent_work/adversarial/,
  agent_work/INDEX.md, REVIEW-1.md, FABLE-REVIEW.md.
- Git: one commit per finding, subject `node: <imperative>`, body saying what the tests
  prove, ending with the trailer line `Built-By: Gemini 4 Neon`; one
  commit appending **"17. Tests of the shard threshold and the retire-time check; the loopback port race"** to
  `agent_work/07-e2e/build-notes.md`. Never push, merge, rebase, reset, stash or switch
  branches. Finish with `git status` clean.
- Machine: 4 cores, 7 GB, shared. Do not claim anything you did not run.

Final message: the commits, one line per finding (closed / partly / disputed), the
mutation results, anything not done.

## Dispatch notes (orchestrator, 2026-11-23)

- Builder: Gemini 4 Neon (seat-swap ruling for this chunk's finish). Trailer: `Built-By: Gemini 4 Neon`.
- You are in a SECOND worktree of the chunk (branch `chunk/07-e2e-r3`, same commit as
  `chunk/07-e2e`): a long test run is using the first one. Work only in the current
  directory. The machine is busy with that run: one Docker run at a time, and expect
  tests to take longer than usual; a timeout under load is reported, not "fixed" by
  loosening.
- `agent_work/07-e2e/build-notes.md` already has a section 16; yours is section 17.
- Three findings now: R3-1, R3-2, R3-3. One commit each, then the notes commit.
