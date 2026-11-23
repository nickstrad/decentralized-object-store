VERDICT: FAIL (one major, two minors, three nits, three notes) — round 1 of the finish review

Reviewer: Fable (claude-fable-5-2), reviewing Gemini 4 Neon's finish of Chunk 7 under the
owner's seat-swap ruling of 2026-11-22. Reviewed range: `4626d22..81888eb` on
`chunk/07-e2e` (stage 1: `17bf0e2`, `fe577fe`, `f548b69`, `4f42dfa`; stage 2: `5c392c2`
through `81888eb`), against `REVIEW-1.md`, the two recorded harness findings (C7, C10 in
`agent_work/adversarial/07-e2e/ATTACKS.md`) and the chunk's claims in `build-notes.md`
(sections 3–6, 11, 12) and PLAN 2.7.

The finish is good work: every REVIEW-1 finding is addressed, each repaired hole has a
kept canary, and the stricter checks found a real product defect. It does not pass yet
because one of the new checks fails on a correct product.

## Status of what the finish had to close

| Item | Status | Basis |
|---|---|---|
| REVIEW-1 #1 (fuzzed migrating bucket always empty) | CLOSED | 12 nodes at 2+1; fault-free warm-up write fails any fuzz input that cannot place; every completed migration with a warm-up must leave shards in the destination; corpus test and canary. Read; canary and corpus test run by the builder and in the orchestrator's G1 run |
| REVIEW-1 #2 (timed-out migration start forgotten) | CLOSED, with finding 2 below | Saga looked up on the coordinator after the heal, followed to its end, region refreshed from the converged route. My mutation (found saga, then only refresh the region) is killed by `TestNemesisAmbiguousMigrationStartIsTracked` (`migrated = 0, want 1`) |
| REVIEW-1 #3 (E2 never requires a partition-time read or write) | CLOSED, with finding 4 below | Required-success sets derived from layout, side size and pre-cut shard placement; exact `ErrUnavailable` required where a side cannot serve; bytes and ETag checked on pre-cut reads; three-case canary. Tier R E2 run three times in a row by me: 3 of 3 pass (154 s) |
| REVIEW-1 #4 (E4 canary matches anything) | CLOSED | Helper refuses an empty match; E4 and E5 canaries match their planted reason |
| REVIEW-1 #5 (counters never required non-zero) | CLOSED | Discard counter tied to the logged tail on every node, E9 holds the signal until load is in flight, and a sweep of E1–E8 closed a dozen more empty-loop holes. Canary for the counter |
| REVIEW-1 #6 (`Err` versus `Verify`; `declared`) | CLOSED | |
| REVIEW-1 #7 (blocklist reload) | CLOSED | Test fails on the old code per the builder; default build still free of the hook |
| REVIEW-1 #8 (note) | CLOSED | E4 shrunk, share 1/20 → 1/10 (50 seeds at 500); notes updated. PLAN 2.9 wording is the orchestrator's (see note C) |
| Recorded C7 (`settle` accepts any error on GET, tag, delete) | CLOSED, with finding 3 below | Per-operation allowed sets, 39 self-tests |
| Recorded C10 (constructor accepts a failed launch) | CLOSED | All ports reserved together; start-up requires every intended process alive, ready and in every view; two planted-failure tests on tier R |

## Findings

### 1. [MAJOR] The new E7 sweep-level check fails on a correct product for a single seed
- **Where:** `test/e2e/e7_test.go`, the `t.Cleanup` in `TestE7NemesisHealingCyclesS`
  (`totMig > 0 && totPutsMOK > 0 && totMigShards == 0`).
- **What is wrong:** E7 runs without the warm-up, so a seed whose only completed migration
  happens while bucket `m` is still empty, and whose first acknowledged write to `m` comes
  in a later cycle, trips the check although nothing is wrong. With one seed the totals are
  that seed's.
- **Reproduction (run by me, in a scratch copy of `81888eb`):**
  `go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$' -seed=102 -count=1` → FAIL. The
  builder of stage 2 saw the same and left it for review (notes, section 12).
- **Why major:** a test that fails without a defect breaks the suite's own rule that the
  printed replay command reproduces a failure and nothing else; the default
  `go test ./test/e2e` runs one unpinned seed, so the gate's G1 can go red on the seed
  alone. And the per-migration check that does bite (`warmM > 0 && shards == 0`) is off
  for E7, the scenario the plan names.
