VERDICT: FAIL

Second-eyes review and independent probing of `review/06-integration-neon2eyes` at HEAD `5c5fc81` (fix range `1dc99da..HEAD`, pre-fix test commit `2d34871`). The core binding-absence mechanism closes the arbitration defect (orphan collection plus unwritten shard), passes the arbiter's deterministic reproduction, and handles concurrent overwrites, deletes, dual declarers, partitions, pauses, and intact restarts cleanly. However, independent code review and probing uncovered **four MAJOR findings** (two product bugs in `internal/node/inventory.go`, one converse-oracle bug on migrated objects, and one untested safeguard), plus two NITs.

## Findings

1. **MAJOR — Transient local disk `ErrIO` causes false absence records and declares healthy/recoverable objects lost.**
   - **Location:** `internal/node/inventory.go:357` (`stateAbsent`), compounded by `internal/node/inventory.go:166-172` (`scrubShard`) and `internal/node/inventory.go:469` (`rebuild`).
   - **What is wrong:** When `scrubShard` reads a local shard and gets a transient `env.ErrIO`, it deletes the shard from `inv.actual` (`:167-172`), immediately triggering `rebuild` on the same maintenance tick (`:222`). `rebuild` hardcodes `answered[es.idx] = true` (`:469`) without reading the local shard and delegates the local check to `stateAbsent`. `stateAbsent` checks:
     `if b, err := n.store.Store().GetBlob(id); err == nil && storage.BlobChecksum(b) == man.Stripes[stripe].Shards[idx].Checksum { return }`
     Any non-nil error — including transient `env.ErrIO` — falls through and writes a permanent, binding `~absent/` record for a shard that is still intact on disk. This falsifies section 14's guarantees ("H holds no shard of (v, s, i) whose checksum matches the manifest", "A timeout, an unreachable node, a not-ready node or any other error is not an answer, and then no record is written", and "A record's index held no verifiable shard when the record was written"). On `k=2, m=0` (or `k=1, m=0`), two transient shard read failures (`FailReads(holder, name, 2)`) immediately declare a completely healthy object lost and tombstone its manifest. On `k=2, m=1` with 1 rotted shard (within `m`, completely recoverable), sequential transient read errors (`FailReads(..., 3)`) on the two healthy holders write two false absence records (`> m`) and declare the recoverable object lost, failing the converse oracle.
   - **Reproduction:** `TestNeon2LocalTransientDiskIOErrorMustNotDeclareHealthyObjectLost` (`internal/node/neon2_loss_declaration_test.go`), both subtests (`k2m0_two_transient_read_errors_on_one_holder` and `k2m1_one_rot_plus_sequential_transient_read_errors_on_healthy_holders`). Both fail deterministically on HEAD with `n000 declared g/k version 14d266b89c5208a4 lost while every stripe still had at least k=2 verifiable shards on its holders' disks`.
   - **Smallest fix:** In `stateAbsent` (`internal/node/inventory.go:357`), return without writing an absence record unless `GetBlob(id)` returns `isNotFound(err)`, `isCorrupt(err)`, or `err == nil` with a mismatched checksum:
     ```go
     b, err := n.store.Store().GetBlob(id)
     if err == nil {
         if storage.BlobChecksum(b) == man.Stripes[stripe].Shards[idx].Checksum {
             return
         }
     } else if !isNotFound(err) && !isCorrupt(err) {
         return
     }
     ```
     Also avoid deleting `name` from `inv.actual` in `scrubShard` (`:167-172`) when `err` is neither `isNotFound(err)` nor `isCorrupt(err)`.

