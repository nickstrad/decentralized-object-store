# Chunk 7 build notes — end-to-end suite, nemesis cycles, fuzz hardening

Seat: Opus builder. Branch `chunk/07-e2e`, worktree
`/root/Software/decentralized-object-store-work/07-e2e`. Everything below ran in the `dev`
container (`docker compose run --rm -T dev ...`) unless it says "host". Tier S numbers are
virtual time; tier R numbers are wall time on the shared 4-core build VM with `-race` builds of
`objnode`. "500 seeds" means `DST_SEEDS=500 DST_SEED_BASE=7001 go test ./test/e2e -v`
(record: `hardening/gate-equivalent/e2e-500-seeds.txt`); tier R numbers come from the final
tier R run (`hardening/tier-r.txt`).

Short version: the nine scenarios exist and pass on both tiers where the plan's table asks
(E7 tier S only, E9 tier R only), with the three-outcome object oracle, the discard checks
and the deletion, loss and spread oracles running at every step of tier S. The suite found
**no product defect**; it found test defects of my own, two operating limits of the real
runtime (section 6) and the cost problem that forced per-scenario seed shares (section 5).
The hardening fuzz runs and the large seed sweep are in section 7.

## 1. What was built, file by file

### test/e2e (new; every file but `e2e.go` is a `_test.go` file of package `e2e`)
| File | What it is |
|---|---|
| `e2e.go` | `package e2e` only, so the directory is a package for `go build ./...` and `go vet` |
| `cluster_test.go` | the `Cluster` interface both tiers implement (object API calls returning a `*Call`, `Wait`, `Sleep`, `Await`, faults, shard files on disk, health, members, `Converged`); `Call`; the client-side `Ledger` that records every PUT attempt's SHA-256 and checks every GET against it |
| `api_test.go` | tier S only: the client protocol over the simulated network (channel byte `0xA0`, op byte, JSON head, raw body), `apiNode` (wraps each node: serves client calls by calling the node's own methods, passes every other channel through unchanged) and `clientProc` (one client process per region) |
| `simcluster_test.go` | tier S driver: 21 nodes (by default) in regions r0–r2 plus three clients; optional static clock skew and drift per node; every node's disk wrapped by the deletion oracle and its transport by a byte counter (E3); `Await` steps virtual time in 20 ms slices and fails with the name of what it waited for |
| `simoracle_test.go` | tier S oracles, all event-driven (see section 2) and the end-of-run checks: `checkAcks`, `checkLiveReadableOrLost`, `checkOrphans`, `finalChecks` |
| `harness_test.go` | `runSim` (every tier S scenario goes through `simtest.RequireDeterministic`), the four-way seed sharding, `seedsFor` (per-scenario seed shares, section 5), `measure` |
| `scenario_e1_test.go` … `scenario_e6_test.go` | the shared scenario definitions E1, E2 + E8, E3, E4, E6, written once against `Cluster` and run by both tiers; shared checks (`collectShards`, `checkSpread`, `checkContests`, `discardLog`, `splitViews`, `shardsOutside`, `routedTo`, `awaitMigration`) |
| `e5sim_test.go` | E5 tier S: a `sagasim.Harness` built on the e2e cluster (clients on the network, background traffic) so `sagasim.Run` drives every cell of the 78-cell failure matrix |
| `nemesis_test.go` | the nemesis engine shared by E7 and the fuzz target: chaos ops, the fault budget, live traffic, heal checks; `randomPlan` (E7) and `planFromBytes` (fuzz) |
| `e7_test.go` | `TestE7NemesisHealingCyclesS`, `FuzzClusterNemesis` |
| `tiers_test.go` | tier S tests E1 (two tests), E2, E3, E4, E6, E8 with their measurements |
| `realcluster_test.go` | tier R driver: builds `objnode -race -tags e2efault` once per test binary, starts processes, HTTP client, error mapping from status codes back to the sentinels, blocklist files, SIGKILL, wipe, file bit-rot, `TestMain` removing the built binaries |
| `tierr_test.go` | tier R tests E1–E6, E8, E9, the E9 canary, the harness hygiene test, the default-build check |
| `canary_test.go` | the tier S canaries (section 4) and the switches they use |
| `oracle_self_test.go` | one test per oracle that plants the fault it exists to catch, plus `TestHealthReportsTheMetadataDigestAndRoot` |
| `race_on_test.go`, `race_off_test.go` | `raceEnabled` by build tag (section 5) |
| `testdata/seeds.txt` | empty pinned-seed file (no seed has failed) |

### internal/nodetest (new, test support)
`lineage.go`: `Lineage` (the per-key acknowledged-write oracle), `ContentOf`, `LWWAfter`,
`Covers`. These are `internal/node`'s `ackOracle`, `contentOf`, `lwwAfter` and `covers` from
`oracle_test.go`, **moved**, not copied: `internal/node`'s tests now call them (`helpers_test.go`,
`oracle_test.go` and five `h.acks.Check` call sites), and the e2e oracle uses the same code.
The field `lostWhy`, never set anywhere, was not carried over; nothing else changed in logic.
`internal/nodetest` is not under a detlint root and no product package imports it. It has no
test file of its own: it is exercised by `internal/node`'s oracle self-test
(`TestAckOracleKeepsIdenticalPayloadsOnTwoKeysApart`) and every node DST that calls `Check`,
and by `TestAckOracleFlagsALostWriteMissingFromEveryDiscardLog` here.

