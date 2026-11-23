# Chunk 7 — end-to-end suite, nemesis cycles and hardening: findings, limits, pitfalls

What was built file by file, the claim-to-test table of the nine scenarios and every
measurement are in `agent_work/07-e2e/build-notes.md` (sections 1–10 are the first build; 11
and 12 the finish after the first review; 13 the fix round; 14 the short mode; 15 the fixes
for the adversarial pass; a later section overrides an earlier one where they differ). The
first review is `REVIEW-1.md` beside it and the second review, in three rounds, is
`FABLE-REVIEW.md`. The adversarial pass is `ATTACKS.md` and `RESULTS.md` in
`agent_work/adversarial/07-e2e-opus/`. This entry holds what the next builder must know and
would not guess.

## How the suite is put together

- Everything is in `test/e2e`, package `e2e`, in `_test.go` files (`e2e.go` only makes the
  directory a package). Scenarios E1–E4, E6 and E8 are written once against the `Cluster`
  interface (`cluster_test.go`) and run on both tiers. E5 and E7 have their own drivers; E9
  runs on real processes only.
- **Tier S** is the simulator: 21 nodes in regions r0–r2, k=4 m=2, 64 KiB stripes, plus one
  client process per region. Clients send object-API calls over the simulated network
  (`api_test.go`), so client traffic sees latency, partitions, lost replies and timeouts. No
  code is mocked. Tier S reads every node's disk and metadata, so all oracles run there, and
  every run goes through `simtest.RequireDeterministic`. It cannot show real sockets, real
  fsync, process signals or goroutines.
- **Tier R** is 9 `objnode` processes, 3 per region, built once per test binary with
  `-race -tags e2efault`, driven over HTTP. Partitions are blocklist files read by a hook that
  exists only under the `e2efault` tag; `TestDefaultBuildCarriesNoFaultInjection` holds the
  default binary free of it. Tier R sees a node only through HTTP and its shard files: it
  cannot read manifests, does not count bytes moved by repair, checks the routing pointer
  only through where probe writes land, and checks catch-up after a crash only against the
  shards the node held before. Routed-bucket tests (E4, E5) use k=2 m=1 there, because a
  routed bucket needs k+m nodes inside its region.
- Oracles (tier S, `simoracle_test.go`; each has a planted-fault test in
  `oracle_self_test.go`):
  - `Ledger`, the invariant `e2e/no-get-returns-wrong-bytes`, after every event: a GET's
    bytes match the version its ETag names, or some write attempt on that key.
  - `nodetest.Lineage`, fed by `CommitSink` and `DiscardSink`, at every heal and at the end:
    each acknowledged write is current, a migration copy, superseded by a later write that
    held it, or named as a loser in a discard.
  - Deletion safety (`watchDisk`): no shard delete of a version that can still become the
    winner on any node, and no commit of a version whose shards were already deleted.
  - Loss declarations: a `~lost/<v>` commit fails the run unless some stripe has fewer than
    k verifiable shards.
  - Spread: no stripe places more than m shards in one region when the writer's view had
    room.
  - After a full heal: `Converged` (every up node ready, membership equal to the up set,
    identical metadata bytes and Merkle roots), `checkLiveReadableOrLost` (every live key
    reads back or is declared lost on record) and `checkOrphans` (no shard without a live
    manifest after `OrphanGrace + GCGrace + 10 s`).
  - On both tiers, `checkContests` reads `/_health` `RecentDiscards`: every acknowledged
    contested write is current or logged as a loser, and the discard counter must equal the
    logged tail up to its 64 entries.