2. **MAJOR — `rebuild` completion (`finish`) does not re-check `statedAbsent`, storing a shard after the holder already holds its own binding absence record.**
   - **Location:** `internal/node/inventory.go:499-507` (`rebuild`).
   - **What is wrong:** `checkInventory` checks `!n.statedAbsent(...)` before starting `rebuild` (`:222`), and `rebuild`'s `finish()` re-checks `n.purged[man.Version]` (`:500`), but `finish()` never re-checks `n.statedAbsent(rd, id)` (or `n.occ.Reader()` error / `n.dead(rd, name)`) before calling `n.store.Store().Put(...)` (`:504`). If a wiped holder rejoins, receives the manifest before its own `~absent/` record (e.g., via OCC delta or per-key Merkle repair), starts `rebuild`, and then receives `~absent/` while peer `OpGetShard` RPCs are in flight, `finish()` still writes the rebuilt shard to disk even though `statedAbsent` is already true in the node's own metadata. This violates section 14's binding guarantee ("After writing it, H never rebuilds (v, s, i)") and is distinct from the stated residual (where a late shard arrives *before* metadata restores `~absent/`).
   - **Reproduction:** `TestNeon2WipedHolderRebuildRacesOwnAbsenceRecord` (`internal/node/neon2_restarts_test.go`). Fails deterministically on HEAD (`rebuild stored shard shards/g/cf/719d8df10dadef3f/0.0 on n001 even though n001 already held its own binding absence record ... before finish() ran (Rebuilds=1)`).
   - **Smallest fix:** In `rebuild`'s `finish()` (`internal/node/inventory.go:499-507`), fetch `rd, err := n.occ.Reader()` and abort (incrementing `RebuildFailures` and returning without writing) if `err != nil || n.statedAbsent(rd, man.ShardID(es.stripe, es.idx)) || n.dead(rd, name)` before calling `n.store.Store().Put(...)`.

3. **MAJOR — Converse loss oracle (`onCommitLoss`) falsely fails on any migrated object declared lost.**
   - **Location:** `internal/node/helpers_test.go:345-369` (`onCommitLoss`) and `internal/node/migrate.go:754` (`migrator.Retire`).
   - **What is wrong:** `onCommitLoss` looks up the manifest solely in `h.mans[ver]`, which is populated only by `CommitSink` (`n.committed`). `migrator.Retire` (`migrate.go:754`) swaps a staged migration copy into `occ.ManifestKey` via `g.n.occ.Put` without calling `g.n.committed`. Thus `h.mans` never contains migrated object versions. When a migrated object legitimately loses `> m` shards and is declared lost, `onCommitLoss` unconditionally fails the simulation with `"declared version ... lost, a version no node ever committed"` instead of verifying the holders' disks.
   - **Reproduction:** `TestNeon2LossDeclarationAfterBucketMigration` (`internal/node/neon2_loss_declaration_test.go`). Fails deterministically on HEAD (`n000 declared version dfd3759082e06c7e of m/k lost, a version no node ever committed`).
   - **Smallest fix:** In `helpers_test.go:onCommitLoss` (`:361`), when `man == nil`, read `occ.ManifestKey(parts[0], parts[1])` from `h.node(id).OCC().Reader()` (the manifest is still live at the instant `~lost/<v>` commits because Safeguard 3 writes `~lost/<v>` before tombstoning the manifest), decode it, and verify `man.Version.String() == ver`.

4. **MAJOR — Safeguard 2 (`serveStorage` refusing `OpPutShard` before metadata loads) has no test in the codebase.**
   - **Location:** `internal/node/inventory.go:555-559` (`serveStorage`), `internal/node/absent_internal_test.go`, `internal/node/lost_test.go`.
   - **What is wrong:** Commit `47feeb4c` added two safeguards (`noteAbsent` `SplitN` and `serveStorage` refusing shard writes when `n.occ.Reader()` returns an error), and section 14 documents both, but no existing test exercises `serveStorage` while OCC is not yet loaded (`occ.ErrNotReady`). Removing lines 555–559 breaks zero existing tests.
   - **Reproduction:** Code inspection of `absent_internal_test.go` and `lost_test.go` (no test injects an unloaded OCC store during `OpPutShard`). Our independent test `TestNeon2ServeStorageRefusesShardWriteBeforeMetadataLoads` (`internal/node/neon2_restarts_test.go`) exercises this exact path and passes on HEAD.
   - **Smallest fix:** Add a regression test (equivalent to `TestNeon2ServeStorageRefusesShardWriteBeforeMetadataLoads`) that fails reads on `occ.StateBlob` across restart, sends `OpPutShard`, and asserts `occ.ErrNotReady` with no shard written to disk, then verifies `ErrFenced` once `occ` loads.