- **Required:** make the E7 check exact per migration instead of statistical — either run
  E7 with the warm-up, or compare each completed migration against what bucket `m` held
  when it started — and keep a canary that proves it bites. If you change what E7 does per
  seed, re-check both pinned seeds (`166963932986536` and `1`) still exercise what they
  were pinned for, and say so. Seed 102 must pass on its own.

### 2. [MINOR] The wipe guard for a node with a running saga is not tested
- **Where:** `test/e2e/nemesis_test.go`, `apply` (`|| n.hasActiveSaga(id)`) and the subtest
  "wipe targeting coordinator while start outcome is unknown is skipped".
- **What is wrong:** my mutation removing `|| n.hasActiveSaga(id)` SURVIVES
  `go test ./test/e2e -run 'Nemesis|E7|Fuzz' -count=1` (ok, 28 s). The subtest wipes the
  coordinator while `n.mig != nil`, which the pre-finish condition already protected, so
  it passes on the old code too and proves nothing about the repair.
- **Required:** either a test in which the guard is the only protection (a saga still
  running on a node when `n.mig` is nil — for example a second cycle while the first
  migration is deliberately held open), failing when the guard is removed; or remove the
  guard and say in the notes why `finishMigration` awaiting every saga makes it
  unnecessary. Do not keep untested protection that the notes claim.

### 3. [MINOR] The availability check was loosened without a floor
- **Where:** `test/e2e/nemesis_test.go`, `settle` (commit `d32405c`).
- **What is wrong:** the diagnosis is right (an availability read scheduled before a
  fault and hit by it is not a defect). But the repair voids every availability read of a
  cycle as soon as any network fault is applied in that cycle, including reads that had
  already completed cleanly, and nothing requires the suite to keep any. The notes say
  seed 290 has none at all.
- **Required:** (a) void only the reads the fault could have touched (those not yet
  complete when the fault is applied, or issued through a node touched afterwards), if the
  runner can know that cheaply; if not, say why. (b) Measure `availability_gets` over the
  `DST_SEEDS=500` E7 sweep before and after `d32405c`, record both, and add a sweep-level
  floor that fails if the scenario stops checking availability (written so that a
  single-seed run cannot false-fail — finding 1 is the cautionary example).

### 4. [NIT] E2's early reads on the majority side stay lenient
`earlyGets` on side B (three per seed) succeed on every measured seed on both tiers, and
PLAN 2.7 says a read the surviving shards allow succeeds. They are issued before the views
settle, which was my instruction for writes; a read does not need the view. Require them
when the side holds `k` shards and run the E2 share of the 500-seed sweep; if a seed
fails, keep the lenient rule and record the seed and the reason instead.

### 5. [NIT] Names that say more than the code does
`sideCanPut` computes `max(K+M, min(K+1, K+M))`, which is `K+M`; write that.
`migratedShards` / `e7/migrated_shards` count shards of the bucket present in the
destination region after a completed migration, including objects written there after
the route flipped — not shards moved. Rename or define it in the notes so the chapter
does not print "moved".

### 6. [NIT] One table test for every cause the product joins under `ErrUnavailable`
The fix in `f70e2c2` is correct (see note A). Its two tests cover `ErrNotFound`,
`ErrCorrupt` and `env.ErrNotFound`. Add the other causes a shard request can return
(`env.ErrTimeout`, `env.ErrUnreachable`, `storage.ErrBusy`, `storage.ErrExists`,
`storage.ErrInvalid`) and run the cases through the merged table the node really uses
(`node.CodeTable()`), plus the HTTP table, so the next reordering cannot reopen it.

## Notes (no action by the builder unless stated)

- **A. The product fix (`f70e2c2`) is accepted.** A read of a live object that could not
  gather `k` shards was reported as "not found" (wire code 96, HTTP 404) or "corrupt"
  instead of "unavailable" (99, 503), because the first-match tables listed the per-shard
  causes first. I checked the merged table: application rows precede the transport rows,
  and the storage rows now start with `ErrUnavailable`, so a timeout cause also encodes as
  unavailable. Undoing the fix in a scratch copy makes pinned E7 seed 1 fail with the
  recorded message, so the suite holds the fix as well as the two unit tests. This is a
  product defect found by Chunk 7 and must be told as such in the chapter and the landing
  message (the first build reported none).
