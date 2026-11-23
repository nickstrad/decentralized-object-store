# Chunk 7 adversarial pass (Opus): ATTACKS

ADVERSARIAL VERDICT: FINDINGS (2 MAJOR, 3 MINOR, 7 notes): F1 and F2 must be fixed or answered before the chunk lands

Seat: Claude Opus 5.5, adversarial tester (fresh agent). Branch `chunk/07-e2e` at `796e127`.
Every Go command ran in the repo's Docker `dev` service on a scratch copy of the tracked tree
under `/tmp` (minus `agent_work/` and `book/`). The drivers copy the tree, patch it, run it and
delete it. The worktree was never modified. Mutations are patch files in `mutations/`, generated
by `tools/mkpatches.py` from exact string edits that fail loudly if the code moves.
`tools/compilecheck.sh` builds every patch, and all compiled. Docker runs from the scratch copy
(`docker compose run --rm -T dev`, which mounts that copy at `/src`). The Go helper code is test
files in package `e2e` (`tools/*.go.txt`, copied into the scratch tree by the drivers), because
the runner (`nemesis`, `runFuzzInput`, `planFromBytes`) is unexported test code that a nested
module cannot import. So there is no nested Go module.

Final run: `bash agent_work/adversarial/07-e2e-opus/run-all.sh`, which writes
`evidence/run-all.txt` (summary at the end of this file). `evidence/first-run/run-all.txt` and
`run-all-second.txt` hold the two earlier full runs, made before driver bugs were fixed (see
"Harness corrections"). Their substantive results match the final one.

## Findings

| # | Grade | Area | One line |
|---|---|---|---|
| F1 | MAJOR | product | A migration whose coordinator stalls longer than OrphanGrace mid-copy completes and loses acknowledged objects |
| F2 | MAJOR | suite | The printed REPLAY command reproduces nothing for a sweep seed outside `shard=0`, and the replay run passes |
| F3 | MINOR | oracle | Migration manifest swaps bypass the commit sink: the loss and delete-then-commit oracles are blind to migrated versions |
| F4 | MINOR | suite | A migration whose purge step does nothing passes all of tier S; only tier R E4 kills it |
| F5 | MINOR | suite | The loss-declaration oracle is never exercised by a scenario: a product that declares loss on a single absence record passes E3, E6, E7 and the fuzz corpus |

### F1 [MAJOR, product] A migration whose coordinator stalls longer than OrphanGrace mid-copy completes and loses acknowledged objects

- **What happens.** Bucket `m` holds two acknowledged warm-up objects, written with no faults.
  A migration of `m` starts. Right after the copy starts (100 ms after the start call), the
  coordinator is paused for 70 s; the fuzz layout's OrphanGrace is 60 s. The coordinator then
  resumes and the saga ends `completed`. The live manifest of `m/warm00` now names three holders
  in the destination region, and none of them holds its shard:
  `verifiable=0 K=2 holders [n001(r1)=absent n007(r1)=absent n004(r1)=absent]`
  (`evidence/adhoc/pause70-seed31337-holders.log`). The designated node then declares the object
  lost. An acknowledged object is gone.
- **How often (measured).** With a 70 s pause, 6 of 12 seeds lose an object; with 15 s and 30 s
  pauses, 0 of 12 (`evidence/adhoc/pause15-30-70-12seeds.log`). A plain migration with no lost
  reply loses an object on 5 of 6 seeds (`evidence/adhoc/pause70-no-lost-reply.log`). With
  OrphanGrace raised to 300 s, nothing is lost on the same seeds
  (`evidence/adhoc/pause70-orphangrace300.log`). The final run reproduces it as check
  `a02-coordinator-paused-past-orphan-grace-loses-object`, alongside its control.
- **Cause (read in the code, consistent with every run above).**
  1. A destination node treats a shard as expected only when a live manifest or a *staged*
     migration record names it (`internal/node/inventory.go`, `expectedAll`).
  2. `Copy` writes the copy intent `~cv/<saga>/<version>` before `cp.Put`, but nothing in the
     inventory consults intents. The staged record is written only after `cp.Put` returns
     (`internal/node/migrate.go`).
  3. So if the coordinator freezes after the copy's shards land but before the staged record
     replicates, the destination records the shards as orphans and collects them after
     OrphanGrace.
  4. When the coordinator resumes, it stages the copy and passes `verify`. Verify compares
     Merkle roots over `(key, content hash, size)` taken from *manifests* (`migrator.root`). It
     checks neither shard presence nor the `~orphan/` records the destination wrote.
  5. The saga then flips, and `Retire` swaps each live manifest onto the collected version.