### Changes in landed packages
| Where | Change | Why |
|---|---|---|
| `internal/node/records.go`, `export_test.go` | `ManifestOfRecord`, `ManifestOfStaged` move from `export_test.go` into the product | the e2e oracles decode manifests from metadata records; `export_test.go` is visible only to `internal/node`'s own tests |
| `internal/storage/api.go`, `internal/node/objects.go` | `Health` gains `MetaDigest` and `MetaRoot` (hex of the OCC digest and the metadata Merkle root, empty until the node is ready) and the counter `rebuilt_bytes` | tier R sees nodes only through HTTP; it needs these to check "identical metadata bytes and Merkle roots" and bytes rebuilt per damaged byte |
| `internal/noderuntime/runtime.go` | one hook: `var outboundFilter func(dataDir string, to env.NodeID) bool`, consulted in `realNet.Send` (drop) and `realNet.Request` (no dial; the request's own timer fires `ErrTimeout`), nil in a default build | the e2efault blocklist |
| `internal/noderuntime/e2efault.go` (`//go:build e2efault`) | sets the hook: reads `<data dir>.e2e-blocklist`, one peer ID per line, at most every 50 ms; `BlocklistPath`, `BlocklistSuffix` | tier R partitions |
| `internal/noderuntime/e2efault_test.go` (tagged) | path test; two runtimes split by blocking both directions, rejoin when the files go | |
| `internal/noderuntime/runtime_net_test.go` | the test `realCluster` keeps its data dirs | used by the tagged test |
| `cmd/objnode/main.go` | after shutdown, `leakReport` waits up to 2 s for every goroutine except the signal loop to end and prints `objnode: goroutines left at exit: N` with each stack; exit status unchanged | E9's "no goroutine left (the binary's own leak report)" |
| `cmd/objnode/main_test.go` | `TestLeakReportCountsAGoroutineUntilItEnds` | |

No `panic(` was added anywhere; `agent_work/gates/panic-allowlist.txt` is unchanged. No error
is ignored with `_ =` in product code; in tests: `_ = cmd.Wait()` (the exit state is read from
`ProcessState`), `_ = logf.Close()` and `_ = os.RemoveAll(binary dir)` in tier R cleanup,
`_ = json.Unmarshal(body, &eb)` when an error body is not JSON (the raw text is used), `_, _ =
h.Write` into an FNV hash.

## 2. How tier S works and what its oracles check at every step

**Clients over the simulated network.** Each node is wrapped in `apiNode`; three client
processes (`client-r0..2`, one per region) send object-API calls as requests on the simulated
transport, so client traffic sees latency, partitions (a region cut takes its client with it),
lost responses and timeouts like any other traffic. The wrapper calls the node's own methods
(`PutObject`, `GetObject`, `PutTag`, ...); errors cross the wire through the node's code
table, so `errors.Is` works on the client side. No code of this repository is mocked.

**One scenario definition for both tiers.** Scenarios are blocking Go code against `Cluster`.
On tier S, `Wait` runs the simulator until the calls complete and `Await` advances virtual time
in 20 ms slices until a condition holds; on tier R they block on goroutines and poll every
100 ms. Everything random comes from `c.Rand()` (the workload stream on S), so tier S runs stay
deterministic and every one goes through `RequireDeterministic`.

**Oracles that run at every step (tier S).**
- *No GET returns wrong bytes* (`Ledger`, registered as the invariant
  `e2e/no-get-returns-wrong-bytes`, checked after every event): every PUT attempt records
  `(key, SHA-256, size)`; a GET's bytes must match its reported size and content hash; if its
  ETag is an acknowledged version, the bytes must be exactly the ones written under it;
  otherwise they must be the bytes of some attempt on that key (a write whose response was
  lost may still win).
- *Acknowledged metadata writes* (`nodetest.Lineage` fed by `CommitSink` and `DiscardSink`):
  per-key ancestry, independent of OCC's classifier. Checked at every heal (E7) and at the end:
  each acknowledged write is current, a migration copy of the current content, superseded by a
  later write that held it, or named as a loser in a discard.