- **B. Tier R write concurrency 4 → 1 (`996b289`) is accepted as a stated limit, not as a
  repair.** With four concurrent PUTs the race-built nodes on this 4-core machine exceed
  the one-second shard request timeout and answer 503, which the stricter E2 rightly no
  longer forgives. Lowering the load hides nothing about correctness but means tier R
  (apart from E9) no longer exercises concurrent writes. Builder: add this to the limits
  in the build notes (section 8) in one paragraph, with the product follow-up it points
  at (a configurable shard request timeout — already request 5 of section 9).
- **C. Seed shares.** Accepted as built (shares stated exactly; E4 now 50 seeds at 500).
  The orchestrator changes PLAN 2.9's wording to match and flags it for the owner.
- **Not verified by me in this round:** the full gate on `81888eb` (not yet run), the
  500-seed sweep and the full tier R suite (builder's runs: 1629.78 s ok; 13 tests ok,
  241.93 s), the E9 change (read only). The Opus adversarial pass follows the fix round.

## What I ran (scratch copy of `81888eb`, through Docker)

| Check | Result |
|---|---|
| `go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$' -seed=102 -count=1` | FAIL (finding 1) |
| Mutation: undo `f70e2c2` (both tables), then `go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$' -count=1` | KILLED by pinned seed 1 (`get w/k15 via n017: disallowed error: remote error (code 96) ...`) |
| Mutation: remove `|| n.hasActiveSaga(id)`, then `go test ./test/e2e -run 'Nemesis|E7|Fuzz' -count=1` | SURVIVED (finding 2) |
| Mutation: after an ambiguous start finds its saga, only refresh the region, same command | KILLED (`TestNemesisAmbiguousMigrationStartIsTracked`: `cycles=1: migrated = 0, want 1`) |
| `E2E_TIER_R=1 go test ./test/e2e -run 'TestE2.*R$' -count=3` | ok, 154 s |
| Orchestrator's check after stage 1 (`4f42dfa`): `GATE_ONLY=G0,G1` | pass (G1 268 s) |

Read in full: the stage 1 diff; from stage 2 the diffs of `nemesis_test.go`,
`canary_test.go`, `scenario_e2_test.go`, `realcluster_test.go`, `tierr_test.go`,
`e2efault.go` and its test, `scenario_e4_test.go`, `internal/storage/errors.go`,
`internal/storage/httpapi/http.go` and their tests. Skimmed: the non-zero guards in E1,
E3, E5, E6 and `oracle_self_test.go`.

---

# Round 2 — confirm of the fix round

VERDICT: PASS (all six findings and note B closed; two notes, no action required for landing)

Reviewed range: `87ca0a2..2617109` on `chunk/07-e2e` (the fix round by a fresh Gemini 4 Neon
builder: `00e269e`, `96db03e`, `3911ed0`, `78edab5`, `bf05175`, `4de93e2`, `2d62a51`,
`2617109`). No product code changed in the range.

| Finding | Status | Basis |
|---|---|---|
| 1 (major) E7 check fails seed 102 on a correct product | CLOSED | The sweep-level guess is gone; the runner snapshots the live keys of bucket `m` on the coordinator when the migration starts and requires each of them to hold at least `k` verified shards in the destination region once the migration completes. Seed 102 passes alone (run by me), and so does seed 290. My mutation switching the per-key check off is KILLED by the new E7 canary (`the scenario passed with the planted fault on`). Keys in `m` are unique and never overwritten or deleted by the runner, so a snapshot key cannot legitimately vanish; the builder's 500-seed sweep (301 E7 seeds) passed |
| 2 (minor) untested wipe guard | CLOSED | Guard removed with the reasoning written down (the coordinator is protected while `n.mig` is set, and `finishMigration` follows every started or recovered saga to its end before clearing it); the subtest that proved nothing is replaced by one in which a slow copy step outlives the call timeout, with a canary for the forgotten start. Read, and both run in my default-seed run |
| 3 (minor) availability check loosened without a floor | CLOSED | Reads are voided only when they ended at or after the cycle's first network fault, or the touching of their node. My mutation restoring the wholesale rule is KILLED by `TestNemesisSettleValidatesTerminalErrorsForEveryOp` (`availability get finished before netFaultAt must still fail`). Builder's measurement over the 500-seed sweep: min 6 / p50 20 before the loosening, min 0 / p50 7 after it, min 6 / p50 20 now. Floor of 10 per seed on sweeps of 10 seeds or more, safe for one seed |
| 4 (nit) E2 early reads lenient | CLOSED | Required whenever the side holds `k` shards; builder's runs: 126 of 126 seeds at `DST_SEEDS=500`, tier R 3 of 3 |
| 5 (nit) names | CLOSED | `sideCanPut` is `K+M`; `dest_*` (present in the destination afterwards) and `moved_*` (pre-existing objects only) are separate and defined in the notes |
| 6 (nit) one table test for every joined cause | CLOSED | Eight causes, both join orders, through `node.CodeTable()` and the HTTP table; builder shows them failing with the product fix undone |
| Note B (tier R write concurrency) | CLOSED | Stated as a limit in section 8 of the notes |