- **Smallest reproduction.** Copy `tools/opus_sched_test.go.txt` to
  `test/e2e/opus_sched_test.go`, then run
  `OPUS_NOPART=1 OPUS_ONLY=lost-reply-then-pause-past-heal-bound OPUS_SEEDS=31337 go test ./test/e2e -count=1 -run '^TestOpusLostReplyCoordinatorSchedules$'`.
  It fails with `n000 declared ~lost/5d9ac6cbe4491d3d of m/warm00 lost, ...`. Driver:
  `a02-ambiguous-start.sh`.
- **Why the suite cannot see it.** No E7 or fuzz fault outlives one 4 s chaos phase, because
  the heal resumes and restarts everything. OrphanGrace is 20 s in E7 and 60 s in the fuzz
  layout. Tier R E5 kills the coordinator instead of freezing it.
- **Grade.** MAJOR rather than BLOCKER, because it needs one process frozen (SIGSTOP, VM
  suspend, a stalled host) for longer than OrphanGrace, which is 1 h by default. It sits next
  to the stated limit OQ-13 ("a coordinator that never returns") but is not inside it: this
  coordinator returns, and the store then silently drops acknowledged data. It needs a fix, or
  a written widening of the limit to "a coordinator stalled longer than OrphanGrace". Possible
  fixes:
  - let copy intents protect their version's shards on the destination; or
  - have verify, catch-up or `Retire` refuse a staged version that has `~orphan/` records or
    missing shards.

### F2 [MAJOR, suite] REPLAY does not replay a sweep seed outside shard 0

`sweep` (`test/e2e/harness_test.go`) puts each seed in subtest `shard=<i mod 4>/seed=<s>`.
`simtest.ReplayCommand` prints `-run '^Test$/^shard=<k>$/^seed=<s>$' -seed=<s>`. With `-seed`,
`seedsFor` returns that one seed, which `sweep` puts in `shard=0`. So for any failing seed in
shards 1–3, the printed command matches no seed subtest: the top-level test runs no seed, its
sweep floors need at least 10 seeds, and the replay run reports `ok`.

A default `go test ./test/e2e` runs the two pinned seeds plus one fresh seed, at indexes
0, 1, 2. So whenever the default run fails on its fresh seed, the REPLAY it prints passes.

Measured in `a05-robustness.sh`: a failure planted at cycle 1 in every E7 seed
(`mutations/p3-plant-e7-failure.patch`, `DST_SEEDS=8`; six seeds fail), after which every
printed REPLAY line is run. The two in `shard=0` reproduce the failure. The four in shards 1–3
exit 0 having run no seed subtest, for example
`go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$/^shard=2$/^seed=2$' -seed=2 -count=1` → `ok`
(`evidence/a05-replay-shard=2-2.log`).