- *Deletion safety* (`watchDisk` around every node's disk): a shard delete of a version that
  some key on any node (up, down or paused) can still make the winner fails the run; so does
  committing a version whose shards were already deleted.
- *Loss declarations*: when any node commits `~lost/<v>`, every stripe of `v` is checked on its
  holders' disks; the run fails unless some stripe has fewer than k verifiable shards ("declared
  lost while k verifiable shards of every stripe exist").
- *Spread*: when a client PUT's manifest commits, if the writer's view at the time of the PUT
  had room (sum over regions of min(members, m) ≥ k+m, bucket not routed), no stripe may place
  more than m shards in one region.
- *Three outcomes after a full heal* (`checkLiveReadableOrLost`): every key whose manifest is
  live is read through a random node; it must read back (the ledger checks the bytes) or be
  declared lost on record; "live and unreadable" fails. E2, E6 and E7 additionally require
  zero declarations (inside the budget and the grace period nothing may be declared lost).
- *Convergence after heal*: `Converged` = every up node ready, live membership equal to the set
  of up nodes, identical OCC digests (metadata bytes) and identical metadata Merkle roots.
  One version per key holds by construction of the OCC store; the digest equality is the check.
- *No orphan shards after the grace periods* (`checkOrphans`, end of E2, E6, E7): after
  `OrphanGrace + GCGrace + 10 s`, every shard blob on an up node must be expected there by a
  live manifest.

## 3. The scenarios: claim → test → measured number → what it does not show

Tier S cluster: 21 nodes, 3 regions, k=4 m=2, 64 KiB stripes, request and RPC timeouts 500 ms,
gossip probe 250 ms. Tier R: 9 `objnode -race -tags e2efault` processes, 3 per region, k=4 m=2
(E4, E5: k=2 m=1, see section 6), 64 KiB stripes, all other settings default (GC grace 10 min,
orphan grace 1 h, request timeout 1 s), data on the container's file system.

### E1 Multi-region put/get (S, R)
- Claim: objects of 0 B, 1 B, prime sizes and multi-MB written through a node in each region
  read back byte-identical through every other node; no region holds more than m shards of a
  stripe.
- Tests: `TestE1MultiRegionPutGetS` (sizes 0, 1, 2, 3, 7, 13, 4093, stripe−1, stripe, stripe+1,
  3·stripe+7), `TestE1MultiRegionMultiMBPutGetS` (2 MiB + 29), `TestE1MultiRegionPutGetR` (the
  same sizes plus 3 MiB + 29). Each object is written through a random node of each region and
  read through all other 20 (S) or 8 (R) nodes; the ETag and length must match the write and the
  ledger checks the bytes; `checkSpread` counts shard files on every disk per (version, stripe,
  region).
- Measured: S 500 seeds: 33 objects / 660 reads / 42 stripes checked per seed (100 seeds); the
  multi-MB test 3 objects / 60 reads / 99 stripes per seed (20 seeds); slowest put 141–164 ms,
  slowest get 162–168 ms (multi-MB: put ≤ 1.08 s, get ≤ 1.18 s). R: 36 objects, 288 reads, 189
  stripes checked; put latency small objects 36–651 ms, 3 MiB 1.96–2.22 s; get p50 15 ms, p90
  93 ms, max 829 ms; cluster start-up 1.5 s (processes started to `Converged`).
- Not shown: reads under faults (E2, E3, E7 do that); spread when a writer's view lacks regions
  (the spread oracle skips those); re-placement after a holder leaves for good (not built).

### E2 Partition and heal (S, R)
- Claim: during a region partition both sides keep accepting metadata writes and the object
  writes and reads their shards allow; anything else is `ErrUnavailable` (reads may also be not
  found), never wrong bytes; after the heal identical membership, metadata bytes and Merkle roots
  within the bound; no acknowledged object write lost; every metadata write discarded by
  last-writer-wins counted and logged.
- Test: `scenarioE2` via `TestE2PartitionAndHealS` (plus `finalChecks`) and
  `TestE2PartitionAndHealR`. Region r0 is cut off; right after the cut (before failure
  detection) both sides write a contested tag and new objects and read; the scenario then awaits
  "each side's view holds only itself" (measured), then both sides write the same tag on every
  key, the same ACL principal, contested objects on half the keys and side-specific new objects
  on the rest. Metadata writes must all succeed; object writes may only fail with unavailable;
  reads only unavailable or not found. After the heal: `Converged` within 30 s (S) / 90 s (R);
  `checkContests` — for each contested key, every acknowledged write is the current version
  (its ETag) or appears as `Loser` in some node's discard log (`/_health` `RecentDiscards`, a
  check that needs no product classifier and runs on both tiers; it fails with a distinct message
  if a node's discard tail could have overflowed its 64 entries); `lost_objects` = 0 on every
  node; on S also `Lineage.Check`, every live key readable, no stray shard after the grace periods.
- Measured (S, 125 seeds): failure detection p50 7.3 s, max 10.0 s; convergence after heal p50
  8.5 s, max 11.6 s (bound 30 s); per seed 9 object writes acknowledged during the cut and 6
  refused as unavailable (all of them before detection); 13 contested writes discarded and on a
  discard log, 16 current; lineage oracle: 12 discarded, 9 superseded; declared lost 0; live
  keys unreadable 0. R: detection 21.8 s, convergence 5.7 s (bound 90 s), 5 acknowledged and 10
  unavailable object writes, 25 contested acknowledged writes (15 current, 10 discarded and
  logged), declared lost 0.
- Not shown: a cut longer than `OrphanGrace` (the declared-lost path; Chunk 6's
  `lost_test.go`), asymmetric cuts (E7 does them), quota under partition (Chunk 6's partition
  DST).

### E3 Bit-rot detection and repair (S, R)
- Claim: with up to m shards per stripe corrupted on disk every GET returns correct bytes;
  anti-entropy restores every corrupted shard within the bound; bytes moved for repair are
  proportional to the damage, not the bucket.
- Test: `scenarioE3` via `TestE3BitRotRepairS`, `TestE3BitRotRepairR`. 16 objects of 3 stripes
  + ~100 B. Phase 1: rot 1..m shards of every stripe of two objects (S: `sim.BitRot`; R: one
  byte flipped in the middle of the shard file's payload, written back by rename), read every
  object at once (all must succeed with the written version), await every rotted shard
  verifiable again. Phase 2: rot two other objects the same way with no traffic and measure
  time to repair, `rebuilt_bytes` (summed over nodes) per damaged byte, and on S the bytes moved
  on the storage channel (requests + replies, counted by a transport wrapper on every node) per
  damaged byte. Asserted: rebuilt bytes between 0.9× and 2× the damage; moved bytes ≤
  (k+m)·damage + 64 KiB; the bucket at least twice that limit, so a bucket-wide transfer would
  fail the check.
- Measured (S, 125 seeds): 20–28 shards rotted per seed; repair with reads p50 1.24 s, max 1.94
  s; repair without traffic p50 2.0 s, max 2.04 s (bound 30 s); rebuilt per damaged byte 1.000
  on every seed; moved per damaged byte p50 4.46, max 5.02 (≈ k fetches per rebuilt shard plus
  headers); bucket 4.75 MB vs damage 0.10–0.20 MB. R: 23 shards rotted, repair 1.4 s with
  reads and 1.4 s without, rebuilt 148,302 B for 148,302 B damaged.
- Not shown: rot of metadata or saga-log blobs (the budget never rots them); rot beyond m
  (Chunk 6's lost tests); moved bytes on tier R (no counter for it).

### E4 Bucket migration, success (S, R)
- Claim: a bucket moves from region A to B under live traffic; it ends with verified equal
  Merkle roots, the routing pointer on B at every node, zero shards of the bucket left in A,
  every object readable.
- Test: `scenarioE4` via `TestE4MigrationUnderTrafficS`, `TestE4MigrationUnderTrafficR`. A
  bucket routed to r0 with 8 objects; migration to r1 started through node 2; every 250 ms until
  the saga is terminal a client writes a new key to the bucket, reads an old one and (every
  other tick) writes to another bucket. Traffic may only see success, busy (between flip and
  promote) or unavailable. Then: `Converged` (equal roots), no shard of the bucket outside r1,
  every acknowledged object (old and written during the move) reads back the bytes written, and
  a probe written through every node must land only in r1 — a node that still routed to r0
  would place it there; that is how "pointer on B at every node" is checked on both tiers.
- Measured (S, 25 seeds): begin to completed p50 24.8 s, max 26.9 s (bound 40 s); 187–265
  client calls per seed, 62–88 of them busy or unavailable, none other; 20–28 objects read back.
  R (k=2 m=1): 10.5 s under 97 calls (34 busy or unavailable), 13 objects read back.
- Not shown: two concurrent migrations (Chunk 6's duel DST), migration across a partition (E7
  mixes migrations with cuts).

### E5 Bucket migration, rollback (S every failure point; R kill target mid-copy, kill coordinator mid-saga)
- Claim: the saga ends compensated: pointer still on A, zero orphan shards in B, source intact;
  a restarted coordinator resumes from its log and reaches the same end.
- Tests: `TestE5MigrationEveryFailurePointS` runs `sagasim.Run` for every one of the 78 cells
  (each step failed or crashed before or after its action and mid-compensation, torn or not,
  single or double crash) on the e2e cluster with clients on the network and 60 background
  rounds of client traffic (a put to another bucket and a get of the migrating one); each cell must reach its expected terminal state; `CheckCompensated` requires the route
  on r0 at every up node, no live staged entry, every source object decoding byte-identical from
  disks in r0, no shard of the bucket on any r1 disk and no wrong read; `CheckMoved` the mirror.
  Crash cells crash and restart the coordinator, which resumes from its log.
  `TestE5RollbackKillTargetMidCopyR` and `TestE5RollbackKillCoordinatorMidSagaR`: a routed
  bucket of 24 objects; once the saga reports step ≥ 1 (copy), the coordinator's outbound
  traffic to every r1 node is blocked (so the copy cannot finish and compensation must happen)
  and either the last r1 node is SIGKILLed, or the coordinator is SIGKILLed and restarted; the
  saga must reach `compensating`, then the block is lifted (and the target restarted), the saga
  must end `compensated`, every source object must read back unchanged, no shard of the bucket
  may remain outside r0, and probes through every node must land in r0.
- Measured (S, 500 seeds → 202 runs over the 78 cells): 112 completed, 90 compensated, each the
  cell's expected state; begin to terminal p50 11.4 s, max 16.9 s. R: kill target — from the
  kill to compensated 7.4 s; kill coordinator — 5.2 s; both stopped the saga at step 1.
- Not shown: a coordinator that never returns (stated limit, OQ-13); on R the compensation is
  forced by the blocklist, not by an organic copy failure.

### E6 Node crash and restart (S, R)
- Claim: SIGKILL during writes; acknowledged objects survive; the restarted node rejoins with a
  higher incarnation, catches up by anti-entropy and serves reads; a wiped node is refilled.
- Test: `scenarioE6` via `TestE6CrashRestartWipeS` and `TestE6CrashRestartWipeR`. A wave of 8
  writes; a second wave issued and the victim (node 4) killed 30 ms later; a third wave while it
  is down (writes may only fail as unavailable); restart after 4 s (S) / 3 s (R); await every
  other node seeing the victim alive with an incarnation above the one before the crash and
  `Converged` ("rejoin"); on S also await every shard that the live manifests place on the
  victim being verifiable on its disk ("catch-up"); read every acknowledged object through the
  victim. Then node 8 is wiped (S `WipeRejoin`; R kill, remove the data directory, start) and
  must hold again every shard the manifests place on it (S) or every shard it held before (R).
  Finally every acknowledged object is read through random nodes, and on S the full final checks
  run, including no stray shard after the grace periods.
- Measured (S, 125 seeds): 24 writes acknowledged per seed, none refused; incarnation 0 → 1 or
  2; rejoin plus metadata catch-up p50 5.1 s, max 10.4 s; shard catch-up within the same span
  (bound 30 s); refill p50 2.2 s, max 6.4 s (bound 60 s); 3–14 shards on the victim, 1–14 on the
  wiped node; nothing declared lost. R: incarnation 0 → 2, rejoin 3.4 s, refill of 18 shards
  2.3 s.
- Not shown: shard catch-up on R is checked only against the shards the node held before (the
  harness cannot read manifests over HTTP); torn-tail crashes (E5 crash cells and the Chunk 6
  crash DST cover them).

### E7 Nemesis and healing cycles (S, long, many seeds)
- Claim: alternating chaos and heal for many cycles; safety invariants at every step;
  availability within the fault budget; full convergence after each heal.
- Test: `TestE7NemesisHealingCyclesS` (and the same engine under `FuzzClusterNemesis`). Each
  seed: 21 nodes with static clock skew up to ±3 s and drift up to ±1000 ppm, 0.5 % datagram
  loss, 4 cycles of 4 s chaos. Each cycle draws 2–5 ops (some crashes and pauses get a later
  restart) from the stream `e2e/nemesis`: crash, pause, wipe, restart, region cut, one-way cut of
  a node, isolate, lossy link (10–80 %), clog, bit-rot of a shard, read errors on a shard, and a
  bucket migration (completed or compensated, either is fine). Live traffic every 250 ms: puts,
  overwrites, tags, deletes and gets on 24 keys, writes into the migrating bucket. **Budget**: a
  node or disk fault is applied only if afterwards every stripe of every live manifest keeps k
  verifiable shards on up, unpaused, untouched holders, and at most three nodes are down or
  paused; the migration coordinator is never wiped. At every step the event-driven oracles run.
  At each heal (`FaultHealAll`): all pending calls must have ended with success or an allowed
  error; `Converged` within 60 s; a running migration must end and leave no shard of its bucket
  outside its region; every shard of every live manifest back on its holder; `Lineage.Check`;
  zero declared losses; every live key readable. **Availability**: in a cycle with no network
  fault, a read of a key settled before the cycle, through a node no fault touched, must not
  fail except "not found". After the last cycle: no stray shard after the grace periods.
- Measured (500 seeds → 300 seeds, 1200 heals): 7–18 faults applied per seed, 0–6 skipped by the
  budget; convergence after a heal p50 7.6 s, p99 18.7 s, max 26.2 s (bound 60 s); every shard
  back by the time of convergence on every heal; 31–57 puts per seed (19–54 acknowledged), 4–42
  availability reads per seed, all served; 38–145 distinct discarded losers per seed, every one
  explained; migrations completed 0–3 and compensated 0–1 per seed; declared lost 0.
- Per-seed cost cut, as the brief asks, before seeds or checks: cycles 5 → 4, chaos 6 s → 4 s,
  orphan grace 60 s → 20 s and GC grace 10 s → 5 s for E7 only (both still longer than any cut
  E7 makes), objects at most one stripe (64 KiB) and 16 KiB in the migrating bucket: 7.2 s → 1.8
  s of wall per seed.
- Not shown: clock *steps* during a run — the simulator offers only static skew and drift per
  node at creation (`sim.NodeSpec`); a run with a moving clock needs a simulator change (request
  in section 9). Faults beyond the budget (the fuzz target and the Chunk 6 lost tests explore the
  boundary under the same budget; nothing here lifts it). Metadata blob corruption.

### E8 Local-first metadata sync, node level (S, R)
- Claim: two regions edit tags, ACL entries, quotas and object metadata while disconnected,
  using conditional writes; after healing all replicas converge to the same single version of
  every object; every discarded concurrent write is counted and logged (checked against the
  nodes' discard logs).
- Test: `scenarioE8` via `TestE8LocalFirstMetadataSyncS` (with clock skew ±2 s, drift ±500 ppm)
  and `TestE8LocalFirstMetadataSyncR`. Six objects with two tags each, two ACL principals and a
  quota; their ETags read; r0 cut from r1+r2; each side then edits with `If-Match` of the
  pre-cut ETag: the same tag on every object (contested), a second tag edited on one side only,
  one ACL principal on both sides, another on one side, the same object overwritten on both
  sides, and different quotas on both sides. Every conditional write must succeed while
  disconnected (an object overwrite may also be unavailable). After the heal: `Converged`;
  `checkContests` (every acknowledged edit current or logged as a discarded loser); quota limits
  identical on every node and equal to one side's write; every losing edit's ETag, reused in
  `If-Match`, must now get precondition failed.
- Measured (S, 250 seeds): 25 conditional writes per seed while disconnected, all accepted; 15
  current and 8 discarded and logged per seed; 6 stale ETags refused after the heal per seed;
  convergence p50 5.5 s, max 10.6 s; lineage oracle 7 discarded. R: 22 acknowledged (15 current,
  7 discarded and logged, 1 overwrite unavailable), convergence 4.5 s, 6 stale ETags refused.
- Not shown: quota versions cannot be matched to discard records (`QuotaInfo` carries no ETag),
  so for quotas the test checks one winner on every node and that it is one side's write.

### E9 Shutdown and cancellation (R)
- Claim: SIGTERM under load: in-flight requests finish or fail cleanly; the process exits within
  the deadline with exit status 0 and no goroutine left (its own leak report); restart is clean.
- Test: `TestE9ShutdownUnderLoadR`: 8 client goroutines writing 40 KB objects to node 5 and
  reading back their previous write; after 2 s SIGTERM; the process must exit within 20 s with
  status 0 and log `goroutines left at exit: 0`; every request must have succeeded or failed
  cleanly (503, 504 or a refused/reset connection; anything else, a hang or wrong bytes fails);
  restart; `Converged`; every acknowledged write reads back through the restarted node.
- Measured: exit 1.28 s after SIGTERM (shutdown timeout 10 s); 8 requests in flight at SIGTERM,
  8 finished, 0 failed; 64 requests succeeded overall, 856 refused cleanly after the listener
  closed; 34 acknowledged writes read back after a restart that converged in 1.8 s.
- Not shown: context cancellation inside the node (Chunk 6's cancellation tests); shutdown while
  the disk stalls.

### Harness hygiene and the default build (R)
- `TestTierRHarnessLeavesNothingBehindWhenATestFails` re-executes the test binary to run a child
  that starts a 3-node cluster and then fails; the parent checks that every child process is gone
  (`kill(pid, 0)` = ESRCH), every TCP port it used can be bound again, and its cluster and binary
  directories are removed. Processes are started with `Pdeathsig: SIGKILL` as a second guard.
- `TestDefaultBuildCarriesNoFaultInjection` builds `objnode` with and without `-tags e2efault`
  and requires the tagged binary to contain `.e2e-blocklist`, `noderuntime.blockedPeer` and
  `noderuntime.BlocklistPath` and the default binary none of them. (The default build does carry
  the nil hook variable `outboundFilter`.)

## 4. Planted faults and canaries (one per scenario; the ones marked "test" are kept)

| Scenario | Planted fault | How it fails | Kept as |
|---|---|---|---|
| E1 | every node tells its environment it is in r0 | spread: 3 shards of a stripe in r0 | test `TestCanaryE1NodesThatMisreportTheirRegionFailTheSpreadCheck` |
| E2 | node 4 drops OCC and metadata-Merkle traffic in both directions from the heal on | not converged after the heal within 30 s | test `TestCanaryE2ANodeDeafToAntiEntropyNeverConverges` |
| E3 | `MaintainPeriod` = 1 h (no scrub, no rebuild) | rotted shards not rebuilt within 30 s | test `TestCanaryE3RepairThatNeverRunsMissesTheBound` |
| E4 | the migration's delete-source step answers "moved" without doing anything | shards of the bucket left in r0 | test `TestCanaryE4AMigrationThatSkipsDeletingTheSourceFails` |
| E5 | the copy step's compensation does nothing | `CheckCompensated`: a staged entry left (and shards in r1) | test `TestCanaryE5ACopyThatIsNeverCompensatedLeavesOrphans` |
| E6 | the restarted node is deaf to OCC and metadata Merkle traffic | never rejoins with caught-up metadata within 30 s | test `TestCanaryE6ARestartedNodeDeafToAntiEntropyNeverCatchesUp` |
| E7 | every node's disk silently ignores shard deletes | 44 stray shards after the grace periods | test `TestCanaryE7ShardsThatAreNeverCollectedFailTheOrphanCheck` |
| E8 | node 3 deaf to OCC and metadata Merkle traffic from the heal on | no single version per key within 30 s | test `TestCanaryE8ADeafSideNeverReachesOneVersionPerKey` |
| E9 | SIGKILL instead of SIGTERM | exit status −1, no leak report | test `TestCanaryE9AKilledNodeFailsTheCleanShutdownChecks` (tier R) |

Each canary runs the scenario once with its switch and passes only when the scenario fails for
that reason (the error text is matched). The switches live in the tests (environment and
configuration wrappers, saga step wrappers), not in product code. The oracle self-tests
(`oracle_self_test.go`) plant the fault each oracle exists for: wrong bytes, a loss record over
a healthy object, deleting a live winner's shard, a stray shard, an unreadable live key without
a loss record, more than m shards in a region, a lost write missing from every discard log.

## 5. Seed counts, cost and the race build

Per-seed wall at 8 seeds, before the E7 cuts: E1 3.2 s, E2 0.9 s, E3 0.6 s, E4 5.9 s, E6 1.1 s,
E7 7.2 s, E8 0.3 s, E5 1 s per cell run — about 2.8 h for `DST_SEEDS=500`. After cutting E7
(section 3) and splitting the multi-MB objects of E1 into their own test, each scenario sweeps a
share of `DST_SEEDS` (`seedsFor`, pinned seeds always included): E7 3/5, E8 1/2, E2, E3, E6 and
the E5 matrix 1/4, E1 1/5, E1 multi-MB 1/25, E4 1/20. At `DST_SEEDS=500`: E7 300 seeds, E8
250, E2/E3/E6 125, E5 202 runs, E1 100, E4 25, E1 multi-MB 20. Measured: the whole package at
`DST_SEEDS=500` took **1494 s** wall (other jobs were running on the VM at the same time).
`E2E_FULL_SEEDS=1` gives every scenario the full count.

Under `-race` (gate G4) tier S runs one seed per scenario and every eighth E5 cell: the
simulated tier has no goroutines, so the race build only multiplies the time. Measured: `go test
-race -count=1 ./test/e2e` 170 s, so G4's `-count=3` costs about 8.5 minutes. Tier R tests skip
unless `E2E_TIER_R=1`.

## 6. Defects found

**Product defects: none.** No scenario, seed, canary-free run or fuzz input (section 7) failed
on product behaviour. Every failure during the build was in my own test code, fixed in place
before the scenario was committed: reading a key on another node before its metadata replicated
(E1), expecting shards for a 0-byte object (E1), tagging an object in the same batch that
created it (E2, E8), issuing partition-time writes before the failure detector had split the
views (E2 — now awaited and measured), a wiped node legitimately answering "no such bucket"
(fuzz seed corpus), and E9 reading a key before writing it.

**Operating limits found on tier R (not fixed; requests in section 9):**
- *The 1 s shard-write timeout under load.* With 36 concurrent PUTs (three of 3 MiB) on 9 `-race`
  processes sharing 4 cores with real fsyncs, shard writes passed the default 1 s
  `RequestTimeout` and PUTs answered 503 (`4 of 6 shards durable, need 5`). That is the
  documented behaviour (unavailable, never wrong bytes), but `objnode` has no flag for the
  timeout. The harness caps in-flight PUTs at 4 per cluster.
- *A routed bucket needs k+m nodes in its region.* With 3 nodes per region, a 4+2 routed bucket
  cannot place a stripe (`placement: 3 members for 6 shards`); E4 and E5 on tier R use 2+1.
- Failure detection of a region cut on default gossip settings took 21.8 s on tier R (7.3 s p50
  on tier S's 250 ms probe interval).

## 7. Hardening runs

Scripts: `hardening/run-fuzz.sh` and `hardening/run-sweep.sh`, both run in the `dev` container
on commit `28262f1`'s tree (the test code was final; only notes changed after).

### 7.1 Fuzz (PLAN 2.3, OQ-9)
Every fuzz target in the root module (15, found with `go test -list '^Fuzz'`): 15 minutes for the
four headline targets, 5 minutes for the rest, at most 3 at a time, `-fuzzminimizetime 5s`,
`-parallel 1` (one fuzz worker per target, so three targets fit the shared 4 cores). Logs:
`hardening/fuzz/<Target>.log`; table: `hardening/fuzz/summary.tsv`. "New" is the fuzzer's own
count of new interesting inputs found in this run; "corpus" its corpus size at the end (seed
corpus plus the generated cache, which is not committed).

| Target | Package | Budget | Executions | New | Corpus | Findings |
|---|---|---|---|---|---|---|
| FuzzErasureCoding | erasure | 15 min | 1,638,239 | 1 | 175 | none |
| FuzzMerkleDiff | merkle | 15 min | 1,206,992 | 65 | 360 | none |
| FuzzOCCConditionalWrite | occ | 15 min | 795,497 | 31 | 315 | none |
| FuzzClusterNemesis | test/e2e | 15 min | 234 | 148 | 222 | none |
| FuzzGF256 | erasure | 5 min | 1,465,627 | 0 | 35 | none |
| FuzzShardDecode | storage | 5 min | 2,048,357 | 0 | 18 | none |
| FuzzObjectAPI | storage/httpapi | 5 min | 1,088,426 | 14 | 156 | none |
| FuzzMerkleWire | merkle | 5 min | 1,286,188 | 1 | 108 | none |
| FuzzOCCMergeLaws | occ | 5 min | 211,166 | 19 | 325 | none |
| FuzzOCCDecode | occ | 5 min | 1,684,894 | 5 | 434 | none |
| FuzzGossipWire | gossip | 5 min | 1,215,010 | 0 | 88 | none |
| FuzzViewMergeLaws | gossip | 5 min | 1,098,174 | 1 | 135 | none |
| FuzzSagaLogRecover | saga | 5 min | 1,732,817 | 4 | 42 | none |
| FuzzSimSchedule | sim | 5 min | 97,121 | 7 | 493 | none |
| FuzzFrame | noderuntime | 5 min | 1,108,768 | 0 | 14 | none |

No crasher, so nothing was added under any `testdata/fuzz/<Target>/`. `FuzzClusterNemesis`
runs a whole 9-node cluster per input (about 0.26 executions per second with one worker); its
234 executions are 234 distinct whole-cluster fault schedules, each with safety checks at every
step and convergence after every heal.

### 7.2 Seed sweep
Every package whose tests import `simtest`, one package at a time, base `9000001` (fresh; the
gate derives its base from the commit). The count is 5000 where a 40-seed calibration (base
`424242`) predicted it fits 28 minutes, otherwise the largest count predicted to fit. `test/e2e`
keeps its per-scenario shares (section 5): at 332 that is E7 199 seeds, E8 166, E2/E3/E6 83, E5
161 cell runs, E1 66, E4 16, E1 multi-MB 13. Table: `hardening/sweep/summary.tsv`; per package
`<pkg>.run.summary` (top-level results and MEASURE lines) and `<pkg>.cal.log`. The full `-v`
logs (11 MB compressed) were deleted after the summaries were taken.

| Package | Seeds | Wall | Failing seeds |
|---|---|---|---|
| internal/gossip | 3733 | 1297 s | none |
| internal/merkle | 1976 | 982 s | none |
| internal/node | 442 | 1295 s | none |
| internal/occ | 1816 | 574 s | none |
| internal/saga | 5000 | 210 s | none |
| internal/saga/sagasim | 5000 | 0 s | none (its tests do not sweep seeds; nothing ran per seed) |
| internal/sim | 5000 | 942 s | none |
| internal/storage | 5000 | 1141 s | none |
| test/e2e | 332 (shares as above) | 996 s | none |

`internal/node` and `test/e2e` could not reach 5000 in 30 minutes (442 and 332). No seed
failed anywhere, so `testdata/seeds.txt` files are unchanged and there is no narrowing request.
E7 in the sweep: 199 seeds, 796 heals, convergence p50 7.7 s, max 26.2 s, nothing declared lost.


## 8. Limits and things not done

- Clock steps during a run are not exercised: the simulator only offers static skew and drift
  per node (section 9).
- Tier R does not count bytes moved by repair, does not check the routing pointer directly (only
  through where probe writes land) and checks shard catch-up after a crash only against the
  shards the node held before.
- The tier S sweep uses per-scenario seed shares (section 5) instead of `DST_SEEDS` for every
  scenario; E5 spreads its seeds over the 78 cells as Chunk 6 did (every cell runs at least
  once; at 500 seeds 202 runs, not 78 × 500).
- E7's budget never rots metadata blobs or saga logs and never wipes the migration coordinator
  (a lost coordinator is a stated limit).
- `internal/nodetest` has no test file of its own (section 1).
- No authentication is tested or added (stated limit).

## 9. Requests to the orchestrator

1. **Gate wiring for test/e2e.** G1, G4 and G5 already pick up `./test/e2e` (`./...`; G4's
   `-count=3` list names `test/e2e`). Expected costs: G1 about 110 s for the package at default
   seeds; G4 about 8.5 min for `-race -count=3`; G5 about 25 min at `DST_SEEDS=500` with the
   seed shares of section 5. Please confirm the seed shares (or give the budget you want and I
   will set them), since PLAN 2.9 says "500 seeds per scenario".
2. **Tier R as its own gate step**, after G6 inside the container:
   `E2E_TIER_R=1 go test ./test/e2e -run 'R$|TierR|DefaultBuild|CanaryE9' -count=1 -v -timeout 40m`
   (211 s wall on an idle VM; it builds `objnode` with `-race -tags e2efault` itself). It
   should not run concurrently with other heavy gate steps: it is timing-bound (real timeouts).
3. **G3 fuzz** includes `FuzzClusterNemesis` automatically (fuzz-short.sh lists targets with
   `go test -list`); at 30 s it runs about 90 executions.
4. **A simulator change request (not made, `internal/sim` is frozen):** a fault that steps one
   node's clock during a run (for example `FaultClockStep{Node, Delta}` adjusting the node's
   skew) so E7 and the LWW scenarios can exercise HLC behaviour under a moving clock. Until then
   E7 and E8 use static skew and drift.
5. **Product follow-ups to consider (not changed here):** an `objnode` flag for the shard request
   timeout (section 6); `QuotaInfo` carrying an ETag so a client can match a quota write to a
   discard record.
6. **Panic allowlist:** no new lines.

## 10. Commands run and results

All in the `dev` container.

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...`, `go vet ./test/e2e`, `go vet -tags e2efault ./internal/noderuntime` | clean |
| `go build ./...` | ok |
| `go test ./test/e2e -count=1` (default seeds) | ok, 107 s |
| `go test ./internal/node ./internal/nodetest -count=1` (after the move) | ok |
| `go test ./internal/storage/... ./internal/noderuntime ./cmd/... -count=1` | ok |
| `go test -tags e2efault ./internal/noderuntime -run Blocklist` | ok |
| `go test ./cmd/objnode -count=3` | ok |
| `go test -race -count=1 ./test/e2e` | ok, 170 s |
| `DST_SEEDS=500 DST_SEED_BASE=7001 go test ./test/e2e -v` | ok, 1494 s (`hardening/gate-equivalent/`) |
| `E2E_TIER_R=1 go test ./test/e2e -run 'R$\|TierR\|DefaultBuild\|CanaryE9' -v` | ok, 12 tests, 211 s (`hardening/tier-r.txt`) |
| `go test ./test/e2e -run Canary` | ok, 8 canaries |
| fuzz and sweep | section 7 |
| `agent_work/gates/gate.sh 07-e2e` (host) | run after the last commit; result in `agent_work/07-e2e/gates/` (uncommitted) and in the final report |

Coverage: `test/e2e` holds no product statements; `internal/nodetest` is covered only through
other packages' tests (section 1). The gate's G1 reports per-package coverage of the landed
packages.

