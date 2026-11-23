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
| `testdata/seeds.txt` | pinned seeds; one entry, E7 seed 166963932986536 (section 6) |

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
  paused; the migration coordinator is never wiped; a node is never wiped while it holds a
  metadata version no other up node holds (section 6). At every step the event-driven oracles run.
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

**Product defects: none.** No scenario, seed or fuzz input (section 7) failed on product
behaviour; the one gate failure in E7 (below) is a limit of the design, not a bug. Every failure during the build was in my own test code, fixed in place
before the scenario was committed: reading a key on another node before its metadata replicated
(E1), expecting shards for a 0-byte object (E1), tagging an object in the same batch that
created it (E2, E8), issuing partition-time writes before the failure detector had split the
views (E2 — now awaited and measured), a wiped node legitimately answering "no such bucket"
(fuzz seed corpus), and E9 reading a key before writing it.

**Found by the gate's G5 seeds (E7, seed 166963932986536): an acknowledged metadata write lost
with its only disk.** The nemesis cut `n000` off one way, a client tag write on `w/k06` was
acknowledged by `n000` (OCC acknowledges after a local fsync: one copy), and before anti-entropy
could copy it anywhere the nemesis wiped `n000`. The write was then not current, not superseded
and on no discard log, and the lineage oracle failed the run. This is not a protocol bug: a
local-first store that acknowledges writes while disconnected cannot keep a write whose only
copy is destroyed. The same holds for an object PUT whose manifest exists on one node only. It
was my budget that was wrong: it protected shard stripes, not the only copy of metadata. Fix
(test side, failing seed first): a wipe is applied only if every metadata version on the node is
also held, with the same version ID, by some other up node (`nemesis.metadataElsewhere`). The
seed is pinned in `test/e2e/testdata/seeds.txt`, and
`TestCanaryE7WipingTheOnlyCopyOfAnAcknowledgedWriteIsCaught` replays it with the rule off and
requires the lineage oracle to fail. After the fix, the 300 E7 seeds of the gate's base pass
(564 s). Needs a decision, see request 7.