## Notes

- **D.** The per-key check sums verified shards over all stripes of an object. Every object the
  runner writes to bucket `m` fits one stripe (warm-up 4 KiB, traffic under 16 KiB, stripe
  64 KiB), so the check is exact today. If multi-stripe objects are ever written to `m`, make
  it per stripe.
- **E. Not a finding; new work from the owner's CI ruling of 2026-11-22** (push-triggered CI
  runs quick tests only). At default seeds the package takes about 310 s here, of which the
  78-cell migration failure matrix (`TestE5MigrationEveryFailurePointS`, three seeds per cell,
  234 runs) is about 970 of roughly 1,090 CPU-seconds. The fast lane needs a short mode for it;
  briefed separately to the builder. The default run and every sweep stay as they are.

## What I ran in round 2 (scratch copies of `2617109`, through Docker)

| Check | Result |
|---|---|
| `go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$' -seed=102 -count=1` | ok |
| same, `-seed=290` | ok |
| Mutation: per-key check off (`if false && have < K`), then `go test ./test/e2e -run 'TestCanaryE7|TestCanaryFuzz|CorpusMovesShards' -count=1` | KILLED by `TestCanaryE7CompletedMigrationMissingPreExistingShardsFails` |
| Mutation: void every availability read once any network fault is on, then `go test ./test/e2e -run 'TestNemesisSettle|TestE7Avail' -count=1` | KILLED by `TestNemesisSettleValidatesTerminalErrorsForEveryOp` |
| `go test -count=1 -v ./test/e2e` | ok, 308.8 s, 13 skips (tier R, off by default) |