5. **NIT — `expectedAll` does not verify `parts[1] == ""` when parsing `~absent/` tags.**
   - **Location:** `internal/node/inventory.go:80-84` (`expectedAll`).
   - **What is wrong:** `expectedAll` calls `n.noteAbsent(parts[2])` for any live `KindTag` whose `parts[2]` has prefix `~absent/`, without checking `parts[1] == ""` (bucket-scoped tag) or scoping `n.absent` by bucket (`parts[0]`). User tags cannot begin with `~` (`objects.go:435`) and `VersionID` is 64-bit random, so this is not exploitable today, but checking `parts[1] == ""` matches `deadVersions` (`inventory.go:623`) and `migrator.registered` (`migrate.go:117`).
   - **Smallest fix:** Guard `inventory.go:80` with `parts[1] == "" && strings.HasPrefix(parts[2], tagAbsent)`.

6. **NIT — Shallow coverage on two existing safeguard/refusal tests.**
   - **Location:** `internal/node/lost_test.go:496-543` (`TestAHolderThatRecordedItHoldsNoShardRefusesItLater`) and `internal/node/lost_test.go:545-575` (`TestTheLossRecordIsCommittedBeforeTheManifestIsTombstoned`).
   - **What is wrong:** The refusal test does not verify that `ErrFenced` survives an intact-disk `Crash`/`Restart`, and the write-order test asserts commit order without actually crashing between `~lost/` and the tombstone to confirm recovery on restart/successor takeover. Both behaviours hold on HEAD (verified by `TestNeon2HolderIntactRestartPreservesAbsenceRefusal` and `TestNeon2DeclarerCrashBetweenLossRecordAndTombstone`).
   - **Smallest fix:** Extend both tests to cover intact-disk restart and mid-declaration crash recovery.

---

## Part A — Code review against stated guarantees

1. **Absence record write conditions (`inventory.go:160-178, 348-368, 453-543`):** Read `scrubShard`, `rebuild`, `stateAbsent`, `liveVersion`, and `storage/chunkstore.go`, `storage/stripe.go`. For *remote* holders, `heard` (`:470-474`) correctly treats only `err == nil || isNotFound(err) || isCorrupt(err)` as an answer; timeouts, `ErrUnreachable`, `ErrNotReady`, `ErrStopped`, and remote `ErrIO` leave `answered[j] == false` and suppress `stateAbsent`. `stateAbsent` checks `purged`, `dead`, `liveVersion` (excluding staged copies), and `statedAbsent`, and counts verifiable remote blobs against `man.Layout.K`. **However, the local-shard re-check is broken (Finding 1):** `stateAbsent:357` treats any non-nil error from `GetBlob(id)` — including transient local `env.ErrIO` — as absence, while `scrubShard:167` deletes from `inv.actual` on `env.ErrIO` and `rebuild:469` assumes `answered[es.idx] = true`. Thus an absence record *can* be written on weaker evidence (transient local `ErrIO`) while a good shard exists on disk.

2. **Binding nature across all shard-write paths (`inventory.go:222, 499-513, 545-579`, `migrate.go:221-342`, `storage/node.go:266-295`, `occ/node.go:192-215`):** Read every path that writes shards to `ChunkStore.Put`. Inbound wire `OpPutShard` (including local `Put` and migration `Copy` because `storage.Node.Put` always routes through `e.Net.Request` -> `OnRequest` -> `serveStorage`) checks `n.occ.Reader()` and `n.statedAbsent(rd, id)` before storing, refusing with `ErrFenced` and counting `AbsentRefused`. Single-threaded execution prevents a wire write from racing `stateAbsent`'s commit: if `OpPutShard` runs first, `stateAbsent` re-checks disk; if `stateAbsent` runs first, `occ.SetScalar` commits before `serveStorage` runs. Intact-disk restart reloads `occ/state` synchronously in `occ.Start()` (or stays not-ready on `ErrIO`, refusing `OpPutShard`). **However, `rebuild`'s completion path is NOT binding (Finding 2):** `rebuild` `finish()` (`:499-507`) does not re-check `statedAbsent`, so a wiped holder that receives the manifest before `~absent/` and starts `rebuild` will store the rebuilt shard even after `~absent/` arrives in metadata before `finish()` executes.

