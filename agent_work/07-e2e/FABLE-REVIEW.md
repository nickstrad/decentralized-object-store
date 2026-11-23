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