Not run by me in this round: the 500-seed sweep, the race run, the fuzz run and tier R (the
builder's results above are taken as reported; the full gate re-runs all of them), and the
Opus adversarial pass, which follows.

---

# Round 3 — review of the fix round for the Opus adversarial findings

VERDICT: NITS (F1–F5 closed, the product fix is accepted; two minors, both missing tests of the new rule; three notes)

Reviewed range: `f82ea4b..ab86c7b` on `chunk/07-e2e` (Gemini 4 Neon, builder: `7654e7a`,
`eaa0556`, `79fb2f6`, `aec000c`, `23e1341`, `ab86c7b`), against
`agent_work/adversarial/07-e2e-opus/ATTACKS.md`. This range changes product code
(`internal/node/migrate.go`, `inventory.go`, `meta.go`). The builder's run was cut off by
its provider's spending cap after the code commits and most of its verification, before
the 500-seed end-to-end sweep, the static gate and its notes; nothing in this verdict
rests on a run that did not happen.

| Finding | Status | Basis |
|---|---|---|
| F1 (major, product) a migration whose coordinator stalls past the orphan grace mid-copy completes and loses acknowledged objects | CLOSED | See "The product fix" below. The adversarial seat's own F1 lines now read HELD (driver `a02`, run by the builder: seeds 31337, 3, 4 keep every acknowledged object). My mutation removing the new verification everywhere is KILLED by three tests in two packages; removing it from the verify step alone is KILLED |
| F2 (major, suite) REPLAY does not replay outside shard 0 | CLOSED | A seed's shard is now `seed mod 4`, the same in a sweep and in a replay; a `-seed` run that matches no seed subtest fails; `TestReplayCommandsReproduceEverySeed` re-executes every printed line. Read; the adversarial driver `a05` re-run is in RESULTS.md |
| F3 (minor) swaps invisible to the oracle | CLOSED | `Retire` reports each swap to the commit sink; with verification off, the canary is now caught by the commit-after-delete oracle itself (seen in my mutation run: `committed ... after its shard ... was deleted`) |
| F4 (minor) do-nothing purge passes tier S | CLOSED | E4 checks the source region the moment the saga completes; builder reports the patch killed on tier S; re-checked by driver `a01` |
| F5 (minor) loss oracle never exercised | CLOSED | E3 now produces a real absence record short of a loss on every seed; builder reports the patch killed; re-checked by driver `a04` |

## The product fix (read in full; accepted)

Before a migration makes a copied version matter, the coordinator asks every holder named
in that version's manifest to confirm its shard (`opVerifyShards`, node channel op 4). The
request carries the staged record itself; the holder merges it durably first, so from that
moment its own inventory expects the shard and will not collect it as an orphan, then
checks the blob against the checksum and answers. Check and protection are the same act,
which closes the gap between checking and acting. This runs at three points: after each
object is copied (plain presence, before the staged record is written), in the verify step
and the post-flip catch-up (through `Root` of the destination), and again in `Retire` before
manifests are swapped. A stripe passes with the write acknowledgement quorum (k+1 by
default) verified; with fewer, the step fails and is retried while the shards could still
be there or be rebuilt, and when fewer than k can exist the staged version is dropped —
before the flip the saga compensates, after it the catch-up copies the object again from
the still-live source. Staged records are now deleted only after every live peer holds the
swapped manifests (they used to go first, which left the same window open at the end).

Checked by reading: holders are visited in sorted order and results are joined by index, so
nothing depends on map order; the callback fires once on every path, including zero
holders and a stopped node; the request decoder bounds its count by the bytes present; a
record for another bucket is refused; no new wire error code (the failure reuses the
"replicas disagree" error); no clock, goroutine or new panic.

## Findings

### R3-1. [MINOR] The shard threshold of the new rule is not held by any test
My mutation `if verified < quorum` → `if verified < 1` (a stripe passes with one verified
shard) SURVIVES `go test ./internal/node -run TestMigration` and the migration tests of
`test/e2e`. Every stall test loses all of a version's destination shards at once, so
"none" is the only failing case exercised. The code is right as read; the test is
missing. **Required:** a test in which a staged version keeps some but fewer than k
verified shards (must not go live; saga compensates before the flip), and one in which it
keeps at least k but fewer than the quorum (must wait, be rebuilt, then complete), each
failing under that mutation.

### R3-2. [MINOR] The re-check in `Retire` is not held by any test
My mutation removing the verification from `Retire` only SURVIVES the same runs. After the
verify step has pinned the shards, a stall can no longer remove them, so the stall tests
never reach this check; it matters when destination holders lose shards (a wipe, bit rot)
between the verify step and the swap. **Required:** a test that wipes destination holders
of a staged version after the verify step and before `Retire` (enough to fall under the
quorum, and separately under k), failing under that mutation; or remove the check and say
why the catch-up's own verification makes it unnecessary.

## Notes

- **F.** Two further mutants survive and are judged equivalent: dropping the holder's
  "my inventory expects this shard" test (the merge just before it already makes the shard
  expected whenever the staged record is current), and dropping the removal of the orphan
  timer (an expected shard never reaches the orphan branch, and the timer map is rebuilt on
  every pass).
- **G.** One verification request per holder carries every staged record of the bucket
  that names that holder, so a bucket's migration is bounded by the 16 MiB frame (roughly
  tens of thousands of objects). The purge and the pointer agreement already send
  whole-bucket messages, so this is the existing design limit, not a new one; it should be
  stated with the other limits in the notes and the chapter.
- **H.** `go test -race ./test/e2e` now takes about 460 s (was about 170 s), so gate G4's
  three repetitions cost about 23 minutes; and the package no longer fits `go test`'s
  default 10-minute timeout under the race detector (the builder's first run died on that;
  the gate passes `-timeout 45m`).

## What I ran in round 3 (scratch copies of `ab86c7b`, through Docker)

`go test ./internal/node -run 'TestMigration' -count=1` and
`go test ./test/e2e -run 'StalledPastOrphanGrace|TestCanaryMigration|TestOracleSeesMigrated|TestE4|CorpusMovesShards' -count=1`
on each mutant:

| Mutant | Result |
|---|---|
| none | ok |
| verification removed from `Root` and `Retire` | KILLED (`TestMigrationStalledAfterStageWithoutSyncCompensates`, `TestMigrationCheckThenActGapClosed`, `TestCanaryMigrationUnverifiedStallLosesObject`) |
| removed from `Root` only | KILLED (the first two) |
| removed from `Retire` only | SURVIVED (R3-2) |
| the per-object check after each copy removed | KILLED (`TestMigrationCoordinatorStalledPastOrphanGraceMidCopy`, invariant "no shard of a possible winner deleted") |
| holder does not merge the pushed record | KILLED (migrations can no longer complete: five tests) |
| threshold lowered to one verified shard | SURVIVED (R3-1) |
| holder confirms without the expectation test | survived, equivalent (note F) |
| orphan timer not cleared on confirm | survived, equivalent (note F) |

Builder's runs taken as reported (from its log; the full gate repeats them): unit tests of
the node, saga, storage and end-to-end packages ok; `-short` end-to-end 73 s; 120 s fuzz of
`FuzzClusterNemesis` ok; tier R E4 and E5 ok; race ok (node 427 s, end-to-end 460 s);
`DST_SEEDS=500 ./internal/node` ok (1502 s); node coverage 86.8%. Not run by anyone yet on
this tip: the 500-seed end-to-end sweep and the static gate — both are in the full gate.

---

## Round 4 — the two missing tests (R3-1, R3-2) and the loopback port race (R3-3)

VERDICT: PASS (R3-1 and R3-2 closed; R3-3, found by the full gate's race step, closed)

Reviewed: `4fd5621..4c85be7` (three test commits and build-notes section 17; no product
file changed — `git diff --stat` shows `_test.go` files, the notes and the brief only).

- **R3-1 closed.** `TestMigrationVerifyBelowKCompensates` and
  `TestMigrationVerifyBelowQuorumWaitsForRebuild` (three seeds each) leave exactly 3, then
  exactly 4, verified destination shards per stripe before the verify step. The second
  asserts the waiting itself: the flip must not run while a stripe holds fewer than the
  quorum.
- **R3-2 closed.** The builder showed the check in `Retire` is reachable (a holder wiped
  after it answered the catch-up's verification and before the swap) and added
  `TestMigrationRetireBelowQuorumWaitsForRebuild` and
  `TestMigrationRetireBelowKRecopiesAndNeverGoesLive` (three seeds each); a commit sink
  checks every manifest swap.
- **R3-3 closed.** The gate failure (`TestConformanceOnLoopback`, "listen udp :40303:
  bind: address already in use", then six leaked goroutines) was a reserve-then-release
  race in the test helper. One helper now starts every loopback runtime in the package,
  retries the whole cluster on fresh ports on address-in-use only (five attempts), and
  closes what it started on every failure path; the two other packages with the same
  pattern (`cmd/objnode`, the real-process cluster in `test/e2e`) were given the same
  bounded retry. A pinned-port start still fails at once.

### What I ran in round 4 (scratch copy of `4c85be7`, through Docker)

| Mutant | Command | Result |
|---|---|---|
| none | `go test -count=1 ./internal/node -run 'TestMigrationVerifyBelow\|TestMigrationRetireBelow'` | ok |
| `if verified < quorum` → `if verified < 1` | `go test -count=1 ./internal/node -run TestMigration` | KILLED (`TestMigrationVerifyBelowQuorumWaitsForRebuild`, `TestMigrationRetireBelowQuorumWaitsForRebuild`, all seeds) |
| `verifyDest` in `Retire` replaced by immediate success | same | KILLED (`TestMigrationRetireBelowQuorumWaitsForRebuild`, `TestMigrationRetireBelowKRecopiesAndNeverGoesLive`, all seeds) |
| retry on address-in-use switched off | `go test -count=1 ./internal/noderuntime -run TestStartRealRetries` | KILLED (two of three subtests) |

Both round-3 survivors are now killed. Builder's runs taken as reported (build-notes
section 17); the full gate repeats them.

Note, no action: the retry matches the error by text as well as by `syscall.EADDRINUSE`,
because the runtime wraps the bind error in a message; fine for a test helper.

NITS CLOSED (2026-11-23): R3-1 and R3-2 closed by tests that kill both round-3 mutants.