3. **Declaration rules and edge cases (`inventory.go:75-103, 305-430`, `migrate.go:733-811`, `node.go:581-641`):** Read `expectedAll`, `noteAbsent`, `unrecoverableStripe`, `designated`, `declareLost`, `handleDiscard`, and `migrator.Retire`. `unrecoverableStripe` counts `byShard[[2]int{s, i}][ref.Node]` per stripe against `c > man.Layout.M`, so only the manifest's actual placement holder for `(s, i)` counts and counts never sum across stripes. Works for `m=0` (`k=1, m=0` and `k=2, m=0`), `k=1, m=1`, multi-stripe objects, short last stripes, and 0-byte objects (empty `Stripes`). Two partitioned nodes that each consider themselves `designated()` can both declare only on `> m` existing records; after healing, LWW reconciles tombstones/loss records and `handleDiscard` triggers a quota recount so usage converges cleanly. Metadata arriving in any order works because `expectedAll` rebuilds `n.absent` and `n.liveRefs` from `rd.Keys()` each tick. Staged migration copies are excluded from `n.liveRefs` and rejected by `liveVersion` in `stateAbsent`. Concurrent overwrites/deletes compete with `declareLost`'s tombstone under LWW: a losing newer write is counted and logged as a discard; a winning newer write stays live and readable while only `v1`'s `~lost/<v1>` remains.

4. **The three safeguards (`inventory.go:309-337, 407-424, 553-565`, `absent_internal_test.go`, `lost_test.go:545-575`):**
   - *Safeguard 1 (`noteAbsent` `SplitN(..., "/", 3)`):* Correct; handles any non-empty holder ID containing `/` (verified both by unit test and end-to-end in `TestNeon2SlashHolderIDsEndToEndLossDeclaration`). `TestAbsenceRecordKeyRoundTripsForAnyHolderName` and `TestMalformedAbsenceRecordNamesAreIgnored` genuinely fail if `SplitN` reverts to `Split`.
   - *Safeguard 2 (`serveStorage` refusing `OpPutShard` before metadata loads):* Implementation is correct (`n.occ.Reader()` returns `occ.ErrNotReady` until `occ/state` loads), **but has no test in the repository (Finding 4).**
   - *Safeguard 3 (`~lost/<v>` written before manifest tombstone):* Implementation is correct (`SetScalar(lk)` commits before `occ.Delete(r.key, IfMatch(r.etag))`, and on retry `if !lheld.Live()` skips re-writing `lk`). `TestTheLossRecordIsCommittedBeforeTheManifestIsTombstoned` genuinely fails on pre-fix commit `ccf6f4db` (order `[tombstone lost]`), though it only checks order rather than a real crash between writes (Finding 6).

5. **Tests and converse oracle (`lost_test.go`, `helpers_test.go:311-393`, `oracle_test.go`, `dst_putget_test.go:143-287`, `gc_test.go`):**
   - Each named test in section 14's table proves what it claims: the 5 `...DeclaredLost` functions (6 cases) all fail on `2d34871` and pass on HEAD; the readable controls assert live metadata, exact GET bytes on every node, zero declarations, and full shard rebuild; `TestAHolderThatCannotBeHeardFromBlocksALossDeclaration` covers both `partitioned` and `paused` for 2 simulated minutes; `TestClusterPutGetDST`'s `orphanedAfterAck` exercises the arbitration class (`late_orphans_plus_unwritten` hit on 50-seed sweep) and enforces both readability of recoverable runs and `checkLiveReadableOrLost`.
   - `onCommitLoss` (the converse oracle) is installed in `newHarness` (`helpers_test.go:81`), covering every full-node DST and harness test (note `prop_test.go` constructs `node.NewFactory` directly without `newHarness`). It genuinely catches false declarations (it caught our Finding 1 and Residual Case (b) tests immediately). **However, `onCommitLoss` is broken for migrated objects (Finding 3)** because `migrator.Retire` never invokes `n.committed`, leaving `h.mans[ver] == nil`.