- **Nemesis runner** (`nemesis_test.go`, used by `TestE7NemesisHealingCyclesS` and the fuzz
  target). E7 runs 4 cycles of 4 s chaos per seed with static clock skew, drift and 0.5 %
  datagram loss. A cycle draws 2–5 operations: crash, pause, wipe, restart, region cut,
  one-way cut, isolate, lossy link, clog, shard bit-rot, shard read errors, a bucket
  migration. Traffic runs every 250 ms. Each cycle ends with `FaultHealAll`, then: every
  pending call ended with success or an error allowed for its operation, `Converged` within
  60 s, a running migration terminal with no shard of its bucket outside its region, every
  shard back on its holder, the lineage check, zero declared losses, every live key
  readable.
- **Fault budget.** A node or disk fault is applied only if every stripe of every live
  manifest keeps k verifiable shards on up, unpaused, untouched holders, and at most three
  nodes are down or paused. A node is not wiped while it holds a metadata version no other up
  node holds (`metadataElsewhere`). The coordinator of a running migration is never wiped:
  a lost coordinator is a stated limit, and removing that guard makes E7 fail. No separate
  guard for "a saga still running" exists; `finishMigration` follows every started or
  recovered saga to a terminal state before it clears the migration.
- **Availability check.** In a cycle, a read of a key settled before the cycle, through an
  untouched node, must not fail except with not found. It is voided only if it ended at or
  after the cycle's first network fault or the touching of its node.
- **Whole-cluster fuzz target.** `FuzzClusterNemesis` turns the input bytes into a plan
  (`planFromBytes`) and runs the same runner on 12 nodes, 4 per region, at k=2 m=1. Before
  the first cycle it writes two 4 KiB objects into the routed bucket `m` with no faults, and
  any failure there fails the input. Every completed migration must leave each object that
  was live at its start with at least k verified shards in the destination. Inputs over
  64 KiB are skipped. `TestFuzzClusterNemesisCorpusMovesShards` runs the 18-input seed
  corpus and requires acknowledged traffic writes to `m`.
- **Canaries.** One kept test per scenario (E1–E8 in `canary_test.go`, E9 on tier R in
  `tierr_test.go`), plus one per hole repaired later; 15 on tier S. A canary runs the real
  scenario once with a switch that plants a fault. The switches are in test code
  (environment, configuration and saga step wrappers), never in the product. The rule: a
  canary passes only when the scenario fails with its planted reason. `checkCanaryFailure`
  refuses an empty or blank match string, and `TestExpectCanaryFailureRejectsEmptyMatch`
  holds that.

## Product defects found

- **A read short of k shards answered not found.** A GET of a live object that could not
  gather k shards joins `storage.ErrUnavailable` with the per-shard causes. The first-match
  tables listed the causes first, so the wire carried code 96 (not found) or 97 (corrupt)
  instead of 99, and HTTP answered 404 instead of 503. Found by the stricter `settle` on E7
  seed 1. Fixed in `f70e2c2`: `ErrUnavailable` precedes the shard causes in
  `storage.CodeEntries` and `httpapi.StatusTable`; code numbers are unchanged. Held by
  pinned E7 seed 1, `TestJoinedUnavailablePrecedesEveryShardCause` (through
  `node.CodeTable()`), `TestCodeTableJoinedUnavailablePrecedesShardCauses` and
  `TestStatusTableJoinedUnavailablePrecedesShardCauses`: eight causes, both join orders.