**A flaky existing test, fixed:** gate G4 (`-race -count=3`) failed once in
`cmd/objnode`'s `TestServeAndShutdownOnCancel`: it picks a free port, releases it and another
package running at the same time took it (`bind: address already in use`). The test now retries
with a fresh port, at most three times, only on that error.

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
failed in the sweep. (The gate's own G5 seeds then found one E7 failure; section 6.)
E7 in the sweep: 199 seeds, 796 heals, convergence p50 7.7 s, max 26.2 s, nothing declared lost.


## 8. Limits and things not done

- Clock steps during a run are not exercised: the simulator only offers static skew and drift
  per node (section 9).
- Tier R does not count bytes moved by repair, does not check the routing pointer directly (only
  through where probe writes land) and checks shard catch-up after a crash only against the
  shards the node held before.
- Tier R write concurrency is capped at 1 in-flight PUT per cluster outside E9 (`realPutSlots` /
  `realBounds.writeBatch` lowered from 4 to 1 in `996b289`; E9 keeps `putSlots: 4`). With four
  concurrent PUTs, nine `-race`-built `objnode` processes on this 4-core VM exceed the fixed 1 s
  shard `RequestTimeout` and answer HTTP 503, which the stricter E2 check rightly no longer
  forgives. Lowering the load hides nothing about protocol correctness, but means tier R (apart
  from E9) no longer exercises concurrent writes; lifting this limit requires the product
  follow-up of a configurable shard request timeout flag on `objnode` (request 5 of section 9).
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
7. **Decision needed (Nick's gate, the stated guarantees):** metadata and manifests are
   acknowledged with one copy on the writer's disk. If that disk is lost before anti-entropy
   copies the write (seconds on a connected cluster, unbounded while the writer is cut off), the
   acknowledged write is gone without a discard record. Chunk 6's wording ("no acknowledged
   object write is lost", "every discarded write counted and logged") assumes the writer's disk
   survives. Either state this as a limit ("an acknowledged write survives the loss of its
   writer's disk only once it has replicated") or change the product (acknowledge only after one
   peer holds the write, which would stop a cut-off node from accepting writes). I narrowed E7's
   budget to the first reading; please confirm or rule.

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
| `agent_work/gates/gate.sh 07-e2e` (host), first run on `97da51e` | overall=fail: G4 (the port flake above) and G5 (the E7 seed above); G0–G3, G6, G7 green |
| `DST_SEEDS=500 DST_SEED_BASE=166963932986520 go test ./test/e2e -run '^TestE7'` after the fix | ok, 300 seeds, 564 s |
| `agent_work/gates/gate.sh 07-e2e` (host), rerun on the final commit | result in `agent_work/07-e2e/gates/` (uncommitted) and in the final report |

Coverage: `test/e2e` holds no product statements; `internal/nodetest` is covered only through
other packages' tests (section 1). The gate's G1 reports per-package coverage of the landed
packages.

## 11. Finish round, stage 1 (Neon): REVIEW-1 majors

Seat: Gemini 4 Neon (builder), branch `chunk/07-e2e`. Product code (`internal/`, `cmd/`): **no changes**. All three majors of `REVIEW-1.md` (Findings 1, 2, 3) are closed in `test/e2e/` with zero Go comments added; Findings 4–8 are left for stage 2 per brief.

### Finding 1 — the fuzzed migrating bucket is always empty

- **What was wrong:** `fuzzOptions()` (`test/e2e/e7_test.go`) overrode only `o.nodes = 9` (3 per region across `simRegions = 3`) while keeping `defaultSimOptions()`'s `Layout{K: 4, M: 2}` (`K+M = 6` shards per stripe). Routed bucket `m` places shards only inside its own region, so every PUT to `m` failed with `storage: placement: 3 members for 6 shards: unavailable`, which `nemesis.settle()` silently allowed. Every fuzzed migration moved 0 objects and 0 shards.
- **What changed:**
  - `test/e2e/e7_test.go`: changed `fuzzOptions()` to `nodes: 12` (4 nodes per region) and `Layout{K: 2, M: 1, StripeSize: simStripe}` (`K+M = 3`, `AckQuorum = 3`). Four nodes per region leaves 3 live nodes (`>= K+M`) inside the routed region even when one node in that region is crashed, paused, or isolated, so routed PUTs and migrations into/out of that region continue to place shards under single-node faults; `2+1` also halves per-stripe shard work relative to `4+2` to keep fuzz executions cheap. Extracted `fuzzCorpus()`, `runFuzzInput()`, `checkFuzzCorpus()`, set `fuzzWarmM = 2`, added `TestFuzzClusterNemesisCorpusMovesShards`, and added `e7/puts_m`, `e7/puts_m_ok`, `e7/migrated_objects`, `e7/migrated_shards` measures plus sweep-level assertions to `TestE7NemesisHealingCyclesS`.
  - `test/e2e/nemesis_test.go`: added `warmM`, `warmMOK`, `putsM`, `putsMOK`, `migratedObjects`, `migratedShards` to `nemesisStats` and `warmM` to `nemesis`; added a fault-free warm-up prefix in `nemesis.run()` (`n.warmM` 4 KiB objects into `bucketM` before cycle 1, failing immediately on any PUT error); added `bucketShardsIn()` to count live objects and verified shards of `bucketM` on disks in `n.region` after a completed migration and `shardsOutside == 0`, failing when `n.warmM > 0 && shards == 0`.
  - `test/e2e/canary_test.go`: added `TestCanaryFuzzRoutedBucketCannotPlaceShards`.
- **E7 and Tier R check for the same hole:**
  - `e7Options()` uses `defaultSimOptions()`: 21 nodes (7 per region) at `4+2` (`7 >= K+M = 6`), tolerating a single node fault in the routed region. No hole. Across 31 E7 seeds (`DST_SEEDS=50`): `puts_m` 2–10 (p50 5), `puts_m_ok` 1–9 (p50 4), `migrations_completed` 0–3 (p50 1), `migrated_objects` 0–9 (p50 1), `migrated_shards` 0–54 (p50 6).
  - Tier R (`realcluster_test.go`, `tierr_test.go`): `defaultRealOptions()` (9 nodes, 3/region, `4+2`) is used only for unrouted buckets (E1, E2, E3, E6, E8, E9). Every routed-bucket test on Tier R (`TestE4MigrationUnderTrafficR`, `TestE5RollbackKillTargetMidCopyR`, `TestE5RollbackKillCoordinatorMidSagaR`) already uses `routedRealOptions()` (9 nodes, 3/region, `2+1`, `3 >= K+M = 3`). No hole.
- **Test proving the repair:** `TestFuzzClusterNemesisCorpusMovesShards` (failed on the old 9-node `4+2` `fuzzOptions()` with `fault-free warm-up put to routed bucket m via n000 failed: ... storage: placement: 3 members for 6 shards: storage: unavailable`; passes after the fix).
- **Canary proving the test bites:** `TestCanaryFuzzRoutedBucketCannotPlaceShards` (restores `nodes=9`, `K=4, M=2`):
  - `warm-up fails immediately on unplaceable routed bucket`: matches `"fault-free warm-up put to routed bucket m"` and `"unavailable"`; caught `corpus[0] (seed 7347990519673328018): fault-free warm-up put to routed bucket m via n000 failed: remote error (code 99): storage: put m/warm00: stripe 0: storage: placement: 3 members for 6 shards: storage: unavailable: e2e: scenario check failed: e2e: scenario check failed`.
  - `corpus check catches zero acknowledged puts to routed bucket without warm-up` (`warmM=0`): matches `"acknowledged 0 traffic puts to routed bucket m"`; caught `fuzz seed corpus acknowledged 0 traffic puts to routed bucket m: e2e: scenario check failed`.
- **Numbers measured:**
  - Fuzz seed corpus (18 inputs): `warm_m_ok=36` (2 warm-up objects / 6 shards per run), `puts_m=25`, `puts_m_ok=19`, `migrations_completed=2`, `migrations_compensated=0`, `migrated_objects=5`, `migrated_shards=15` (2.5 objects and 7.5 shards moved per completed migration across the seed corpus).
  - Fuzz executions/sec before vs after (4 workers, `dev` container):
    - Raw (`-fuzztime 20s -fuzzminimizetime 0s`, fresh cache): **11.0 exec/s** before (227 execs in 20.6 s) vs **8.7 exec/s** after (178 execs in 20.5 s).
    - With minimization (`-fuzztime 30s -fuzzminimizetime 5s`, fresh cache): **1.37 exec/s** before (43 execs in 31.5 s, 17 new) vs **1.34 exec/s** after (43 execs in 32.1 s, 16 new).
    - Warm cached baseline (222 cached inputs): **7.66 exec/s** before (222 in 29 s) vs **5.55 exec/s** after (222 in 40 s).
    - Required 120 s run (`-fuzztime 120s -fuzzminimizetime 5s`): 287 execs in 121.2 s (2.37 exec/s overall, 50 new interesting inputs, total corpus 272, 0 failures).
    - Seed corpus only (`-run '^FuzzClusterNemesis$' -count=1`): 18 seeds in 4.71 s (3.82 exec/s).

### Finding 2 — timed-out migration start treated as "nothing started"

- **What was wrong:** `nemesis.finishMigration()` (`test/e2e/nemesis_test.go`) cleared `n.mig = nil` and returned `nil` on `env.ErrTimeout` or `env.ErrUnreachable` from `StartMigration`, ignoring the simulator contract that a timeout means outcome unknown. If `coord` applied `StartMigration` and only its reply was lost, the runner skipped `awaitMigration` and `shardsOutside`, failed to count the migration, left `n.region` stale (causing a subsequent `opMigrate` to fail with `node.ErrNoMigration`), and stopped protecting a live coordinator from `opWipeNode`.
- **What changed:**
  - `test/e2e/nemesis_test.go`: added `seenSagas`, `migTo`, `forgetAmbiguousMig`, `beforeCycle` to `nemesis` and `ambiguousRecovered`, `finalRegion` to `nemesisStats`. Before calling `StartMigration`, `opMigrate` records `coord.Engine().IDs()` into `n.seenSagas` and saves `n.migTo`. In `finishMigration()`: on `env.ErrTimeout` or `env.ErrUnreachable` (unless the canary switch `forgetAmbiguousMig` is set), `findNewSaga(coord)` inspects `coord.Engine().IDs()` (existing exported product API; no product change needed) for any saga ID not in `n.seenSagas`. When found, it marks the ID seen, increments `n.st.ambiguousRecovered`, and runs the exact same post-migration path as an acknowledged start: `awaitMigration` to terminal state, `c.Await(..., c.Converged)`, `refreshRegion()` from converged `rd.Route(bucketM)` asserting match with `wantTo` (completed) or `prevRegion` (compensated), `shardsOutside(c, bucketM, n.region) == 0`, and `bucketShardsIn` shard accounting. When no saga started, `liveBucketRegs(c, bucketM)` cross-checks converged OCC metadata for any stray live `~reg/` entry and `refreshRegion()` still refreshes `n.region`. `refreshRegion()` also runs when `n.mig == nil` or on `ErrBusy`/`ErrUnavailable`. `opWipeNode` now skips if `(n.mig != nil && n.migCoord.ID == string(id)) || n.hasActiveSaga(id)`, keeping any live coordinator protected while an outcome is unknown or a saga is non-terminal.
  - **`ErrBusy` / `ErrUnavailable` review:** kept as "certainly not started" (while still calling `refreshRegion()`). `Node.StartMigration` (`internal/node/objects.go:628-646`) never returns `storage.ErrBusy` (which occurs on `PutObject`/`DeleteObject` or later inside saga steps `Claim`/`Seal`), and `storage.ErrUnavailable` is returned only by `apiNode.OnRequest` when `a.n == nil` before `Node.StartMigration` is invoked (or on HTTP 503 for pre-`Begin` `ErrNotReady`/`ErrStopped`). Neither can be returned after `engine.Begin` commits `RecBegin`.
  - `test/e2e/e7_test.go`: added `ambiguousMigrationRun`, `verifyAmbiguousMigrationTracked`, `TestNemesisAmbiguousMigrationStartIsTracked`, and `e7/ambiguous_migrations_recovered` measure.
  - `test/e2e/canary_test.go`: added `TestCanaryNemesisForgottenAmbiguousMigrationFails`.
- **Test proving the repair:** `TestNemesisAmbiguousMigrationStartIsTracked` (fixed seed 702, under `simtest.RequireDeterministic`, using `sim.FaultPartitionOneWay` from storage nodes to client nodes in cycle 0 so `StartMigration` executes on `coord` while its reply is dropped and the caller receives `env.ErrTimeout`):
  - 1-cycle run: `recovered=1 migrated=1 shards=6 finalRegion=r1`.
  - 2-cycle run: `recovered=1 migrated=2 shards=18 finalRegion=r2`.
  - `dropped request timeout starts no saga and next cycle migrates from original region` (`FaultPartitionOneWay` clients → nodes): `ambiguousRecovered=0`, `migrated=1`, `finalRegion="r1"`.
  - `wipe targeting coordinator while start outcome is unknown is skipped`: `opWipeNode` targeting `n.migCoord` at 1 s is skipped (`incarnation == 1`, `skipped >= 1`, `recovered=1`, `migrated=1`, `finalRegion="r1"`).
- **Canary proving the test bites:** `TestCanaryNemesisForgottenAmbiguousMigrationFails` (`n.forgetAmbiguousMig = true`):
  - `2-cycle run fails with no migration when timed-out start is forgotten`: matches `"bucket cannot migrate"`; caught `start migration via n002: remote error (code 184): node: migrate "m" from "r1" to "r1": node: bucket cannot migrate: e2e: scenario check failed`.
  - `1-cycle tracking assertions catch forgotten ambiguous start`: matches `"ambiguous migration start was not recovered"`; caught `ambiguous migration start was not recovered: got 0 recovered, want 1: e2e: scenario check failed`.

### Finding 3 — `scenarioE2` never requires partition-time reads or writes to succeed

- **What was wrong:** `scenarioE2` (`test/e2e/scenario_e2_test.go`) accepted `ErrUnavailable` for every partition-time PUT and GET without requiring any surviving-side read or write to succeed; because tags and ACLs still succeeded on both sides, returning `ErrUnavailable` for every object PUT and GET while split still passed E2 on both tiers.
- **What changed:**
  - `test/e2e/scenario_e2_test.go`: added `sideCanPut(side, layout)` (`len(side) >= max(K+M, min(K+1, K+M))`) and `sideHasKShards(preShards, version, side, K)` (checks every stripe of a pre-cut version holds `>= K` distinct shard indices on `side`). Before `PartitionRegion`, `scenarioE2` collects pre-cut shard placement via `collectShards`. Once `splitViews` settles and `Sleep(p.detect)` finishes:
    1. Issues pre-cut GETs for every pre-cut key on both `sideA` and `sideB` before any overwrites: sides with `>= K` shards must succeed with the exact pre-cut SHA-256 and ETag; sides with `< K` shards must return `storage.ErrUnavailable`.
    2. For post-split PUTs (`pa`, `pb`, `pn`): sides with `sideCanPut == true` must succeed; sides with `sideCanPut == false` must return `storage.ErrUnavailable`.
    3. For post-split concurrent GETs: sides with `>= K` surviving shards must succeed; sides with `< K` shards and no overwrite on that side must return `storage.ErrUnavailable`.
    4. Asserts `postGetsRequired > 0 && getsOK > 0` and `postPutsRequired > 0 && putsAcked > 0`.
    Pre-split early calls (before `splitViews`) keep `allowedDuringPartition` / `allowedReadDuringPartition`.
  - `test/e2e/api_test.go`, `test/e2e/simcluster_test.go`: added test-only `apiIntercept func(n *node.Node, op byte) error` hook on `simCluster`/`apiNode` (nil in all normal runs).
  - `test/e2e/tiers_test.go`, `test/e2e/tierr_test.go`: added `post_puts_required`, `gets_ok`, `post_gets_required` measures.
  - `test/e2e/canary_test.go`: added `TestCanaryE2ObjectOpsUnavailableDuringSplitFails`.
- **Numbers measured per seed:**
  - Tier S (`TestE2PartitionAndHealS`, 13 seeds from `DST_SEEDS=50`, 21 nodes = 7+14, `K=4, M=2`): `post_puts_required=9` (4 `sideA`, 5 `sideB`), `puts_acked=9`, `puts_unavailable=6` (all pre-split); `post_gets_required=12` (6 pre-cut + 6 concurrent on `sideB`), `gets_ok=15` (12 post-split + 3 early `sideB`) on every seed (min=p50=max).
  - Tier R (`TestE2PartitionAndHealR`, 9 nodes = 3+6, `K=4, M=2`): `post_puts_required=5` (all 5 on `sideB`; `sideA`'s 4 post-split PUTs required and returned `ErrUnavailable` since `3 < 6`), `puts_acked=5`, `puts_unavailable=10` (6 early + 4 `sideA` post-split); `post_gets_required=12` (all 12 on `sideB`), `gets_ok=15` (12 post-split + 3 early `sideB`).
- **Canary proving the test bites:** `TestCanaryE2ObjectOpsUnavailableDuringSplitFails` (before the fix, all 3 subcases passed `scenarioE2` and failed the canary; after the fix, all 3 pass):
  - `put and get unavailable during split`: matches `"must succeed"`; caught `post-split get e2/k00 via n020 must succeed (side holds >= k=4 shards): remote error (code 99): storage: unavailable: e2e: scenario check failed`.
  - `only get unavailable during split`: matches `"get e2/"`; caught `post-split get e2/k00 via n020 must succeed (side holds >= k=4 shards): remote error (code 99): storage: unavailable: e2e: scenario check failed`.
  - `only put unavailable during split`: matches `"put e2/"`; caught `post-split put e2/k00 via n000 must succeed (side size 7 >= k+m=6): remote error (code 99): storage: unavailable: e2e: scenario check failed`.

### Failures found

None. Zero failing seeds, zero fuzz crashers, and zero suspected product defects.

### Commands run and results (stage 1)

All Go commands ran inside `docker compose run --rm -T dev ...`; `GATE_ONLY=G0` ran `agent_work/gates/gate.sh 07-e2e` which builds and runs inside the `dev` container.

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...` | clean |
| `go build ./...` | ok |
| `go test -count=1 ./test/e2e` | ok, 245.1 s |
| `go test -count=1 ./internal/node/... ./internal/nodetest/... ./internal/noderuntime/...` | ok (`node` 48.9 s, `nodetest` no test files, `noderuntime` 5.4 s) |
| `DST_SEEDS=50 go test -count=1 -v ./test/e2e -run 'TestE2\|TestE7\|TestCanary'` | ok, 89.5 s (13 E2 seeds, 31 E7 seeds, all 12 Tier S canaries) |
| `go test -v ./test/e2e -run '^FuzzClusterNemesis$' -count=1` | ok, 18 seeds, 4.71 s |
| `go test ./test/e2e -run '^$' -fuzz '^FuzzClusterNemesis$' -fuzztime 120s -fuzzminimizetime 5s` | PASS, 121.2 s, 287 execs, 50 new (total 272), 0 failures |
| `E2E_TIER_R=1 go test ./test/e2e -run 'TestE2.*R$' -count=1 -v -timeout 20m` | PASS, 51.85 s (`post_puts_required=5`, `post_gets_required=12`, `gets_ok=15`, `declared_lost=0`) |
| `go test -race -count=1 ./test/e2e -run 'TestE2\|TestE7\|TestCanary'` | ok, 124.5 s |
| `GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e` | overall=pass (tools, gofmt, vet, build, detlint, panics, deps, comments, selfcontained all pass) |

### Anything not done, and requests to the orchestrator

- **Not done (out of stage 1 scope):** REVIEW-1 Findings 4–8 (four nits and one note) are left for stage 2 as instructed.
- **Requests to the orchestrator:**
  1. Run the full gate (`agent_work/gates/gate.sh 07-e2e`) and proceed to Fable review, Opus mutation check, and stage 2 (Findings 4–8).
  2. Consider re-running the 15-minute hardening fuzz budget on `FuzzClusterNemesis` once stage 2 lands so the hardening archive reflects the repaired 12-node `2+1` non-empty migration setup.

## 12. Finish round, stage 2 (Neon): REVIEW-1 nits and note, recorded harness findings

Seat: Gemini 4 Neon (builder), branch `chunk/07-e2e`. All REVIEW-1 nits (Findings 4–7), the note (Finding 8), and both recorded adversarial harness findings (C7, C10) are closed with zero Go comments added (`GATE_ONLY=G0` `comments` check passes against baseline `d667977`).

**FLAG — product code changed outside `internal/noderuntime/e2efault.go`:**
Tightening `(*nemesis).settle` for recorded harness finding C7 turned up a real error-classification bug in `internal/storage` on E7 seed `1` (see Item E and "Failures found" below):
- `internal/storage/errors.go` (`CodeEntries`): moved `{Code: CodeUnavailable, Err: ErrUnavailable}` ahead of `CodeNotFound` and `CodeCorrupt` (wire code numbers unchanged: 99, 96, 97, 98, 100).
- `internal/storage/httpapi/http.go` (`StatusTable`): moved `{Err: storage.ErrBusy, Status: 503}` and `{Err: storage.ErrUnavailable, Status: 503}` ahead of `{Err: storage.ErrNotFound, Status: 404}` and `{Err: env.ErrNotFound, Status: 404}` (keeping `storage.ErrNoSuchBucket` and `occ.ErrNotFound` at 404).
- Tests proving the bug and fix: `TestCodeTableJoinedUnavailablePrecedesShardCauses` (`internal/storage/errors_test.go`) and `TestStatusTableJoinedUnavailablePrecedesShardCauses` (`internal/storage/httpapi/http_test.go`), both failed before the fix (`Encode` returned 96/97 instead of 99; `StatusOf` returned 404 instead of 503) and pass after.

### Item A (Finding 4) — E4 canary matches any failure

- **What was wrong:** `TestCanaryE4AMigrationThatSkipsDeletingTheSourceFails` (`test/e2e/canary_test.go`) passed `""` to `expectCanaryFailure`, accepting any failure of `scenarioE4`. `expectCanaryFailure` silently skipped substring matching whenever `want == ""`.
- **What changed:**
  - `test/e2e/canary_test.go`: extracted `checkCanaryFailure(err error, want string) error`, which refuses empty or whitespace-only `want`, refuses `err == nil`, and requires `strings.Contains(err.Error(), want)`. `expectCanaryFailure` delegates to it.
  - Updated `TestCanaryE4AMigrationThatSkipsDeletingTheSourceFails` to match `"no shard of the bucket left in r0"`.
  - Audited all 10 other `expectCanaryFailure` call sites in `test/e2e/canary_test.go`; all already passed non-empty specific strings, and `TestCanaryE5ACopyThatIsNeverCompensatedLeavesOrphans` was tightened from `"check failed"` to `"still holds a staged entry"`.
- **Test and self-test:** `TestExpectCanaryFailureRejectsEmptyMatch` proves `checkCanaryFailure` rejects `""`, `"   "`, `nil`, and unrelated errors while accepting matching errors. All Tier S canaries pass:
  - E4 caught: `no shard of the bucket left in r0: not within 40s: e2e: scenario check failed` (matched `"no shard of the bucket left in r0"`).
  - E5 caught: `sagasim: cell verify-fail-before-action-untorn: node n000 still holds a staged entry for o0: sagasim: check failed` (matched `"still holds a staged entry"`).

### Item B (Finding 5) — counters and traffic never required to be non-zero

- **Product contract for `Health.Discards` vs `RecentDiscards`:** In `internal/occ/store.go` (`Store.record`, `decodeState`) and `internal/node/objects.go` (`Node.Health`), each discard event observed on a node increments persisted `s.discards` (`Health.Discards`, saturating at `MaxUint64`) and appends to the bounded tail `s.tail` (`Health.RecentDiscards`, cap `occ.DiscardTailLen = 64`), persisted atomically together. Therefore on every up node: `uint64(len(h.RecentDiscards)) == min(h.Discards, uint64(occ.DiscardTailLen))`. Because one lost version may be observed by multiple nodes (over-count, never under-count), across the cluster `sum(h.Discards) >= uint64(res.contested.discarded) > 0` and `max(h.Discards) > 0`.
- **What changed (E2, E8, E9):**
  - `test/e2e/scenario_e2_test.go`: `discardLog` now returns `discardSummary{losers, most, total}`, refuses an empty `upNodes(c)`, and checks `uint64(len(h.RecentDiscards)) == min(h.Discards, uint64(occ.DiscardTailLen))` on every up node. `checkContests` requires `len(cs) > 0` and `res.acked > 0 && res.current > 0`. Both `scenarioE2` and `scenarioE8` require `res.discards > 0 && ds.total >= uint64(res.contested.discarded)`; `scenarioE2` also requires `p.objects >= 2` and `res.meta > 0`; `scenarioE8` also requires `p.objects > 0`, `res.conditional > 0`, `len(qs) > 0`, and `res.staleRefused >= p.objects`.
  - `test/e2e/tierr_test.go`: in `scenarioE9`, added atomic `active` and `preOK` counters around worker batches and hold the signal until `>= 2s` elapsed with `preOK >= 4 && active >= 2` (15 s bound, failing closed if traffic never establishes), so in-flight load is guaranteed at SIGTERM. Added `checkE9Traffic(sig, res)` requiring `res.ok > 0` and `res.inflightOK+res.inflightFailed > 0` on `SIGTERM`, plus `len(acked) > 0` before the post-restart readback loop.
- **Sweep of all other scenarios (Tier S and Tier R):**
  - **E1 (`scenario_e1_test.go`, S and R):** Closed empty-parameter / empty-spread holes: require `len(p.sizes) > 0`, `res.objects > 0`, `res.reads > 0`, `len(stripes) > 0` per version in `checkSpread`, and `res.stripes > 0` whenever non-empty objects exist.
  - **E3 (`scenario_e3_test.go`, S and R):** Closed vacuous-rot holes: require `p.damaged > 0 && p.objects >= 2*p.damaged`, `res.bucketBytes > 0`, `len(first) > 0`, `res.reads > 0`, `len(second) > 0 && res.damagedBytes > 0`, `res.rebuiltBytes > 0`, and (when `movedCounter` is implemented on Tier S) `res.movedBytes > 0`. (Before this fix, empty `second` gave `damagedBytes == 0`, making `rebuiltBytes` and `movedBytes` checks vacuously pass.)
  - **E4 (`scenario_e4_test.go`, S and R):** Closed empty-traffic / empty-probe holes: require `p.objects > 0`, `res.traffic > 0 && res.trafficOK > 0`, `res.objects > 0`, and inside `routedTo` require `len(probes) > 0` and `len(stripes) > 0` per probe.
  - **E5 (`e5sim_test.go` S, `tierr_test.go` `scenarioE5R` R):** Closed empty-table holes: require `len(e.data) > 0` in `e5Cluster.check`, `len(cells) > 0` and `len(mine) > 0` in `TestE5MigrationEveryFailurePointS`, and `res.killedAt >= 1` in `scenarioE5R`. Source-readback loop (`len(pre) == 24`), `routedTo`, and `shardsOutside` in `scenarioE5R` were checked and are non-empty.
  - **E6 (`scenario_e6_test.go`, S and R):** Closed vacuous-catchup / vacuous-refill / zero-observer holes: require `p.perWave > 0`, ensure `victim` holds at least 1 shard after wave 1 (writing extra objects if needed while keeping `victim = nodes[4]` for the E6 canary), require `observers > 0` in `rejoined` and `res.incAfter > res.incBefore`, require `res.victimShards > 0` on Tier S, pick a `wiped` node with `> 0` shards (preferring `nodes[8]`) and require `res.wipedShards > 0 && len(want) > 0`, and require `res.acked > 0 && res.reads > 0`.
  - **E7 (`nemesis_test.go`, `e7_test.go`):** Require `len(plan.cycles) > 0` and `n.st.puts+n.st.gets > 0` in `nemesis.run`. In `TestE7NemesisHealingCyclesS`, require per seed `st.cycles == e7Cycles`, `st.applied > 0`, `st.putsOK > 0`, and `st.getsOK > 0`. (`availabilityGets` is NOT forced `> 0` per single seed because a 4-cycle seed can legitimately draw a network fault in every cycle, as seed 290 did; `TestFuzzClusterNemesis` also permits minimal 1-cycle fuzz inputs with 0 faults.)
  - **`finalChecks` (`simoracle_test.go`, called by E2/E4/E6/E8 Tier S):** Require `oc.readable+oc.lost > 0` so `checkLiveReadableOrLost` cannot pass with zero live manifests.
  - **Tier R hygiene & default-build (`tierr_test.go`):** Judged fine: `TestTierRHarnessLeavesNothingBehindWhenATestFails` already asserts `len(rec.PIDs) != 3 || len(rec.Ports) != 6 || len(rec.Dirs) < 2`; `TestDefaultBuildCarriesNoFaultInjection` already loops over a non-empty literal slice of 3 markers and requires every marker present in the tagged binary before checking absence in the default binary.
- **Canary and self-test proving it bites:**
  - `TestCanaryE2AndE8UnrecordedDiscardCounterFails` (`test/e2e/canary_test.go`, using test-only `faultyDiscardHealthCluster` wrapper over `simCluster`): all 3 subcases failed on the old code (`the scenario passed with the planted fault on`) and pass after the repair:
    - `E2 discards logged but Health.Discards is zero` (matched `"Health.Discards=0"`): caught `node n000 reports Health.Discards=0 with 25 RecentDiscards (want 0): e2e: scenario check failed`.
    - `E8 discards logged but Health.Discards is zero` (matched `"Health.Discards=0"`): caught `node n000 reports Health.Discards=0 with 11 RecentDiscards (want 0): e2e: scenario check failed`.
    - `E2 discards logged but Health.Discards undercounts RecentDiscards` (matched `"Health.Discards=1"`): caught `node n000 reports Health.Discards=1 with 25 RecentDiscards (want 1): e2e: scenario check failed`.
  - `TestCheckE9TrafficRequiresLoadAndInflight` (`test/e2e/tierr_test.go`): 6 table cases proving `checkE9Traffic` rejects `ok == 0` (`"0 successful requests"`) and `inflightOK+inflightFailed == 0` (`"0 requests in flight"`) on `SIGTERM`, and accepts non-zero combinations.
  - Measured on Tier R `TestE9ShutdownUnderLoadR`: `shutdown=1.57s`, `in_flight_at_sigterm_finished=7`, `in_flight_at_sigterm_failed_cleanly=0`, `requests_ok=44`, `acked_read_after_restart=24`.

### Item C (Finding 6) — `s.Err()` vs `s.Verify()`, and `nemesisStats.declared`

- **Audit of every `.Err(` call in `test/e2e` and `internal/nodetest`:**
  - `internal/nodetest`: 0 matches.
  - `test/e2e` end-of-run checks that bypassed `simtest.RequireDeterministic` (all 3 switched to `s.Verify()`):
    1. `FuzzClusterNemesis` (`test/e2e/e7_test.go`)
    2. `checkFuzzCorpus` (`test/e2e/e7_test.go`)
    3. `verifyAmbiguousMigrationTracked` (`test/e2e/e7_test.go`)
  - Remaining `.Err(` calls in `test/e2e` are either mid-run/post-construction guards where `RequireDeterministic` calls `s.Verify()` at end of run (`harness_test.go:28`, `e5sim_test.go:43`, `e7_test.go:33`, `simcluster_test.go:160,200,313,320,327`), or canary failure checks (`canary_test.go:125,152,360`) expecting a planted failure error.
- **`nemesisStats.declared`:** Kept and populated (`n.st.declared = len(c.o.declared)` before the `len(c.o.declared) > 0` failure check and at end of `nemesis.run`) so `MEASURE e7/declared_lost` stays accurate both on passing runs (`0`) and if a loss is ever declared inside the budget.

### Item D (Finding 7) — `e2efault` blocklist reload misses equal-size rewrites

- **What was wrong:** `(*blocklist).reload` (`internal/noderuntime/e2efault.go`) cached `(fi.ModTime(), fi.Size())` from `os.Stat` and skipped `os.ReadFile` when both matched, missing rapid equal-length rewrites within one filesystem mtime tick; non-ENOENT read errors were also dropped silently.
- **What changed:**
  - `internal/noderuntime/e2efault.go` (`//go:build e2efault`): removed `mod`/`size` and `os.Stat`; `reload` calls `os.ReadFile(path)` directly on each 50 ms throttled check. `fs.ErrNotExist` sets an empty peer set with no error. Any other read error keeps the last good `b.peers` (initializing empty if nil) and surfaces the error via `blocklistLogf("noderuntime: e2efault: read %s: %v", ...)` (defaults to `log.Printf`, writing to stderr / `objnode`'s log file with no dropped error return).
- **Tests proving it bites and passes:**
  - `internal/noderuntime/e2efault_internal_test.go` (`//go:build e2efault`):
    - `TestBlocklistReloadSeesEqualSizeRewrite`: writes `"peer-a\n"`, then rewrites twice back to back with equal-length `"peer-b\n"` and `"peer-c\n"` preserving identical mtime via `os.Chtimes`. **Failed on old code** (`equal-size rewrite 1 (peer-a -> peer-b with identical mtime) was missed`); passes after repair.
    - `TestBlocklistReloadMissingClearsAndReadErrorKeepsLastGood`: proves missing file clears the set with no logged error, and replacing the file with a directory (`EISDIR`) preserves `"peer-a"` in the peer set and surfaces the error via `blocklistLogf`.
  - `TestDefaultBuildCarriesNoFaultInjection` passes, confirming the default `objnode` binary remains free of `.e2e-blocklist`, `noderuntime.blockedPeer`, and `noderuntime.BlocklistPath`.

### Item E (Recorded harness finding C7) — `(*nemesis).settle` only checked PUT errors

- **What was wrong:** `(*nemesis).settle` (`test/e2e/nemesis_test.go`) validated terminal errors only for `"put"`. Ordinary `"get"`, `"delete"`, `"tag"`, and unknown ops accepted any error (including `storage.ErrCorrupt` or arbitrary unknown errors) without failing.
- **Allowed terminal error sets per operation (from PLAN 2.6 and `internal/node/objects.go` / `internal/occ/store.go`):**
  - Common transport/node-lifecycle set (all 4 ops): `env.ErrTimeout`, `env.ErrUnreachable`, `occ.ErrNotReady`, `node.ErrNotReady`, `storage.ErrNoSuchBucket` (node wiped and not yet synced bucket record). `storage.ErrCorrupt` is explicitly forbidden for every op.
  - `"put"` (unconditional `PutObject`, no quotas): common set + `storage.ErrUnavailable` + (`storage.ErrBusy` only when `k.Bucket == bucketM`).
  - `"get"` (`GetObject` on `bucketW`): common set + `storage.ErrUnavailable` + `occ.ErrNotFound`. Availability gets (`k.Size == -1`) additionally require `nil` or `occ.ErrNotFound` when `!n.netFault && !n.touched[k.At] && !n.dirty[k.Key]`.
  - `"tag"` (unconditional `PutTag` on `bucketW`, tag `"t"`): common set + `occ.ErrNotFound` (key missing/deleted or wiped node not yet synced manifest).
  - `"delete"` (unconditional `DeleteObject` on `bucketW`): common set + `occ.ErrNotFound` (`occ.Store.Delete` returns `occ.ErrNotFound` when key is not live) + (`storage.ErrBusy` only when `k.Bucket == bucketM`).
  - Any other `k.Op`: always rejected.
- **What changed:**
  - `test/e2e/nemesis_test.go`: added `allowedNemesisTransportError`, `allowedNemesisError`, validated every op kind (and unknown ops) in `(*nemesis).settle`. Also fixed pre-existing availability-check slip in `settle()` by re-checking `!n.netFault && !n.touched[env.NodeID(k.At.ID)]` (see "Failures found").
  - `internal/storage/errors.go`, `internal/storage/httpapi/http.go`: fixed `ErrUnavailable` precedence over per-shard `ErrNotFound`/`ErrCorrupt` (see top flag and "Failures found").
  - `test/e2e/testdata/seeds.txt`: pinned E7 seed `1`.
- **Self-tests proving it bites:** `TestNemesisSettleValidatesTerminalErrorsForEveryOp` (`test/e2e/oracle_self_test.go`, 39 subtests): before the repair, `put` to `bucketW` with `ErrBusy`, all disallowed `get`/`tag`/`delete` cases (`ErrCorrupt`, unknown error, `ErrInvalid`, `ErrUnavailable` on tag/delete, `storage.ErrNotFound` on get/delete, `ErrPreconditionFailed`), and unknown op `"bogus"` all failed the self-test; after the repair all 39 subtests pass.

### Item F (Recorded harness finding C10) — `newRealCluster` accepts a failed node launch and can collide ports

- **What was wrong:** `newRealCluster` (`test/e2e/realcluster_test.go`) allocated ports via repeated `freePort()` calls that closed each listener before allocating the next (allowing the OS to hand the same port to two nodes in one cluster), and waited only on `c.Converged()`, which filters `upNodes(c)` — so an `objnode` process that exited on bind failure was ignored and `newRealCluster` returned a shrunken cluster.
- **What changed:**
  - `test/e2e/realcluster_test.go`:
    - Replaced `freePort` with `allocDistinctPorts(n int) ([]int, error)`, which holds all `n` TCP listeners and UDP sockets open simultaneously until all `n` distinct ports are reserved, so one cluster can never allocate the same port twice.
    - External-process collision handling: if another process on the host takes a port between reservation release and `objnode` bind (TOCTOU), `objnode` exits with `bind: address already in use`; `awaitStartup` detects the exited process on the next 100 ms poll, fails closed immediately with an error naming the node ID, region, exit status, and its log tail, and `startRealCluster`'s deferred cleanup kills every launched process (waiting on `<-p.exited`) and removes the cluster temp dir so zero stray processes or files remain.
    - Extracted `startRealCluster(t, seed, o) (*realCluster, error)` and `awaitStartup` / `allStartedAndConverged`: requires all `len(c.procs)` intended processes running, each HTTP endpoint's `/_health` returning `Ready == true` with `h.Node == p.node.ID` (identity verified), identical `MetaDigest`/`MetaRoot` across all `N` nodes, and all `N` nodes reported alive in `/_cluster/members`.
    - Also lowered default `realPutSlots` / `realBounds.writeBatch` from 4 to 1 (keeping `putSlots: 4` for E9) to prevent 4× concurrent fsync bursts from tripping `objnode`'s 1 s `RequestTimeout` on the shared 4-core VM during Tier R E2/E3.
- **Tests proving it bites:**
  - `TestAllocDistinctPortsNeverCollides` (`test/e2e/tierr_test.go`): allocates 40 distinct ports and verifies uniqueness.
  - `TestTierRStartupFailsOnPlantedLaunchFailure` (`test/e2e/tierr_test.go`, Tier R):
    - `wrapper binary exits non-zero for one node` (exits 73 for `r2`, execs real race-built `objnode` for `r0`/`r1`, matching C10's reproduction): caught `node 127.0.0.1:45919 (r2) exited with status 73 during startup; log tail: planted launch failure for region r2: e2e: scenario check failed`, verified all 3 PIDs return `ESRCH` and cluster dir removed.
    - `occupied port causes bind failure surfaced with node and log tail` (real TCP listener holding node 2's HTTP port): caught `node 127.0.0.1:37701 (r2) exited with status 1 during startup; log tail: ... bind: address already in use ...`, verified all 3 PIDs return `ESRCH` and cluster dir removed.

### Item G (Finding 8) — Note items

- **E4 Tier S object sizes and seed share:**
  - `test/e2e/scenario_e4_test.go`, `test/e2e/tiers_test.go`: added optional `multiStripeSize` and `trafficSize` to `e4Params`; changed Tier S `TestE4MigrationUnderTrafficS` from `objects: 8, size: 2*simStripe+5` (8 × 128 KiB initial + ~100 × 64 KiB traffic writes) at share `1/20` to `objects: 4, size: 1024, multiStripeSize: simStripe+5, trafficSize: 512` (1 two-stripe 64 KiB+5 B object + 3 one-stripe 1 KiB objects + 512 B traffic writes, still exercising multi-stripe shard copy, Merkle verify, and source purge on every seed) at share **`1/10`**. Tier R E4 and all other scenarios' seed shares are unchanged.
  - **Before vs after measured:**
    - Before (`1/20`): 9 seeds in 47.47 s wall = **5.27 s/seed wall**; at `DST_SEEDS=500`: 25 unpinned seeds (~142 s wall for E4; 1494 s package wall in initial build).
    - After (`1/10`): 10 seeds in 22.25 s wall = **2.22 s/seed wall** (2.37× faster); at `DST_SEEDS=500`: **50 unpinned seeds (52 total with 2 pinned seeds)** (~115 s wall for E4, 2× E4 seed coverage at lower E4 wall time).
    - Full package `DST_SEEDS=500 go test -count=1 ./test/e2e -timeout 60m`: **1629.78 s** (~27.2 min; +135 s vs initial 1494 s because pinning seed `1` in `testdata/seeds.txt` added 78 extra E5 cell runs — 280 vs 202 — plus the stage 1 and stage 2 tests/canaries).
- **Default `go test ./test/e2e` seed count:** At default `go test ./test/e2e` (`DefaultSeeds = 3`), `seedsFor` runs **1 unpinned seed** per scenario (plus all pinned seeds in `testdata/seeds.txt`: currently 2, `166963932986536` and `1`); no change made.
- **Limits — E7 lone-holder fault-budget narrowing (Chunk 4 citation):** The E7 wipe-budget rule (`nemesis.metadataElsewhere`, section 6 and 8) matches the exact durability limit already stated in Chunk 4 (`docs/knowledge/chunk-04-occ.md:27-28`: *"The one unrecorded loss: a version whose every holder, its writer included, lost its disk before passing it on. No node ever saw it lose."*), because `internal/node` acknowledges local-first metadata/manifest writes after a single local fsync.
- **ACL and quota `CommitSink` coverage:** No change made; Chunk 6's `PutACL` and `SetQuota` do not invoke `n.committed`, and ACL discards / quota convergence remain verified independently via `Health.RecentDiscards`, `Health.Discards`, and all-node quota queries in E2 and E8.

### Note on stage 1 (observed while replaying seed 102)

In `test/e2e/e7_test.go:62-64` (committed in stage 1), `TestE7NemesisHealingCyclesS`'s `t.Cleanup` fails when `totMig > 0 && totPutsMOK > 0 && totMigShards == 0`. On a multi-seed sweep this holds, but on a single-seed `-seed=102` replay it produced a false failure: seed `102` completed 1 migration of `m` in an early cycle while `m` was still empty (`warmM == 0` in E7) and acknowledged its 1 `puts_m_ok` in a later cycle after the migration. Left untouched per brief ("do not undo stage 1"); a follow-up could guard that sweep check on `seedsRun > 1` or track per-migration shard counts only when `m` already held an object before `opMigrate`.

### Failures found

1. **Product defect — `storage.ErrUnavailable` misclassified as `CodeNotFound` (96) / HTTP 404 (and `CodeCorrupt` 97) when joined with per-shard causes:**
   - **Found by:** stricter C7 `(*nemesis).settle` on E7 seed `1` (`TestE7NemesisHealingCyclesS/shard=1/seed=1`).
   - **Pinned:** `1` appended to `test/e2e/testdata/seeds.txt`.
   - **Replay command:** `go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$/^shard=1$/^seed=1$' -seed=1 -count=1`.
   - **Error observed:** `get w/k15 via n017: disallowed error: remote error (code 96): storage: get w/k15: stripe 0: 3 of 6 shards verified, need 4: storage: unavailable / shard 3 on n008 attempt 1: remote error (code 96): storage: read shard shards/w/f5/de7dd0a497155148/0.3: storage: not`.
   - **Diagnosis & fix:** `storage.Node.Get` (`internal/storage/node.go:514`) and `storage.DecodeStripe` (`internal/storage/stripe.go:130`) join `storage.ErrUnavailable` with per-shard causes (`st.causes`, which may include `storage.ErrNotFound` from a wiped holder or `storage.ErrCorrupt` from a bit-rotted shard). `storage.CodeEntries()` listed `CodeNotFound` (96) and `CodeCorrupt` (97) before `CodeUnavailable` (99), so `env.CodeTable.Encode` put code 96 or 97 on the wire instead of 99. The same precedence bug existed in `httpapi.StatusTable()` (`internal/storage/httpapi/http.go:94,96` listed `storage.ErrNotFound`/`env.ErrNotFound` at 404 before `storage.ErrUnavailable` at 503). Fixed in `f70e2c2` with unit tests `TestCodeTableJoinedUnavailablePrecedesShardCauses` and `TestStatusTableJoinedUnavailablePrecedesShardCauses`; seed `1` and all 301 E7 seeds in `DST_SEEDS=500` pass.
2. **Test harness bug — `(*nemesis).settle` availability check did not re-check `n.netFault` or `n.touched`:**
   - **Found by:** `DST_SEEDS=500` sweep on E7 seeds `102` and `290` (`availability within the fault budget: get k01/k00 via untouched n012 failed with no network fault in the cycle: ... timeout`).
   - **Replay commands:** `go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$' -seed=102 -count=1` and `-seed=290 -count=1`.
   - **Diagnosis & fix:** `nemesis.traffic()` scheduled an availability GET (`Size: -1`) early in a cycle while `n.netFault == false` and `n012` was untouched, then a later tick in the same cycle applied a network fault or touched `n012`. `settle()` re-checked `!n.dirty[k.Key]` but forgot `!n.netFault && !n.touched[k.At]`. Fixed in `d32405c` with self-test subcases in `TestNemesisSettleValidatesTerminalErrorsForEveryOp`; seeds `102`, `290`, and all 301 E7 seeds in `DST_SEEDS=500` pass.
3. **Tier R load limit — `realPutSlots = 4` triggered 1 s shard-write timeouts under load on E2/E3:**
   - **Found by:** full Tier R run (`TestE2PartitionAndHealR` and `TestE3BitRotRepairR` returned HTTP 503 `4 of 6` / `3 of 6 shards durable, need 5` with `1s: timeout` under 4-core VM load ~2.9).
   - **Diagnosis & fix:** Matches the documented 1 s `RequestTimeout` operating limit (section 6). Lowered default `realPutSlots` and `realBounds.writeBatch` from 4 to 1 (keeping `putSlots: 4` on E9) in `996b289`; full Tier R suite (13 tests) passed in 241.93 s.

### Commands run and results (stage 2)

All Go commands ran inside `docker compose run --rm -T dev ...`; `GATE_ONLY=G0` ran `agent_work/gates/gate.sh 07-e2e` inside the `dev` container.

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...` | clean |
| `go vet -tags e2efault ./...` | clean |
| `go build ./...` | ok |
| `go test -count=1 ./test/e2e ./internal/storage/... ./internal/node/... ./internal/nodetest/... ./internal/noderuntime/... ./cmd/objnode/...` | ok (`e2e` 336.1 s, `storage` 4.4 s, `httpapi` 1.9 s, `node` 88.4 s, `nodetest` no test files, `noderuntime` 8.6 s, `objnode` 2.9 s) |
| `go test -count=1 -tags e2efault ./internal/noderuntime` | ok, 7.87 s (including `TestBlocklistReloadSeesEqualSizeRewrite` and `TestBlocklistReloadMissingClearsAndReadErrorKeepsLastGood`) |
| `DST_SEEDS=500 go test -count=1 ./test/e2e -timeout 60m` | ok, 1629.78 s (27.2 min; on final Go commit `541384c`) |
| `go test ./test/e2e -run '^$' -fuzz '^FuzzClusterNemesis$' -fuzztime 120s -fuzzminimizetime 5s` | PASS, 121.46 s, 332 execs, 44 new (total 316), 0 failures |
| `E2E_TIER_R=1 go test ./test/e2e -run 'R$\|TierR\|DefaultBuild\|CanaryE9' -count=1 -v -timeout 40m` | PASS, 13 tests (including `TestTierRStartupFailsOnPlantedLaunchFailure`), 241.93 s |
| `go test -race -count=1 ./test/e2e ./internal/noderuntime ./cmd/objnode ./internal/storage/...` | ok (`e2e` 430.5 s, `noderuntime` 9.8 s, `objnode` 3.2 s, `storage` 11.9 s, `httpapi` 3.0 s) |
| `GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e` | overall=pass (`tools`, `gofmt`, `vet`, `build`, `detlint`, `panics`, `deps`, `comments`, `selfcontained` all pass) |

### Anything not done, and requests to the orchestrator

- **Not done:** Full gate `agent_work/gates/gate.sh 07-e2e` (left to orchestrator per brief).
- **Requests to the orchestrator:**
  1. Review the small product fix in `f70e2c2` (`internal/storage/errors.go`, `internal/storage/httpapi/http.go` — `ErrUnavailable` precedence over per-shard `ErrNotFound`/`ErrCorrupt`), found by C7 on E7 seed `1`.
  2. Decide whether PLAN 2.9's wording changes for per-scenario seed shares (E4 now `1/10` = 50 unpinned seeds at `DST_SEEDS=500`, other shares unchanged).
  3. Consider guarding stage 1's single-seed E7 cleanup check (`test/e2e/e7_test.go:62`) so single-seed replays like `-seed=102` do not false-fail when a migration runs before the first `puts_m_ok`.
  4. Run the full gate, Fable review, and Opus mutation check.

## 13. Finish round, fix round (Neon): FABLE-REVIEW findings

Seat: Gemini 4 Neon (builder), branch `chunk/07-e2e`. Product code (`internal/` non-test files, `cmd/`): **no changes**. All six findings (1 major, 2 minors, 3 nits) and Note B of `agent_work/07-e2e/FABLE-REVIEW.md` are closed with zero Go comments added (`GATE_ONLY=G0` `comments` check passes against baseline `d667977`).

### Finding 1 (major) — exact per-migration E7 migrated-shards check (`00e269e`)

- **What changed:**
  - `test/e2e/nemesis_test.go`: added `bucketLiveKeys(c, coord, bucketM)` to snapshot the sorted live non-empty object keys of `bucketM` on `coord` right before `opMigrate` calls `c.StartMigration` (pure passive observation; does not alter any PRNG draw, event, or schedule of E7 seeds). Extended `bucketShardsIn` to also return per-key verified shard counts in `n.region`. In `finishMigration()` when `mi.State == "completed"`, every pre-existing key in `n.migPreKeys` must now hold at least `c.cfg.Layout.K` verified shards on disks in `n.region` (and increments `movedObjects` / `movedShards`); with `n.warmM > 0`, also requires `len(preKeys) >= n.warmM && shards > 0`.
  - `test/e2e/e7_test.go`: removed the faulty statistical `totMig > 0 && totPutsMOK > 0 && totMigShards == 0` check from `TestE7NemesisHealingCyclesS`'s `t.Cleanup`, guarded sweep-only totals (`totPutsM >= 4 && totPutsMOK == 0` on `seedsRun >= 10`, `totMovedObjs == 0` on `seedsRun >= 20`) so single-seed replays cannot false-fail, and added `e7/moved_objects` and `e7/moved_shards` measures.
  - `test/e2e/canary_test.go`: added kept E7 canary `TestCanaryE7CompletedMigrationMissingPreExistingShardsFails` on `e7Options()` (21 nodes, 4+2, `warmM == 0`, seed 2) using a `dropBucketOnCopyStep` wrapper on `saga.StepCopy` that tombstones `bucketM`'s live objects via `nd.DeleteObject` before copying so `StepVerify`, `StepFlip`, `StepPromote`, `shardsOutside == 0`, `watchDisk`, `Lineage`, `checkLiveReadableOrLost`, and `checkOrphans` all pass, leaving only the exact per-migration check to catch the missing pre-existing objects.
- **Tests, canary, and pinned-seed checks:**
  - Single-seed reproduction (`go test -v ./test/e2e -run '^TestE7NemesisHealingCyclesS$' -seed=102 -count=1`): **PASS** (4.44 s; `migrations_completed=1`, `puts_m_ok=1`, `moved_objects=0`, `dest_objects=0`).
  - Kept E7 canary (`TestCanaryE7CompletedMigrationMissingPreExistingShardsFails`): **PASS** (0.84 s), matched `"left pre-existing object"`; caught `completed migration n012-6b1b68927-be0c7133 of bucket m left pre-existing object m001 with 0 verified shards in r1 (need >= 4): e2e: scenario check failed`.
  - Pinned seed `166963932986536`: `TestCanaryE7WipingTheOnlyCopyOfAnAcknowledgedWriteIsCaught` **PASS** (2.11 s), matched `"on no discard record"`; caught `acknowledged version d332c2c4 of "\x01\x01w\x03k06\x01t" (written on n000, stamp 45331239499.0@n000) is not current, not superseded by a later acknowledged write and on no discard record: e2e: scenario check failed`.
  - Pinned seed `1` with `f70e2c2` undone in a `/tmp/opencode/scratch-mut` copy (`git show f70e2c2~1` for `internal/storage/errors.go` and `internal/storage/httpapi/http.go`): **KILLED** (`E7_RC=1`, 3.94 s) with `get w/k15 via n017: disallowed error: remote error (code 96): storage: get w/k15: stripe 0: 3 of 6 shards verified, need 4: storage: unavailable / shard 3 on n008 attempt 1: remote error (code 96): storage: read shard shards/w/f5/de7dd0a497155148/0.3: storage: not: e2e: scenario check failed`.
- **Numbers (`DST_SEEDS=500`, 301 E7 seeds):** `e7/moved_objects n=301 min=0 p50=1 p99=9 max=15`, `e7/moved_shards n=301 min=0 p50=6 p99=54 max=90`.

### Finding 2 (minor) — remove redundant `hasActiveSaga` wipe guard and replace subtest (`96db03e`)

- **What changed and why:**
  - `test/e2e/nemesis_test.go`: removed `|| n.hasActiveSaga(id)` from `opWipeNode` and deleted the unused `hasActiveSaga` helper. Reasoning: `opMigrate` is the sole place `nemesis` starts a migration; it sets `n.mig` immediately and `n.mig` is never cleared until `finishMigration()` finishes after `FaultHealAll` + `c.Converged()`. During the entire chaos phase of a cycle (including if `StartMigration` times out early), `n.mig != nil && n.migCoord.ID == string(id)` already skips wiping the coordinator. Inside `finishMigration()`, every acknowledged or timeout-recovered saga on `coord` is awaited via `awaitMigration` to a terminal state (`completed` or `compensated`) before `defer` clears `n.mig` (and if no saga started, `liveBucketRegs` verifies no live registration exists). Therefore zero non-terminal sagas can exist on any node whenever `n.mig == nil`.
  - `test/e2e/e7_test.go`: replaced the subtest that passed without `hasActiveSaga` with `ambiguous start finishes to terminal state before next cycle can wipe coordinator` (`slowAmbiguousMigrationRun`): wraps `saga.StepCopy` with a 25 s virtual-time delay (`> simCallTimeout = 20s`, `SagaPolicy.StepTimeout = 35s`), applies `FaultPartitionOneWay` nodes → clients in cycle 0 so `StartMigration` times out while the saga is still running in `StepCopy`, asserts at `beforeCycle(n, 1)` that `n.mig == nil` and every saga on every node is `Terminal()`, and then wipes `coord` in cycle 1.
  - `test/e2e/canary_test.go`: added subcase `forgetting slow ambiguous start leaves non-terminal saga into next cycle` to `TestCanaryNemesisForgottenAmbiguousMigrationFails` (`forget = true`).
- **Test and canary proving it bites:**
  - `TestNemesisAmbiguousMigrationStartIsTracked/ambiguous_start_finishes_to_terminal_state_before_next_cycle_can_wipe_coordinator`: **PASS** (1.20 s; `recovered=1`, `migrated=1`, `finalRegion="r1"`, `applied >= 2`).
  - `TestCanaryNemesisForgottenAmbiguousMigrationFails/forgetting_slow_ambiguous_start_leaves_non-terminal_saga_into_next_cycle`: **PASS** (0.32 s), matched `"still holds non-terminal saga"`; caught `coordinator n008 still holds non-terminal saga n008-3f8935f85-4db95f91 at start of cycle 1: e2e: scenario check failed`.

### Finding 3 (minor) — narrow availability-get voiding by timestamp and add sweep floor (`3911ed0`)

- **What changed:**
  - `test/e2e/nemesis_test.go`: `Call.Ended` is already recorded in virtual time on every simulated call (`simcluster_test.go:232`). Added `netFaultAt`, `touchedAt`, and `dirtyAt` timestamps via `markNetFault()`, `markTouched(id)`, and `markDirty(key)` (reset per cycle), plus `availabilityReadClean(k)` and `keyDirtyFor(k)`. An availability GET (`k.Size == -1`) is voided only if `n.netFault && k.Ended >= n.netFaultAt` or `n.touched[id] && k.Ended >= n.touchedAt[id]` (and only excused as dirty if `n.dirty[k.Key] && k.Ended >= n.dirtyAt[k.Key]`). Reads that already finished strictly before the fault remain checked and counted.
  - `test/e2e/e7_test.go`: added `checkE7AvailabilityFloor(seedsRun, totAvailGets)` (`e7AvailFloorMinSeeds = 10`, `e7AvailFloorPerSeed = 10`) called from `TestE7NemesisHealingCyclesS`'s `t.Cleanup` (cannot false-fail on a single-seed run; fails a sweep if average availability gets drop below 10/seed, which would catch both 0 checks and the `d32405c` regression). Added unit test `TestE7AvailabilityFloorSafeForSingleSeed`.
  - `test/e2e/oracle_self_test.go`: replaced the coarse availability subtest with exact timestamp boundary cases (`Ended < netFaultAt` fails/counts, `Ended >= netFaultAt` voided and not counted; same for `touchedAt` and `dirtyAt`).
- **Measured `e7/availability_gets` across the `DST_SEEDS=500` E7 sweep:**
  - At `d32405c~1` (scratch copy, 546.74 s, EXIT=1 — seeds 102 and 290 failed): `n=299 min=6 p50=20 p99=41 max=42`.
  - At `d32405c` (scratch copy, 563.98 s, EXIT=0): `n=301 min=0 p50=7 p99=27 max=38` (seed 290 had `0`, seed 102 had `11`).
  - After exact narrowing (`3911ed0`, 628.11 s, EXIT=0; and final verification sweep): `n=301 min=6 p50=20 p99=40 max=42` (seed 102: `21`, seed 290: `16` — full p50=20 and min=6 recovered with zero false failures).
- **Tests proving it bites:** `TestNemesisSettleValidatesTerminalErrorsForEveryOp/availability_get_voids_only_reads_not_yet_complete_when_fault_or_node_touch_occurs` (PASS) and `TestE7AvailabilityFloorSafeForSingleSeed` (6 subtests, PASS).

### Finding 4 (nit) — require E2 early partition reads when side holds `k` shards (`78edab5`)

- **What changed:**
  - `test/e2e/scenario_e2_test.go`, `tiers_test.go`, `tierr_test.go`: checked each `earlyGet` against `sideHasKShards(preShards, preObjs[i].Info.Version, side, l.K)` and required `eg.call.Err == nil` whenever the side holds `>= k` shards (failing with `"early partition get %s/%s via %s must succeed (side holds >= k=%d shards): %v"`). Recorded `e2/early_gets_required` and `e2r/early_gets_required`.
- **Verification before keeping the stricter rule:**
  - `DST_SEEDS=500 go test -count=1 -v ./test/e2e -run 'TestE2'`: **PASS** (116.28 s, 126/126 seeds, `e2/early_gets_required n=126 min=3 p50=3 p99=3 max=3`, `e2/gets_ok n=126 min=15 p50=15 p99=15 max=15`).
  - `E2E_TIER_R=1 go test -v ./test/e2e -run 'TestE2.*R$' -count=3 -timeout 30m`: **PASS** (3/3 runs, 157.92 s: 53.54 s, 52.51 s, 51.83 s; `e2r/early_gets_required=3`, `e2r/post_gets_required=12`, `e2r/gets_ok=15..16` on all three).
  - `TestCanaryE2*`: **PASS** (2.94 s).

### Finding 5 (nit) — clarify names and simplify `sideCanPut` (`bf05175`)

- **What changed:**
  - `test/e2e/scenario_e2_test.go`: simplified `sideCanPut` to `return len(side) >= l.K+l.M`.
  - `test/e2e/nemesis_test.go`, `test/e2e/e7_test.go`: renamed `migratedObjects`/`migratedShards` and `e7/migrated_*` / `fuzz_corpus/migrated_*` to `destObjects`/`destShards` and `e7/dest_*` / `fuzz_corpus/dest_*`, and updated `TestNemesisAmbiguousMigrationStartIsTracked` / `verifyAmbiguousMigrationTracked` to assert against `movedShards`.
- **Definitions (for the chapter):**
  - `dest_objects` / `dest_shards`: live objects and verified shards of bucket `m` present in the destination region after a completed migration (includes objects written there after the route flipped).
  - `moved_objects` / `moved_shards`: strictly the pre-existing objects that were live on the coordinator when `opMigrate` started, and their verified shards in the destination region after the migration completes.
- **Measured on fuzz seed corpus (18 inputs):** `dest_objects=5`, `dest_shards=15`; `moved_objects=4`, `moved_shards=12`. On `DST_SEEDS=500` E7 (301 seeds): `dest_objects min=0 p50=1 max=19`, `dest_shards min=0 p50=6 max=114`; `moved_objects min=0 p50=1 max=15`, `moved_shards min=0 p50=6 max=90`.

### Finding 6 (nit) — full shard-cause precedence table tests (`4de93e2`)

- **What changed:**
  - `internal/node/codes_test.go`: added `TestJoinedUnavailablePrecedesEveryShardCause` against the merged `node.CodeTable()` covering all eight shard-request causes (`storage.ErrNotFound`, `storage.ErrCorrupt`, `env.ErrNotFound`, `env.ErrTimeout`, `env.ErrUnreachable`, `storage.ErrBusy`, `storage.ErrExists`, `storage.ErrInvalid`) in both `errors.Join` orders, all combined, and standalone (25 subtests).
  - `internal/storage/errors_test.go`: expanded `TestCodeTableJoinedUnavailablePrecedesShardCauses` with all eight causes (13 subtests).
  - `internal/storage/httpapi/http_test.go`: expanded `TestStatusTableJoinedUnavailablePrecedesShardCauses` with all eight causes in both join orders, all combined, and standalone (18 subtests).
- **Mutation proving it bites (`/tmp/opencode/scratch-mut` with `f70e2c2` undone):**
  - `TestJoinedUnavailablePrecedesEveryShardCause`: **FAIL** (`Encode(Join(ErrUnavailable, storage.ErrNotFound)) = 96, want 99`; `storage.ErrCorrupt` = 97; `storage.ErrExists` = 98; `all` = 96).
  - `TestStatusTableJoinedUnavailablePrecedesShardCauses`: **FAIL** (`StatusOf(Join(ErrUnavailable, storage.ErrNotFound)) = 404, want 503`; `env.ErrNotFound` = 404; `storage.ErrExists` = 409; `storage.ErrInvalid` = 400; `all` = 404).

### Note B (`2d62a51`)

- Added one paragraph to section 8 documenting that tier R write concurrency outside E9 is capped at 1 in-flight PUT per cluster (`realPutSlots` / `realBounds.writeBatch` = 1) due to the fixed 1 s shard `RequestTimeout` under `-race` on 4 cores, and pointing to request 5 of section 9 (configurable timeout flag).

### Commands run and results (fix round, final code `2d62a51`)

All Go commands ran one at a time inside `docker compose run --rm -T dev ...`; `GATE_ONLY=G0` ran inside the `dev` container via `agent_work/gates/gate.sh 07-e2e` and `agent_work/07-e2e/gates` was restored afterwards.

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...` | clean |
| `go vet -tags e2efault ./...` | clean |
| `go build ./...` | ok |
| `go test -count=1 ./test/e2e ./internal/storage/... ./internal/node/...` | ok (`test/e2e` 380.70 s, `storage` 3.01 s, `httpapi` 1.85 s, `node` 83.88 s) |
| `DST_SEEDS=500 go test -count=1 ./test/e2e -run 'TestE2\|TestE7\|TestCanary\|Nemesis\|Fuzz' -timeout 60m` | ok, 862.54 s (E7 301 seeds, E2 126 seeds, all 13 Tier S canaries, fuzz corpus) |
| `go test ./test/e2e -run '^$' -fuzz '^FuzzClusterNemesis$' -fuzztime 120s -fuzzminimizetime 5s` | PASS, 121.86 s, 384 execs, 32 new (total 348), 0 failures |
| `go test -race -count=1 ./test/e2e -run 'TestE2\|TestE7\|TestCanary\|Nemesis'` | ok, 230.43 s |
| `E2E_TIER_R=1 go test ./test/e2e -run 'TestE2.*R$' -count=3 -timeout 30m` | ok, 3/3 pass, 157.92 s (on `78edab5`, identical E2 code except `sideCanPut` simplification `max(K+M, min(K+1, K+M))` → `K+M`) |
| `GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e` | overall=pass (`tools`, `gofmt`, `vet`, `build`, `detlint`, `panics`, `deps`, `comments`, `selfcontained` all pass) |

### Anything not done

- Full gate `agent_work/gates/gate.sh 07-e2e` is left to the orchestrator per brief.
- Zero findings disputed; all six findings and Note B are closed.

## 14. Short mode for the fast lane (Neon)

Seat: Gemini 4 Neon (builder), branch `chunk/07-e2e`, code commit `a6a009b`. Only `test/e2e/e5sim_test.go` changed (zero Go comments added; no product code, seed shares, or other tests touched).

- **What changed (`test/e2e/e5sim_test.go`):**
  - Added pure selector `e5CellSeeds(cell, cells int, short, race, explicitSeed, fullSeeds bool, seeds, pinned []int64) []int64` (`e5TrimStride = 8`):
    - Without `-short`: returns the exact same cells, seeds, and order as before (`e5Seeds` for kept cells, `nil` when `race && cell%8 != 0`).
    - With `-short` (and neither explicit `-seed`/`DST_SEED` nor `E2E_FULL_SEEDS=1`): keeps every eighth cell (`cell%8 == 0`, 10 of 78 cells) on one seed only (the first seed `e5Seeds` would assign that cell).
    - With `-short` plus explicit `-seed`/`DST_SEED` or `E2E_FULL_SEEDS=1`: overrides `-short` and returns the exact non-short path.
  - Added unit test `TestE5CellSeeds` first (verified failing on stub before implementation, passing after) covering all 78 cell indexes across five seed sets (`default`, `many`, `unpinned`, `single-pinned`, `single-unpinned`), non-short parity, `-short` trimming to 10/78 cells with 1 seed, explicit-seed / full-seeds override parity, and the non-empty kept-cell invariant.
  - Wired `TestE5MigrationEveryFailurePointS` through `e5CellSeeds` and added one `t.Logf` line on the parent test when `-short` trims the run: `e5 short smoke: 10/78 cells, 10/234 seed runs (1 seed per kept cell)`.

### Commands run and results (Docker `dev` container, one run at a time)

| Command | Result | Go test time | Wall time |
|---|---|---|---|
| `docker compose run --rm -T dev gofmt -l .` | empty | — | — |
| `docker compose run --rm -T dev go vet ./...` | clean | — | — |
| `docker compose run --rm -T dev go test -count=1 -v ./test/e2e -run '^TestE5CellSeeds$'` (red, pre-impl) | FAIL (expected: `cell=0: got [166963932986536 1 2], want [166963932986536]`) | 0.018 s | — |
| `docker compose run --rm -T dev go test -count=1 -v ./test/e2e -run '^TestE5CellSeeds$'` (green) | PASS (3 subtests) | 0.016 s | — |
| `docker compose run --rm -T dev go test -count=1 -v ./test/e2e -run 'TestE5'` | PASS — 78 cells, **234** `seed=` subtests (78 × 3 seeds), 0 smoke log lines | 229.420 s | 232.19 s (3m52.19s) |
| `docker compose run --rm -T dev go test -short -count=1 -v ./test/e2e -run 'TestE5'` | PASS — **10/78 cells**, **10/234** `seed=` runs (logged `e5 short smoke: 10/78 cells, 10/234 seed runs (1 seed per kept cell)`) | 11.121 s | 13.81 s |
| `docker compose run --rm -T dev go test -short -count=1 ./test/e2e` | PASS | 80.730 s | 83.30 s (1m23.30s) |
| `docker compose run --rm -T dev go test -short -count=1 -v ./test/e2e -run 'TestE5MigrationEveryFailurePointS' -seed=1` | PASS — **78/78 cells**, **78** `seed=1` runs, 0 smoke log lines | 79.455 s | 82.13 s (1m22.13s) |
| `GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e` (then `git checkout -- agent_work/07-e2e/gates`) | `overall=pass` (`tools`, `gofmt`, `vet`, `build`, `detlint`, `panics`, `deps`, `comments`, `selfcontained` all pass) | — | 15.98 s |






## 15. Adversarial fix round (Neon): ATTACKS findings F1–F5

Written by the orchestrator from the builder's commits and run log: the builder's run was
cut off by its provider's spending cap after the code was committed and before it wrote
this section. The design description was checked against the code in the round-3 review
(`FABLE-REVIEW.md`).

- **F1 (`7654e7a` failing tests, `eaa0556` fix).** New node-channel request
  `opVerifyShards` (op 4): the coordinator sends each holder named by a copied version's
  manifest the staged record (or live manifest record) together with the shard list; the
  holder merges the record durably, so its inventory expects the shard from then on,
  checks the blob against the checksum and answers per shard. Used (a) after each object's
  copy, as a plain presence check before the staged record is written; (b) in the verify
  step and the post-flip catch-up, through `Root` of the destination; (c) in `Retire`
  before the manifest swap. Threshold per stripe: the write acknowledgement quorum
  (`AckQuorum`, k+1 by default). Below it the step fails and is retried; when verified plus
  unreachable holders are fewer than k, the staged version is marked dead and its staged
  record deleted, so the saga compensates before the flip and re-copies from the live
  source after it. `Retire` deletes staged records only after every live peer holds the
  swapped manifests. No new wire error code.
  **The guarantee, in one sentence:** a migration never routes a bucket to, or swaps a
  manifest onto, a copied version unless the destination holders have confirmed, and from
  then on keep, at least the write quorum of every stripe's shards — however long any
  node, the coordinator included, was stalled.
  **Still a stated limit:** a coordinator that never returns leaves its saga unfinished
  (OQ-13). And a bucket's migration sends whole-bucket messages, bounded by the 16 MiB
  frame.
  Tests: `internal/node/dst_migstall_test.go` (36 subtests: stall right after the copy on
  12 seeds; stalls at each later phase; a stalled destination holder; the check-then-act
  interleaving; staged-then-collected compensates; post-flip catch-up re-copies);
  `test/e2e/migstall_test.go` (`TestE4MigrationCoordinatorStalledPastOrphanGraceS`, at
  default seeds, about 0.6 s); canary `TestCanaryMigrationUnverifiedStallLosesObject`.
- **F2 (`aec000c`).** `sweep` puts a seed in shard `seed mod 4` (was its index mod 4), so a
  single-seed replay lands in the shard its printed command names; a `-seed` run matching
  no seed subtest fails; `TestReplayCommandsReproduceEverySeed` plants a failure on every
  seed under `DST_SEEDS=8` and re-executes each printed REPLAY line, E5 cells included.
- **F3 (`eaa0556`, `79fb2f6`).** `Retire` reports swaps to the commit sink;
  `TestOracleSeesMigratedManifestSwap`.
- **F4 (`23e1341`).** E4 on the simulator asserts no shard of the pre-migration versions is
  left outside the destination the moment the saga completes.
- **F5 (`ab86c7b`).** `scenarioAbsenceShortOfLoss` in E3: one holder records an absence
  while k others verify; no loss declared, bytes identical, shard rebuilt.

Builder's verification before the cut-off (from `objstore-p2-07-e2e-advfix.log`):
`go test -count=1 -cover ./internal/node` ok, 86.8%; `go test -short -count=1 ./test/e2e`
ok, 73 s; node, saga, storage and end-to-end packages ok (end-to-end 351 s); the Opus
driver `a02-ambiguous-start.sh` exit 0, F1 lines HELD; 120 s fuzz ok; tier R E4 and E5 ok;
race ok (node 427 s; end-to-end 460 s with `-timeout 20m` — the default 10-minute timeout
is too short for it now); `DST_SEEDS=500 ./internal/node` ok, 1502 s. Not run: the 500-seed
end-to-end sweep and `GATE_ONLY=G0`.

Open after the review (round 3, NITS): two tests are missing (the shard threshold; the
re-check in `Retire`).

## 16. After the fix round: gate runs, one more failing seed, the real-process step (orchestrator)

- **Full build-stage gate on `c21e374`:** G0–G5, G6 and G7 green (unit 412 s, 2,000-check
  properties 345 s, 15 fuzz targets 497 s, race 2,936 s, 500-seed sweep 3,968 s). G5R red.
- **A failing sweep seed in a landed package, not a product defect.** A second sweep, with
  the seed base of commit `c2f0e8f`, failed `internal/merkle`
  `TestLyingPeerDST/.../seed=214339956433005`: "the liar was never engaged: failed 23
  invalid 0". Every safety claim held on that seed; a per-seed guard demanded something
  only probable (that a liar round got three tree replies in a row). Fixed in `e130ca7`
  (the standing builder for that package): per seed, the liar was contacted and a round
  failed; across a sweep of ten seeds or more, at least one fabricated entry was refused;
  seed pinned. Measured: 1 of 1,503 seeds has `invalid == 0`. Both mutations (honest nodes
  never contact the liar; the liar never fabricates a tree) are caught. The replay and the
  package were re-run by the orchestrator: ok.
- **The real-process step (G5R) after sustained load.** It failed in both runs in which it
  followed the hour-long sweep in the same gate (E1 and E4, then one test: PUTs answered 503
  after a one-second shard request timeout) and passed in every other run: alone (five
  times), and after a ten-minute heavy preload at a load of 4. A wait for the load to drop
  did not help (the load was already low the second time). The cause after sustained load
  is NOT established; the hypervisor taking CPU from the VM is suspected, not measured.
  The gate now runs the step first among the test gates and still exactly once
  (`agent_work/gates/gate.sh`, `g5r_tierr`). The product-side remedy remains request 5 of
  section 9: a configurable shard request timeout.
- **Pitfall recorded:** `GATE_OUT` must be a path inside the repository; a path under
  `/tmp` is written inside the container and lost with it (two diagnostic logs were lost
  this way). And two packages swept in one `go test` without `-timeout` hit the default
  ten-minute limit.