6. **"Residual limit" accuracy and acceptability (`build-notes.md:1136-1171`, `noderuntime/runtime.go:563-640`):** Read the full paragraph against `inventory.go` and `noderuntime/runtime.go`. The unreachable/permanently-removed holder limit and the wiped-disk (`WipeRejoin`) + late-shard cases (a) and (b) — including the narrowed real-runtime analysis of `Request`/`roundTrip` dialing without destination incarnation — match the code accurately (and we reproduced case (b) in `TestNeon2ResidualCaseBWipedHolderBeforeDeclaration`). However, the text omits the non-wiped failures in Findings 1 and 2, which must be fixed rather than documented away. Once Findings 1 and 2 are closed, the wiped-holder + late-shard residual (cases (a) and (b)) is **acceptable as a stated residual** (not a MAJOR): case (a) cleans up after `GCGrace` with the key staying lost on record, and case (b) requires a simultaneous disk wipe on an already-absent holder of an under-`k` stripe plus an in-flight pre-wipe `OpPutShard` arriving before metadata sync returns `~absent/`.

7. **Regressions, determinism, growth, and frozen packages:** Verified `git diff 1dc99da..HEAD -- internal/env internal/sim` is empty (no edits to frozen packages). No existing test was removed or weakened. `detlint` passed on `internal/node` (9 files, 0 findings); code audit of `inventory.go` confirms no goroutines, locks, wall-clock calls, or map-iteration-order dependence. `~absent/` (and still-written `~orphan/`) records grow monotonically with permanently lost stripes / orphan collections, which is bounded to failure events and explicitly documented in section 14's "Cost" paragraph.

---

## Part B — Independent probing and fixed checks

### Fixed check 1: Arbiter reproduction (`internal/node/neon2_arbiter_repro_test.go`)
- **`TestArbitrationOrphansPlusAnUnwrittenShard` (unchanged from `ARBITRATION-1.md`):** On HEAD, reproduces the exact version `c7bd04bd6c737491` and orphan deletions `n000|0.3`, `n001|0.0`, and **fails at line 87 (`manifest unexpectedly not live`)** because the fix now declares the version lost and tombstones the manifest.
- **`TestNeon2ArbitrationOrphansPlusAnUnwrittenShardFixed`:** Same setup asserting the fixed end state (manifest not live, `~lost/c7bd04bd6c737491` live on every node, GET returns not found, `h.s.Err() == nil`) — **PASS**.

### Fixed check 2: Pre-fix failure confirmation on `2d34871`
Exported `2d34871` into `/tmp/neon2-prefix` and ran `go test -count=1 ./internal/node -run 'DeclaredLost' -v` inside `objstore-dev:local`: all 5 `...DeclaredLost` test functions (6 cases, including `k2m2` and `k4m2`) **FAILED** with `still live in metadata although it cannot be rebuilt`, while `TestOneRottedStripeWithinMIsRepairedNotDeclaredLost` passed.

### Independent scenario table (`internal/node/neon2_*_test.go`)