- **A stalled migration coordinator lost acknowledged objects.** Found by the adversarial
  pass. A destination node expected a shard only when a live manifest or a staged record
  named it, and the staged record was written only after the copy's shards had landed. A
  coordinator frozen in between for longer than `OrphanGrace` came back to shards the
  destination had collected as orphans. The verify step compared roots built from manifests
  and did not look at shards, so the saga completed, `Retire` swapped the manifests onto the
  collected version, and the object was declared lost. With a 70 s pause against a 60 s
  grace, 6 of 12 seeds lost an object. Fixed in `eaa0556`.
  - The rule: a migration never routes a bucket to, or swaps a manifest onto, a copied
    version unless the destination holders have confirmed, and from then on keep, at least
    the write quorum of every stripe's shards — however long any node, the coordinator
    included, was stalled.
  - Mechanism: the node-channel request `opVerifyShards` (op 4) carries the staged record,
    or the live manifest record, with the shard list. The holder merges the record durably
    first, so its inventory expects the shard from then on and will not collect it as an
    orphan; then it checks the blob against the checksum and answers per shard. Check and
    protection are one act, so no gap is left between checking and acting.
  - It runs at three points: after each object's copy, as a plain presence check before the
    staged record is written; in the verify step and the post-flip catch-up, through `Root`
    of the destination; and in `Retire` before the manifest swap.
  - Threshold per stripe: `AckQuorum`, k+1 by default. Below it the step fails and is
    retried. When verified plus unreachable holders are fewer than k, the staged version is
    marked dead and its staged record deleted. Before the flip the saga then compensates;
    after the flip the catch-up copies the object again from the still-live source.
  - `Retire` deletes the staged records only after every live peer holds the swapped
    manifests. They used to be deleted first, which left the same window open at the end.
  - `Retire` now reports each swap to `CommitSink`. Before, the oracles never saw a migrated
    version.
  - No new wire error code. Held by `internal/node/dst_migstall_test.go` (36 subtests),
    `TestE4MigrationCoordinatorStalledPastOrphanGraceS` (stall of `OrphanGrace` + 10 s on the
    fuzz layout), the canary `TestCanaryMigrationUnverifiedStallLosesObject` and
    `TestOracleSeesMigratedManifestSwap`.

## Stated limits

- Metadata and manifests are acknowledged after one fsync on the writer's disk. If that disk
  is lost before anti-entropy copies the write, the write is gone with no discard record.
  This is the limit Chunk 4 already states; E7's budget keeps inside it, and
  `TestCanaryE7WipingTheOnlyCopyOfAnAcknowledgedWriteIsCaught` shows the lineage oracle
  catching the case with the rule off.
- A coordinator that never returns leaves its saga unfinished. The migration rule above
  covers a coordinator that returns, however late.
- A bucket's migration sends whole-bucket messages (shard verification, purge, pointer
  agreement), each bounded by the 16 MiB frame.
- Tier R allows 1 in-flight PUT per cluster outside E9, which keeps 4. With four, nine
  race-built nodes on 4 cores exceed the fixed 1 s shard request timeout and answer 503. Tier
  R therefore does not exercise concurrent writes outside E9. Lifting this needs an `objnode`
  flag for the shard request timeout.
- Clock skew and drift are static per node. The simulator has no fault that steps a clock
  during a run.
- E2 promises bytes, unavailable or not found during a partition, never wrong bytes. It does
  not promise read-your-writes while split: a product that serves the pre-split version of a
  key overwritten during the split passes.
- E7's budget never rots metadata blobs or saga logs. Faults beyond the budget are not
  explored. The stalled-coordinator defect was not reproduced on tier R.
- **OPEN AT THE TIME OF WRITING** (delete this item when closed): two tests of the new
  migration rule are missing after review round 3.
  - R3-1: no test holds the shard threshold. Changing `verified < quorum` to `verified < 1`
    survives. Needed: a staged version keeping fewer than k verified shards must compensate
    before the flip; one keeping at least k but fewer than the quorum must wait, be rebuilt,
    then complete.
  - R3-2: no test holds the re-check in `Retire`. Removing it survives. Needed: destination
    holders wiped after the verify step and before `Retire`; or remove the check and say why
    the catch-up's verification makes it unnecessary.

## Seeds and replay

- Pinned in `test/e2e/testdata/seeds.txt`, run on every invocation by every scenario:
  - `166963932986536`: E7; a wipe destroyed the only copy of an acknowledged tag write. The
    budget rule `metadataElsewhere` came from it.
  - `1`: E7; the not-found-for-unavailable defect. With `f70e2c2` undone it fails on
    `get w/k15 via n017`.