FABLE-REVIEW finding 1 already treats "the printed replay reproduces a failure" as a rule of the
suite, so this is MAJOR. Fix: have `sweep` keep a seed's shard when the run is given one seed
(for instance, take the shard from the seed's position in the full list), or drop the shard
level from the printed pattern.

### F3 [MINOR, oracle] Migration manifest swaps bypass the commit sink

`Retire` swaps manifests with `g.n.occ.Put(...)` + `RecordMeta` and never calls `n.committed`.
So `simOracle.commit` never sees a migrated version:
- `o.mans` lacks it. The loss oracle reports any loss of a migrated object as "a version no node
  ever committed" (F1's runs) instead of judging it. A *wrong* loss declaration of a migrated
  object would get the same misleading message. It still fails the run.
- The check "version committed after its shards were deleted" never runs for a Retire swap.
  That is exactly F1's event, so the oracle built for it was blind. F1 surfaced only through the
  later loss declaration; the runner's per-key shard check or `checkLiveReadableOrLost` would
  also have caught it.

Fix: report the swaps to the commit sink, or let the harness learn manifests from replicated
state as well as from commits.

### F4 [MINOR, suite] Purge no-op passes tier S

`mutations/p11g-retire-purge-noop.patch` turns `Retire`'s purge of the dead source versions
into a no-op. On tier S, the background GC collects those versions (they are marked dead)
within GCGrace: 10 s by default, 5 s in E7. That is inside the 40 s (E4) and 60 s (nemesis)
await bounds. So E4S, E7, `FuzzClusterNemesis` and the corpus test all pass. Only
`TestE4MigrationUnderTrafficR` kills it (GC grace 10 min: `126 shards of e4 outside r1 after the
migration`), and that test runs only in gate G5R.

The end-state claim "zero shards of the bucket left in A" still holds on tier S. What tier S
cannot see is whether the migration itself cleans up. Fix: on tier S, check the source region
right after the saga ends, before GCGrace has passed; E4 already measures
`purge_after_terminal`.

### F5 [MINOR, suite] The loss-declaration oracle is never exercised by a scenario

`mutations/p2f-loss-declared-on-one-absence.patch` makes the product declare a version lost as
soon as one holder records an absence, instead of more than m. It SURVIVES E3, E6 and E7 at
`DST_SEEDS=20`, and the fuzz corpus test (line `a04-oracle-loss-declared-early`). No file in `test/e2e` causes an absence record, so the oracle
"declared lost while k verifiable shards exist" is covered only by its self-test
(`TestLossOracleFlagsADeclarationWhileEveryStripeHasKShards`). The other six oracles each killed
the product mutation aimed at them (Part 2 table).

### Notes (no action required for landing)

- **N1.** `p11e` (routed-bucket PUTs answer `ErrUnavailable` once the bucket holds two objects)
  is killed only by the corpus total in `TestFuzzClusterNemesisCorpusMovesShards` and by E4. It
  survives every individual `FuzzClusterNemesis` input and E7 at default seeds, so a `-fuzz`
  campaign by itself never flags "all traffic writes to `m` fail with an allowed error". The
  warm-up objects keep every migration non-empty, so the repaired claim still holds.
- **N2.** `p11c` (routed-bucket PUTs ignore the route and place shards anywhere) and `p11j`
  (`Retire` never marks the replaced version dead) are killed only by E4S. The nemesis runner
  checks region confinement only at the end of a migration. By then the copy has re-placed
  everything, and orphan GC (20 s in E7) has removed what was never marked dead.
- **N3.** `p12a`, forgetting a start that failed with `ErrUnreachable`, survives 30 E7 seeds,
  the corpus and the ambiguous tests. It is an equivalent mutant: by the simulator contract,
  `ErrUnreachable` on a request is a refusal by a down node or an unknown node, so
  `StartMigration` never ran.
- **N4.** With OrphanGrace 300 s (F1's control), 3 of 6 seeds fail the runner's "no shard of the
  migrated bucket outside its region within 60 s". After a stalled coordinator, leftover copy
  shards go only through orphan GC, so that 60 s bound depends on OrphanGrace being shorter.
- **N5.** After SIGKILL or SIGINT of the tier R test binary with nine nodes up, no `objnode`
  survives (`Pdeathsig`), but two `e2e-*` temp directories stay in `$TMPDIR`. Cleanup cannot
  run after the signal; inside the `--rm` container they go with the container.
- **N7.** A product that serves the pre-split version of a key overwritten during the split
  (`p13h`) passes E2 on tier S. Every partition-time GET in E2 is issued at the same moment as
  the overwrite, so the old version is a legal concurrent answer. E2 never reads a key after an
  acknowledged split-time write and before the heal. E2's claim (bytes, unavailable or not
  found; never wrong bytes) does not promise read-your-writes while split, so this is not a
  broken claim. If the owner wants that property, one read after the acknowledged puts settle
  would pin it down.
- **N6.** Removing the runner's "never wipe the coordinator of a running migration" guard
  (`p12d`) makes E7 fail at `DST_SEEDS=30` (`migration not coordinated here`). The guard is
  load-bearing: it keeps the runner inside OQ-13 and never hides a defect.

## Part 1 mutation table (which test killed it, or SURVIVED)

Tier S runs use default seeds (two pinned seeds plus one fresh) unless stated. "Fuzz set" =
`TestFuzzClusterNemesisCorpusMovesShards|^FuzzClusterNemesis$` (seed corpus only, no `-fuzz`).

### 1. The fuzzed migrating bucket (`a01-fuzz-bucket.sh`)

| Patch | Mutation | Result |
|---|---|---|
| p11a | fuzz layout back to 9 nodes, k=4 m=2 | KILLED: fuzz set, `fault-free warm-up put to routed bucket m ... placement: 3 members for 6 shards` |
| p11b | fuzz layout 6 nodes (2 per region), k=2 m=1 | KILLED: fuzz set, `... 2 members for 3 shards` |
| p11d | bucket `m` gets a 1-object quota (puts fail another way) | KILLED: fuzz set (warm-up `quota exceeded`) and E7 (`put m/... disallowed error ... quota exceeded`) |
| p11e | product: routed PUT answers unavailable once the bucket holds 2 objects | KILLED: corpus test (`acknowledged 0 traffic puts to routed bucket m`), E4S; survives each fuzz input and E7 (N1) |
| p11c | product: routed PUT ignores the route (wrong region) | KILLED: E4S only (`placed a shard on n003 in r0: that node does not route the bucket to r1`) (N2) |
| p11f | product: copy step writes nothing (stages the source manifest) | KILLED: fuzz set, E4S, E7 (`migration ... not terminal within 1m0s`) |
| p11i | product: copy skips the last object | KILLED: fuzz set, E4S, E7 (not terminal) |
| p11h | product: copy skips the last object and the catch-up verify is a no-op | KILLED: fuzz set, E4S, E7 (`no shard of the migrated bucket outside its region`) |
| p11g | product: Retire's purge is a no-op | SURVIVED tier S (fuzz set, E4S, E7); KILLED tier R `TestE4MigrationUnderTrafficR` (F4) |
| p11j | product: Retire never marks the replaced version dead | KILLED: E4S only (`no shard of the bucket left in r0`) (N2) |

Instrumentation (`mutations/instr-fuzz-stats.patch`, one stats line per fuzz execution;
`evidence/a01-*.summary`):
- Seed corpus: 18 executions. Every one acknowledged both warm-up objects. Traffic puts to `m`:
  19 acknowledged of 25. Only 2 inputs contain a migrate op; both complete, moving 4 pre-existing
  objects and 12 shards.
- 60 s of fresh-cache `-fuzz`, first run: 365 executions, 63 with a migrate op, 54 completed
  migrations, 354 shards moved. Second run: 339 executions, 89 with a migrate op, 81 completed,
  10 compensated, 501 shards moved (6.2 per completed migration).
- No execution completed a migration with 0 moved shards. The bucket never empties: it always
  holds the two warm-up objects, and the runner never deletes from `m`. What does leave the
  per-key check unexercised is the input format: about 80% of executions carry no migrate op
  (an op byte ≡ 11 mod 12 is needed), and some migrations only compensate.

### 2. Migration start with unknown outcome (`a02-ambiguous-start.sh`)

| Patch | Mutation | Result |
|---|---|---|
| p12e | runner: never look up a new saga after a timed-out start | KILLED: `TestNemesisAmbiguousMigrationStartIsTracked` (`ambiguousRecovered = 0, want 1`) |
| p12a | runner: forget the start only on `ErrUnreachable` | SURVIVED, equivalent mutant (N3) |
| p12d | runner: allow wiping the coordinator while the migration is unknown or running | suite FAILS (E7 at `DST_SEEDS=30`): guard needed (N6) |
| p12f | product: a started saga never finishes (Claim never completes) | KILLED: ambiguous test, corpus, E7 (not terminal) |
| p11g | product: completes but leaves source shards | see p11g above (F4) |
| p11f + p12g | product copy no-op, with the runner's per-key and outside checks removed | KILLED: still caught (migration never terminal) |

Earlier, Fable killed "found saga, then only refresh the region"; I did not repeat it.

Own lost-reply schedules (`tools/opus_sched_test.go.txt`; every reply from a node to a client
lost for cycle 0; fault on the coordinator; two cycles; seeds 702, 9001, 31337):

| Schedule | Result |
|---|---|
| coordinator paused until the heal | held: recovered, both cycles completed |
| crashed, restarted after 2 s | held |
| crashed, down until the heal (restarted by it) | held |
| torn crash, restarted after 1.5 s | held |
| isolated for 70 s (past the 60 s heal bound) | held: first migration compensated, second completed |
| its region cut for 70 s | held: compensated, then completed |
| paused for 70 s | BROKEN: object lost (F1), seed 31337 |

### 3. Partition-time reads and writes in E2 (`a03-e2-partition.sh`)

| Patch | Mutation (product, "split" = the node's live view lacks a region) | Tier S | Tier R |
|---|---|---|---|
| p13a | GET and PUT unavailable while split | KILLED (`post-split get e2/k00 ... must succeed`) | not run |
| p13b | GET only | KILLED (same) | not run |
| p13c | PUT only | KILLED (`post-split put ... must succeed (side size 7 >= k+m=6)`) | not run |
| p13d | minority side only | KILLED (minority put must succeed) | survives as specified: the 3-node minority must answer unavailable |
| p13e | majority side only | KILLED | KILLED (`post-split get ... http 503`) |
| p13f | GET unavailable for keys written before the cut | KILLED | not run |
| p13g | GET and PUT unavailable while split, `e2efault` build only (tier R only) | survives (not built on S) | KILLED |
| p13i | GET flips a byte while split | KILLED (`no-get-returns-wrong-bytes` invariant) | KILLED (`returned wrong bytes or etag`) |
| p13h | GET returns the pre-split version of a key once a PUT to it began while split | SURVIVED (N7) | not run |

Vacuity (unmutated, `DST_SEEDS=12` plus one tier R run). On tier S, every seed requires 9 puts,
12 post-split gets and 3 early gets to succeed. The 7-node minority has required writes but
never required reads, because it holds at most m=2 shards of a stripe. On tier R, 5 required
puts, 12 required gets and 3 early gets, all on the 6-node majority: the 3-node minority can
neither read nor write by layout, so no must-succeed assertion there can ever fire. No
assertion runs zero iterations.

A stale read is not wrong bytes under E2's claim (N7).

## Part 2

| Item | Check | Result |
|---|---|---|
| Discards counted | `p2a` `Health.Discards = 0` | KILLED by E2S and E8S (`Health.Discards=0 with 34 RecentDiscards`) |
| `settle` and terminal errors | `p2b` GET failures wrapped as `ErrInvalid` | KILLED by E7 (`disallowed error`) |
| Blocklist reload | `p2c` reload skips a rewrite with the same peer count | KILLED by `TestBlocklistReloadSeesEqualSizeRewrite` |
| Real-cluster constructor | `p2i` startup ignores an exited process | KILLED by `TestTierRStartupFailsOnPlantedLaunchFailure` (tier R) |
| `s.Verify()` | read: fuzz target and corpus check end with `Verify` | held (read, not run) |
| E4 sizes | read: two-stripe object on every tier-S seed | held (read) |
| Ledger oracle | `p2h` every seventh GET flips a byte | KILLED by E1S |
| Spread oracle | `p2d` placement ignores the per-region cap | KILLED by E1S (`3 shards in region r0, more than m=2`) |
| Orphan check | `p2e` GC never deletes | KILLED by E2S (`54 shards that no live manifest expects`) |
| Lineage / contests | `p2g` minority-side tag write acknowledged but not written | KILLED by E2S and E8S (`acknowledged write ... is not current`) |
| Deletion oracle | `p2j` GC also collects a third of the expected shards | KILLED by E2S (`deleted shard ... which ... can still make the winner`) |
| Loss oracle | `p2f` declare lost on one absence record | SURVIVED (F5) |
| Canaries | all 14 tier-S canaries and the tier R E9 canary | all pass with their planted reason (lines `a04-TestCanary*`, `a05-TestCanaryE9*`) |

"Only for its planted reason": each canary matches a specific substring (the helper refuses an
empty one), and the unmutated scenarios pass on 103 of my own seed runs (Part 3). So no canary
passes because its scenario fails for some other reason.

## Part 3

- Own seeds: `DST_SEED_BASE=880000 DST_SEEDS=40` on E1–E4 and E6–E8 tier S, 103 seed runs, all
  pass, including the E7 sweep floors. The E5 matrix was not swept with my seeds.
- Determinism: E7 seed 880123 run in two processes gives identical MEASURE lines. The same fuzz
  input run twice in one process gives the same step count, end time and stats.
- REPLAY: see F2.
- `planFromBytes` edges (22 inputs: empty, `0xff`, zero-length cycles, truncated op, 64 KiB of
  zeros, of `0xff` and random, 24 ops of each single kind, kind bytes above range, ops at time
  255): the plan shape stays within bounds, every input passes `Verify`. Slowest execution
  1.3 s (all wipes); most allocation 315 MiB per execution (all migrates); process `Sys` 31 MiB.
  The fuzz target skips inputs over 64 KiB, and an exactly 64 KiB input runs normally.
- Tier R hygiene: SIGKILL and SIGINT of the test binary leave no `objnode` process (N5 for temp
  dirs). The built-in hygiene test, the default-build test and the E9 canary pass on tier R.
  `strings` on the default `objnode` binary finds no `e2efault` or `blocklist`.

## Harness corrections made during the pass (mine, not the target's)

1. The a05 REPLAY loop ran only its first line, because `docker compose run` consumed the loop's
   stdin; `indocker` now reads from `/dev/null`.
2. `p13h` and `p13f` keep state in package-level maps. These leaked between
   `RequireDeterministic`'s two runs and between the sweep's parallel shards, so `p13h` was
   "killed" by the determinism check in both earlier runs. The maps now reset at node
   construction, those two checks run with `-parallel 1`, and `classify` treats "same seed gave
   different digests" as a setup error, never as a kill.
3. The tier R vacuity line read tier S measures; it now reads the `e2r/` measures.
4. The a04 canary loop counted the skipped tier R E9 canary as failed, and the E4 size check
   read too few lines. Both fixed. The E9 canary runs in a05 with `E2E_TIER_R=1`.

## What I ran versus only read

Ran: every line in `evidence/run-all.txt` (final), the two earlier full runs in `evidence/first-run/`,
and a standalone a03 run (`evidence/a03-rerun.txt`) that confirmed the `-parallel 1` fix before the final run; the
diagnostics in `evidence/adhoc/`; `tools/compilecheck.sh` over every patch.

Read only: the `s.Verify()` and E4-size items; the cause of F1 (inferred from the code and
confirmed only by its behaviour under the OrphanGrace control, not by a trace of the orphan
records); the simulator contract behind N3.

## Not tested, and why

- Tier R for E3, E6, E8 and E5 under mutation: time. Each tier R run costs 1–3 minutes, and I
  spent the tier R budget on E2, E4, the startup check and hygiene.
- A long soak and fill-disk: the earlier seat's harness covers them
  (`agent_work/adversarial/07-e2e/`); not repeated.
- Clock steps: the simulator offers only static skew and drift.
- The 15-minute hardening fuzz budget: not repeated; my fresh-cache runs were 60 s each.
- Terminal-error validation for tag and delete under a product mutation: only GET was mutated
  (`p2b`); the 39 self-tests cover the table.
- F1 on tier R: needs a SIGSTOP of a real coordinator for longer than its 1 h OrphanGrace; not
  attempted.

## Summary of the final run

`bash agent_work/adversarial/07-e2e-opus/run-all.sh`, from 06:40:50 to 07:08:32 UTC on
2026-11-23 (27.7 min, one Docker run at a time):

```
SUMMARY held=74 broken=9 setup_errors=0 wall=1662s
```

The nine BROKEN lines, mapped to the findings:

| Line | Finding |
|---|---|
| `a02-coordinator-paused-past-orphan-grace-loses-object` | F1 |
| `a02-sched-lost-reply-then-pause-past-heal-bound` | F1 (the same loss, reached through the lost-reply schedule) |
| `a05-replay-shard=1` (twice), `a05-replay-shard=2`, `a05-replay-shard=3` | F2 |
| `a01-purge-noop-S` | F4 (the same mutant is killed by `a01-purge-noop-R`) |
| `a04-oracle-loss-declared-early` | F5 |
| `a03-S-stale-read-split` | N7 (no claim broken) |

F3 has no line of its own: it is the oracle message seen in F1's lines. Every line, with its
detail, is in `evidence/run-all.txt` and rendered as a table in `evidence/run-all-table.md`.
Per-check logs are `evidence/<check>.log`. The worktree's tracked files are unchanged
(`git status` shows only this directory), and every scratch tree was removed.