| Test | Area | Setup | Expected | Result | Seeds / Layouts |
|---|---|---|---|---|---|
| `TestNeon2ArbitrationOrphansPlusAnUnwrittenShardFixed` | Fixed check / orphan + loss | Arbiter's exact 4-node schedule (`n002` unwritten, `n000`/`n001` orphaned, `w` isolated on ack, heal + 120s) | Manifest not live, `~lost/<v>` on every node, GET not found, no oracle error | **held** | seed 1; k=2 m=2 |
| `TestNeon2OrphanManifestArrivesJustBeforeGrace` | Orphan collection | All 3 remote shards land; writer isolated until `OrphanGrace - 6*MaintainPeriod`, then healed | Zero orphan collections, zero `~absent/` or `~lost/`, object live & readable on all nodes | **held** | seeds 1–3; k=2 m=2 |
| `TestNeon2OrphanStaggeredCollectionAcrossTicks` | Orphan collection | `Clog` delays one holder by `2*MaintainPeriod` so orphan collection happens across different ticks; recoverable (`m` collected, `k` kept) vs unrecoverable (`m` collected + 1 unwritten) | Recoverable stays live & fully rebuilt; unrecoverable declared lost & remaining shards collected | **held** | seed 1; k=2 m=2 (q=3) & k=4 m=2 (q=5), 2 stripes |
| `TestNeon2EdgeLayoutsAndShortAndZeroObjects` | Loss declaration / layouts | Single rot on `k=1,m=0` (1 B) and `k=2,m=0` (1025 B, 1 B last stripe); 1 rot then 2 rots on `k=1,m=1` (1025 B); 0 B object on `k=1,m=0` | `m=0` declares lost on 1 rot; `k=1,m=1` rebuilds 1 rot and declares lost on 2; 0 B never declared lost | **held** | seed 1; k=1 m=0, k=2 m=0, k=1 m=1 |
| `TestNeon2LocalTransientDiskIOErrorMustNotDeclareHealthyObjectLost` | Loss declaration / fault | (a) `k=2,m=0` healthy object + `FailReads(h0, shard, 2)`; (b) `k=2,m=1` 1 rotted shard + sequential `FailReads(h0, shard, 3)` then `FailReads(h1, shard, 3)` | No absence record, no loss declaration, object remains readable | **broken (product — Finding 1)** | seed 1; k=2 m=0 & k=2 m=1 |
| `TestNeon2LossDeclarationRacesConcurrentOverwriteAndDelete` | Loss declaration / races | Partition designated node `n000` (with `> m` absence records) from side doing concurrent PUT `v2` (4 seeds) or DELETE; heal and converge | Winning `v2` stays live & readable; losing `v2` is counted & logged as LWW discard; quota matches recount; concurrent DELETE leaves 0 shards & 0 usage | **held** | seeds 1–4 (overwrite), seed 1 (delete); k=2 m=2 |
| `TestNeon2DualDeclarersInPartitionedCluster` | Loss declaration / membership | 6 nodes split into `{n000..n002}` and `{n003..n005}` after `> m` absence records form; both `n000` and `n003` declare lost concurrently; heal | Both declarers commit (`n000=1`, `n003=1`), heal converges to tombstoned + `~lost/<v>`, quota matches recount (no underflow), all shards collected | **held** | seed 1; k=2 m=2 |
| `TestNeon2NonHolderAndCrossStripeAbsenceRecordsIgnored` | Loss declaration / validation | Inject 4 non-holder `~absent/` records on stripe 0 plus `m=2` genuine records on stripe 0 and `m=2` on stripe 1 (total 4 > m across stripes) | Never declared lost; stays live & readable | **held** | seed 1; k=2 m=2, 2 stripes |
| `TestNeon2LossDeclarationAfterBucketMigration` | Loss declaration / migration | Create routed bucket in `r0`, put object, migrate `r0 -> r1` to completion, then rot `m+1=2` shards of the migrated version | Migrated version declared lost cleanly without oracle error | **broken (oracle/product — Finding 3)** | seed 1; 9 nodes, k=2 m=1 |
| `TestNeon2StagedMigrationCopyNeverWritesAbsenceWhileStaged` | Loss declaration / migration | Hold migration saga at `Flip` after `Copy` stages object in `r1`; rot `m+1=2` shards of staged copy | Zero `~absent/` records written and zero declarations while copy is only staged | **held** | seed 1; 9 nodes, k=2 m=1 |
| `TestNeon2SlashHolderIDsEndToEndLossDeclaration` | Loss declaration / safeguard 1 | 4-node cluster with node IDs `dc1/r0/n0`..`dc1/r1/n3`; rot 3 shards | End-to-end loss declaration succeeds on every node | **held** | seed 1; k=2 m=2 |
| `TestNeon2HolderIntactRestartPreservesAbsenceRefusal` | Holder restarts | Holder writes `~absent/`, then `Crash` + `Restart` (intact disk); immediately send valid `OpPutShard` | Refused with `ErrFenced`, `AbsentRefused > 0`, shard not written to disk | **held** | seed 1; k=2 m=2 |
| `TestNeon2DeclarerCrashBetweenLossRecordAndTombstone` | Holder restarts | Crash designated `n000` inside `CommitSink` immediately after `~lost/<v>` commits (verified manifest still live on disk); subtests: declarer restarts vs stays down for successor | Tombstone completes on next tick, `~lost/<v>` present, GET 404, quota == 0 | **held** | seed 1; k=2 m=2 |
| `TestNeon2ServeStorageRefusesShardWriteBeforeMetadataLoads` | Holder restarts / safeguard 2 | Holder writes `~absent/`, crashes, restarts with `FailReads(a, occ.StateBlob, 1)` so `occ.Ready() == false`; send `OpPutShard`, then retry after `occ` loads | First `OpPutShard` returns `occ.ErrNotReady` with nothing written; after load returns `ErrFenced` | **held** | seed 1; k=2 m=2 |
| `TestNeon2WipedHolderRebuildRacesOwnAbsenceRecord` | Holder restarts / binding | Holder has `~absent/` on peers; `WipeRejoin`; deliver manifest first while peer replies are clogged so `rebuild` starts; deliver `~absent/` before unclogging replies | `rebuild` `finish()` must abort and not write shard once `statedAbsent` is true | **broken (product — Finding 2)** | seed 1; k=2 m=2 |
| `TestNeon2HolderPauseResumeBlocksThenAllowsDeclaration` | Holder restarts / pause | Pause the 1 surviving holder while 3 shards are rotted for 30s, then `Resume` | Zero `~absent/` and zero declarations while paused; declared lost after resume | **held** | seed 1; k=2 m=2 |
| `TestNeon2LateShardAfterOrphanCollectionBeforeAbsenceSavesObject` | Late-arriving shards | `Clog` holds `OpPutShard` to `h2`; `h0`/`h1` collect as orphans; heal delivers late shard to `h2` before `~absent/` | Object stays live, readable on every node, all 4 shards rebuilt, zero declarations | **held** | seed 1; k=2 m=2 |
| `TestNeon2LateShardDuringRebuildPreventsAbsenceRecord` | Late-arriving shards | 3 shards rotted (`h0`/`h1`/`h2`); clog replies to `h2` so `h2` starts `rebuild`; deliver valid `OpPutShard` to `h2` while `rebuild` is in flight; unclog | `stateAbsent` re-checks disk, skips `~absent/`, object stays live & fully rebuilt | **held** | seed 1; k=2 m=2 |
| `TestNeon2LateShardAfterAbsenceBeforeDeclarationRefused` | Late-arriving shards | Non-holder `n000` paused briefly; 3 shards rotted; `target` writes `~absent/`; send valid `OpPutShard` before declaration; resume `n000` | Refused with `ErrFenced` (`AbsentRefused > 0`); declaration completes after resume | **held** | seed 1; 5 nodes, k=2 m=2 |
| `TestNeon2ResidualCaseBWipedHolderBeforeDeclaration` | Late-arriving shards / residual | 10 nodes; `w -> target` clogged; `n000` paused; 2 other holders rotted; 3 absence records form; `WipeRejoin(target)` + unclog delivers shard before metadata sync; resume `n000` | Confirms documented Residual Limit case (b): `n000` declares lost while `k=2` verifiable shards exist on disk (caught by `onCommitLoss`) | **held (confirms documented residual)** | seed 1; 10 nodes, k=2 m=2 |