- Not pinned: E7 seeds 102 and 290 exposed two harness defects (pitfalls below); seeds
  31337, 3 and 4 lose an object in the adversarial driver `a02-ambiguous-start.sh` on the
  code before `eaa0556`.
- `sweep` (`harness_test.go`) runs seeds in four parallel subtests, `shard=<seed mod 4>`.
  The shard was the seed's index mod 4, so a single-seed replay always landed in `shard=0`
  and the printed command for any seed in shards 1–3 matched nothing and reported `ok`.
- A run with an explicit `-seed` that executes no seed subtest fails (`explicit -seed=N ran
  0 seed subtests`). `TestReplayCommandsReproduceEverySeed` plants a failure on every seed
  under `DST_SEEDS=8` and re-executes each printed REPLAY line, E5 cells included.
- Each scenario sweeps a share of `DST_SEEDS` (`seedsFor`), pinned seeds always in addition:
  E7 3/5, E8 1/2, E2, E3, E6 and the E5 matrix 1/4, E1 1/5, E4 1/10, E1 multi-megabyte 1/25.
  The stalled-coordinator test takes 1/10. At the default of three seeds that is one
  unpinned seed per scenario. E5 spreads its seeds over the 78 cells.
- `E2E_FULL_SEEDS=1` gives every scenario the full count; an explicit `-seed` also bypasses
  the shares. `E2E_TIER_R=1` enables the tier R tests; without it they skip.
- `-short` trims one thing: the E5 failure matrix runs every eighth cell (10 of 78) on one
  seed (`e5CellSeeds`). An explicit `-seed` or `DST_SEED`, or `E2E_FULL_SEEDS=1`, wins over
  `-short` and runs the full matrix.
- Under `-race`, tier S runs one unpinned seed per scenario and every eighth E5 cell: the
  simulated tier has no goroutines, so the race build only multiplies the time.

## Costs and how to run things

In the `dev` container on a shared 4-core machine.

- `go test -count=1 ./test/e2e`: 351 s after the migration fix. The E5 matrix is about four
  minutes of it (`-run TestE5`: 229 s, 234 runs).
- `go test -short -count=1 ./test/e2e`: 73 s.
- `DST_SEEDS=500 go test -count=1 ./test/e2e -timeout 60m`: 1629.78 s, measured before the
  migration fix and not repeated on the final code by the builder. E7 runs 301 seeds, E4 52.
- `go test -race -count=1 ./test/e2e`: 460 s (170 s in the first build). The package no
  longer fits `go test`'s default 10-minute timeout under the race detector; pass
  `-timeout 20m` or more. The gate uses `-timeout 45m`, and its three repetitions cost about
  23 minutes.
- Tier R: `E2E_TIER_R=1 go test ./test/e2e -run 'R$|TierR|DefaultBuild|CanaryE9' -count=1 -v
  -timeout 40m`, 13 tests in 241.93 s. It is bound by real timeouts and must not run beside
  another heavy job.
- Fuzz: `go test ./test/e2e -run '^$' -fuzz '^FuzzClusterNemesis$' -fuzztime 120s
  -fuzzminimizetime 5s` gave 384 executions. The hardening runs (15 fuzz targets, a sweep of
  nine packages) found no crasher and no failing seed; tables under
  `agent_work/07-e2e/hardening/`.
- Gate (`agent_work/gates/gate.sh <chunk>`): G1, G4 and G5 pick the package up through
  `./...`. G5R runs the tier R command above after the sweep; a run in which any selected
  test skipped, or none passed, is red. `GATE_SHORT=1` makes G1 run `go test -short`; such a
  result never counts for landing.
- `agent_work/gates/ci-fast.sh` is the fast lane for a push: G0, G1 short and G6, with the
  sweep variables unset. The 500-seed sweep, race, fuzz, tier R and the full gate are
  on-demand runs, for example `GATE_ONLY=G5 agent_work/gates/gate.sh <chunk>`.