---

## Verification: run vs read

### Verified by reading
- `agent_work/06-integration/ARBITRATION-1.md`, `agent_work/06-integration/build-notes.md` (full, including section 14).
- `git log --stat 1dc99da..HEAD` and `git diff 1dc99da..HEAD -- internal`.
- `internal/node/inventory.go`, `node.go`, `objects.go`, `records.go`, `meta.go`, `migrate.go`, `lost_test.go`, `absent_internal_test.go`, `helpers_test.go`, `oracle_test.go`, `dst_putget_test.go`, `gc_test.go`, `flip_test.go`.
- `internal/storage/node.go`, `stripe.go`, `chunkstore.go`, `shard.go`, `placement.go`.
- `internal/occ/meta.go`, `node.go`.
- `internal/noderuntime/runtime.go` (`Request`, `roundTrip`).
- `internal/sim/disk.go`, `process.go`.

### Verified by running (exact commands)
1. Existing loss/absence tests on HEAD:
   `docker compose run --rm -T -e GOMAXPROCS=2 dev go test -count=1 ./internal/node -run 'TestAnOrphanedWrite|TestAMultiStripe|TestAHolderThat|TestMoreThanM|TestOneRotted|TestTheLossRecord|TestAStaleShard|TestAbsenceRecord|TestMalformedAbsence' -v` — PASS (0.859s).