## Pitfalls

- A statistical check at sweep level false-fails a single-seed replay. A total over the
  sweep ("some migration moved shards") failed E7 seed 102 alone, on a correct product. Make
  the check exact per event (each completed migration against what the bucket held when it
  started). A sweep floor must be guarded by a minimum seed count
  (`checkE7AvailabilityFloor`: 10 per seed, only from 10 seeds).
- A check that passes on the old code proves nothing. The first wipe-guard subtest passed
  with the guard removed. Mutation-test every repaired hole, and keep a canary for it.
- A layout that cannot place shards makes every migration empty. The first fuzz layout, 9
  nodes at 4+2, had 3 nodes per region for 6 shards; every PUT to the routed bucket failed
  with an allowed error, and 234 fuzz executions migrated an empty bucket.
- A timed-out start is "outcome unknown", not "nothing started". The runner dropped a
  migration whose reply was lost, left its region stale and unprotected its coordinator.
  After the heal, look the saga up on the coordinator and follow it to the end.
  `ErrUnreachable` on a request is a refusal, so nothing ran.
- Accepting any allowed error without requiring a success hides a dead feature. E2 passed
  with every partition-time object read and write unavailable. Derive the required successes
  from the layout, the side size and the pre-cut shard placement.
- Accepting any error at all on some operations hid the first product defect: `settle`
  checked only PUT errors. List the allowed terminal errors per operation and reject the
  rest.
- Counters and loops must be required non-zero. A dozen checks passed over empty sets:
  an empty damage list, zero probes, zero traffic in flight at SIGTERM.
- When a check is voided by a fault, void only what the fault could have touched. Voiding
  every availability read of a cycle with any network fault dropped the per-seed count from
  p50 20 to p50 7, and one seed to 0.
- Package-level state in test or mutation code leaks between the two runs of
  `RequireDeterministic` and between parallel shards, and shows up as "same seed gave
  different digests". Reset it at node construction.
- A background collector can mask a missing step. A purge that did nothing passed tier S
  because garbage collection removed the shards inside the await bound. Check the source
  region the moment the saga completes.

## Found by the gate after the fix round

- A 500-seed sweep with a new seed base failed `internal/merkle`
  `TestLyingPeerDST/.../seed=214339956433005` ("the liar was never engaged: failed 23 invalid
  0") on a correct product: a per-seed guard required something only probable. It is now a
  per-seed check of what every seed shows plus a sweep-level floor from ten seeds up, and the
  seed is pinned (`e130ca7`; 1 of 1,503 seeds measured). Same lesson as the E7 check: a
  guard against a vacuous pass must itself be exact per seed, or sit at the sweep with a
  minimum seed count.
- The real-process step (gate G5R) failed both times it ran straight after the hour-long
  sweep (PUTs answered 503 on a one-second shard request timeout) and passed every other
  time: alone, and after ten minutes of heavy tests at a load of 4. Waiting for the load to
  drop did not help. The cause after sustained load is not established. The gate runs the
  step first among the test gates. The product-side remedy is a configurable shard request
  timeout, which this chunk did not build.
- `GATE_OUT` must be a path inside the repository: the gate runs in a container, and a path
  under `/tmp` is lost with it.

## To reuse in Chunk 8 and later

- `internal/nodetest` (`lineage.go`): `Lineage`, `ContentOf`, `LWWAfter`, `Covers`,
  importable by any test package. `internal/node`'s own tests use the same code.
- `node.ManifestOfRecord` and `node.ManifestOfStaged` are product functions now.
  `storage.Health` carries `MetaDigest`, `MetaRoot` and the counter `rebuilt_bytes`.
- The `Cluster` interface with `simCluster` and `realCluster` is the pattern for one scenario
  on both tiers. `startRealCluster` starts and cleans up real processes.
- `runNemesis` with `randomPlan` or `planFromBytes` runs a fault schedule with all oracles.