2. Pre-fix failure check on `2d34871`:
   `rm -rf /tmp/neon2-prefix && mkdir -p /tmp/neon2-prefix && git archive 2d34871 | tar -x -C /tmp/neon2-prefix && docker run --rm -e GOMAXPROCS=2 -v /tmp/neon2-prefix:/src -w /src objstore-dev:local go test -count=1 ./internal/node -run 'DeclaredLost' -v` — 5 `...DeclaredLost` tests (6 cases) FAILED as expected; `TestOneRottedStripeWithinMIsRepairedNotDeclaredLost` PASSED.
3. Arbiter reproduction (unchanged + fixed variant):
   `docker compose run --rm -T -e GOMAXPROCS=2 dev go test -count=1 ./internal/node -run 'TestArbitrationOrphansPlusAnUnwrittenShard|TestNeon2ArbitrationOrphansPlusAnUnwrittenShardFixed' -v` — unchanged FAILS on HEAD at `manifest unexpectedly not live`; fixed variant PASSES.
4. All independent `TestNeon2` probing scenarios:
   `docker compose run --rm -T -e GOMAXPROCS=2 dev go test -count=1 ./internal/node -run 'TestNeon2' -v` — 17 PASS, 3 FAIL (Findings 1, 2, 3).
5. `go vet` + `-race` on all passing `lost_test.go`, `absent_internal_test.go`, and `Neon2` tests:
   `docker compose run --rm -T -e GOMAXPROCS=2 dev go vet ./internal/node/... && docker compose run --rm -T -e GOMAXPROCS=2 dev go test -race -count=1 ./internal/node -run 'TestAnOrphanedWrite|TestAMultiStripe|TestAHolderThat|TestMoreThanM|TestOneRotted|TestTheLossRecord|TestAStaleShard|TestAbsenceRecord|TestMalformedAbsence|TestNeon2(Arbitration|LateShard|ResidualCaseB|LossDeclarationRaces|DualDeclarers|NonHolder|StagedMigration|SlashHolder|Orphan|EdgeLayouts|HolderIntact|DeclarerCrash|ServeStorage|HolderPause)' -v` — PASS (9.555s).
6. Extended put/get DST (50 seeds) + GC tests:
   `docker compose run --rm -T -e GOMAXPROCS=2 -e DST_SEEDS=50 dev go test -count=1 ./internal/node -run 'TestClusterPutGetDST$|TestCorruptShard|TestDeathEvidence|TestAWriteWhoseShards|TestOrphanRecords|TestANodeRefuses|TestLiveReadableOrLost' -v` — PASS (113.027s; `late_lost` and `late_orphans_plus_unwritten` both exercised).
7. Property tests (2,000 rapid checks):
   `docker compose run --rm -T -e GOMAXPROCS=2 dev go test -count=1 ./internal/node -run 'Prop' -rapid.checks=2000 -v` — PASS (99.877s).
8. Determinism lint on `internal/node`:
   `docker compose run --rm -T -e GOMAXPROCS=2 -w /src/agent_work/gates/detlint dev go run . -root /src internal/node` — PASS (`checked 9 files in 1 packages`).
9. Frozen packages check:
   `git diff 1dc99da..HEAD -- internal/env internal/sim` — empty.
