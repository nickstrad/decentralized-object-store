# Chunk 6 build notes — integration: the object store and sync engine

**Repair round:** sections 1–9 describe the first build, corrected in place where the repair changed
a fact; section 10 is the repair round (each finding, its test, what changed, numbers, requests).

Seat: Opus builder. Branch `chunk/06-integration`. Everything below was run in the `dev`
container unless it says "host". Numbers marked "500 seeds" come from
`DST_SEEDS=500 go test ./internal/node -run DST -v` (section 9); others from the default seeds.

## 1. What was built, file by file

### internal/node (new)
| File | What it is |
|---|---|
| `codes.go` | node sentinels (`ErrNotReady`, `ErrStale`, `ErrUnsettled`, `ErrFenced`, `ErrMalformed`, `ErrUnknownSaga`, `ErrFrame`, `ErrStopped`, `ErrNoMigration`, `ErrConfig`) on codes 176–185 (`ErrStaleRequest`, 186, removed in the repair); `AllCodeEntries` / `CodeTable` join gossip, storage, merkle, occ, saga, the object API and node into one `env.CodeTable` |
| `mux.go` | the channel byte that multiplexes one transport between gossip (1), storage shard RPCs (2), OCC (3), metadata Merkle diffs (4) and node RPCs (5); `gossipView` and `regionView`, the adapters from gossip's view to `storage.Membership`, `merkle.Membership`, `occ.Membership` |
| `node.go` | `Config` (defaults, `Validate`), `Node` (an `env.Process`): construction and wiring, start/stop, inbound dispatch, `EncodeState`, discard handling, quota recount, GC markers, `ShardRequestSize` |
| `objects.go` | the object/bucket API on the single-threaded node: create bucket, put/get/head/delete/list objects, tags, ACL, quota, migration start/status, members, health; precondition translation; quota pre-check |
| `records.go` | the node's metadata encodings: object record (content type + storage manifest), bucket record (initial region, quota limits), staged record (migration copy); reserved system names (`~bucket`, `~live`, `~reg/<saga>`, `~retired`, `~flip`, `~claim/<region>`, `~stage/<saga>`, `~cv/<saga>/<version>`, `~gc/<version>`, `~orphan/<version>/<node>`, `~absent/<version>/<stripe>.<index>/<holder>` (section 14), object-level `~lost/<version>`) |
| `meta.go` | `metaRepairer`, the `merkle.Repairer` over every OCC key (metadata anti-entropy); node RPCs (meta fetch, pointer view, purge of a version list); the two DST canary switches |
| `inventory.go` | per-bucket shard inventory: scrub, expected-vs-actual Merkle comparison, rebuild from k shards, the collection rule of section 10.3 (dead versions, recorded orphans), absence records and the lost-object declaration (section 14), inbound shard-RPC hook (shard deletes refused), purge |
| `migrate.go` | `migrator`, the real `saga.MigrationOps` (section 10.2 for the repaired protocol) |
| `status.go` | `StatusRows`, the node's rows of the HTTP status table |

### internal/noderuntime (new in the repair; these files were in internal/node)
| File | What it is |
|---|---|
| `frame.go` | TCP frame codec (length, version, kind, request id, sender, body / code, message) and the UDP datagram header; `MaxFrame` (= `node.MaxFrame`), `MaxDatagram` |
| `vfs.go` | the small file-system interface under the file disk; `osFS` |
| `realenv.go` | wall clock with loop-posted timers (`realTimer`), `fileDisk` (section 10.7) |
| `runtime.go` | `Runtime`: the real environment, the node loop, socket readers, connection handling, outbound requests, off-loop name lookup, panic handling (`Done`, `Err`), start/shutdown |
| `service.go` | `Runtime` as `httpapi.Service` (every call posted to the loop; cancellation states of section 10.9), `Inspect` |

### internal/storage (added files)
| File | What it is |
|---|---|
| `api.go` | object-service types (`Condition`, `ObjectInfo`, `PutRequest`, `BucketSpec`, `QuotaInfo`, `TagInfo`, `ACLInfo`, `MigrationInfo`, `MemberInfo`, `Health`, `Counter`, `StatusRow`), API sentinels `ErrNoSuchBucket`, `ErrBucketExists`, `ErrBusy`, `ErrMethod` on codes 160–163 |
| `httpapi/service.go` | the `Service` interface (package `internal/storage/httpapi`, see "HTTP package split") |
| `httpapi/http.go` | the HTTP handler (routes below), the one status table, `Limits`, `Server` (start, address, shutdown) |

No landed storage file was changed.

### Other
| Path | What it is |
|---|---|
| `internal/occ/node_merge.go` (+ test) | `Node.Merge` — the one change inside a landed package (section 5) |
| `internal/testutil/leakcheck/` | goroutine leak check: `Take`, `Snapshot.Leaked(settle)`, `Main(m, settle)` |
| `cmd/objnode/` | `main.go`, `help.txt` (bare flag names), `main_test.go` |
| `compose.yaml` | the `cluster` profile only: three peers in regions a, b, c (section 7) |
| `agent_work/06-integration/cluster-smoke.sh` (+ `.out`) | the cluster smoke test and its committed output |

### HTTP routes
`PUT /{bucket}[?region=&quota-bytes=&quota-objects=]` create · `GET /{bucket}[?prefix=&limit=]` list ·
`GET|PUT /{bucket}?quota` · `GET|PUT /{bucket}?acl=<principal>` (body `r`, `w`, `a` letters) ·
`PUT /{bucket}/{key}` (If-Match, If-None-Match: *, Content-Type) · `GET|HEAD /{bucket}/{key}` ·
`DELETE /{bucket}/{key}` (If-Match) · `PUT /{bucket}/{key}?tag=<name>` · `GET /{bucket}/{key}?tags` ·
`GET /{bucket}/{key}?meta` · `POST /_migrations` `{"bucket","to"}` → 202 · `GET /_migrations/{id}` ·
`GET /_cluster/members` · `GET /_health`. Object responses carry `ETag` (the OCC record's ETag, quoted)
and `X-Object-Version` (the storage version ID) and `X-Content-Sha256`.

## 2. The wiring

| Consumer interface | Implemented by | Notes |
|---|---|---|
| `storage.Membership` (placement) | `gossipView.Members` (live members, alive+suspect, with region); `regionView` filters to one region for region-routed buckets | one `storage.Node` per placement scope (all regions, or one region), sharing the env; only the all-regions one serves inbound shard RPCs (the store is stateless) |
| `merkle.Membership` | `gossipView.Peers` | metadata worker picks one live peer per round |
| `occ.Membership` | `gossipView.Peers` | push-pull rounds and delta fanout |
| `merkle.Repairer` | `metaRepairer` | keys = every OCC key including tombstones; bytes = the single-key OCC document encoding; `Put` = `occ.Node.Merge` (LWW), refusing a fetched version that loses with `ErrStale` so the worker never records its hash |
| `saga.MigrationOps` | `migrator` | section 3.6 |
| `occ.Config.OnDiscard` | `Node.onDiscard` → sink + deferred `handleDiscard` | never re-enters occ inside its merge |
| `gossip.Subscribe` | `Node.onMember` | counts deaths; a join resets the view-settle clock |

**Placement on membership change (PLAN 7.2).** New writes are placed on the current gossip view
(rendezvous over live members). Existing manifests keep their placement: a death changes no manifest
(no stampede). A missing shard is rebuilt by its own holder's inventory when it is back (restart,
wipe-rejoin) — section 3.3. Not built: re-placing shards of a node that never returns (limit, section 6).

**Node loop and goroutine ownership (real runtime).**

| Goroutine | Owner / started by | Stops when | Talks to the loop by |
|---|---|---|---|
| node loop (`Runtime.loop`) | `Runtime.Start` via `wg.Go` | runtime context cancelled (Shutdown or a recovered panic) | owns all protocol state; runs control closures and datagrams |
| UDP reader (`readDatagrams`) | `Start`, `wg.Go` | socket closed in Shutdown | bounded datagram queue, non-blocking: full → dropped and counted |
| TCP acceptor (`accept`) | `Start`, `wg.Go` | listener closed | spawns connection goroutines |
| connection handler (`serve`) | acceptor, `wg.Go`, at most `MaxConns` (semaphore; excess refused and counted) | after one request/response, deadline `IOTimeout`, `ServeTimeout`, or Shutdown closing all tracked connections | posts the request; waits on a 1-slot reply channel |
| outbound request (`roundTrip`) | loop (`realNet.Request`), `wg.Go` | reply, deadline, or runtime context (dial and conn are closed by `context.AfterFunc`) | posts the completion |
| name lookup (`resolve`) | loop (`realNet.Send` to an uncached host name), `wg.Go`, at most one per name | lookup returns, `ResolveTimeout`, or runtime context | posts the address to the cache |
| timer callbacks | Go runtime (`time.AfterFunc`) | fire once; all stopped after the loop exits | post a generation-checked closure (a stopped timer whose event is already queued never fires) |
| HTTP server (`httpapi.Server`) | `Server.Start`, `wg.Go` | `Server.Shutdown` | calls `Runtime` service methods, which post to the loop |
| signal context (`cmd/objnode`) | `signal.NotifyContext` | `stop()` | cancels `serve` |

`Shutdown(ctx)`: mark closed, post `Node.Stop` (gossip `Leave`, merkle `Stop`, inventories dropped,
maintenance timer stopped) and wait for it within ctx, cancel the runtime context, close UDP, TCP and
every tracked connection, `wg.Wait()` unconditionally (every goroutine is cancellable), stop all timers.
A panic on the loop does the cancel and the closes itself and closes `Done()` (section 10.8).
The second `Close` waits for the first and returns nil; any call after close returns `env.ErrClosed`.
No protocol state is under a lock: the mutex guards only lifecycle state and the connection set.

## 3. Design choices and their costs

### 3.1 One object = one OCC key
The object's metadata is one OCC record (`ManifestKey(bucket, key)`) whose value is the storage manifest
plus the content type. Cost: the ETag changes whenever the manifest would (no placement-only rewrite
without a new ETag). Why: two keys (object meta + manifest) are LWW-resolved independently and a
concurrent pair of PUTs could leave one write's metadata over the other's bytes. `ObjectKey`
(`occ.ObjectMeta`) is not used; quota recounts sum manifests (my `Recount`), not `ObjectMeta`.

### 3.2 Metadata anti-entropy: one Merkle tree over every metadata key
A single `merkle.Worker` per node covers the whole OCC document (every key, tombstones and system keys
included), depth 3, period 1 s. PLAN 7.4 says "manifest tree per bucket"; a per-bucket worker cannot
discover a bucket it has never heard of (repair is pull-only), so I used one tree; per-bucket trees are
used for shard inventories (3.3). OCC's own push-pull also runs, at `OCCPeriod` 3 s (whole-document
digest; full exchange on mismatch) as the backstop. Cost: two anti-entropy paths for metadata.

**Tombstones (brief point 3).** A delete is an OCC tombstone. The tree's entry for a deleted key is the
tombstone version's encoding; a repair fetch is merged with LWW, so the tombstone (written by a node that
saw the put, so stamped above it) beats the stale live version a crashed, wiped or partitioned node
still holds, and a fetched version that loses is refused (`ErrStale`) and never enters the tree.
**Retention rule:** tombstones are kept indefinitely (no collection in this chunk), and so are GC
markers (3.4). Cost: metadata grows by one record per deleted key and per dead version.

### 3.3 Shard inventories (PLAN 7.4's adaptation)
Per bucket, each node keeps a Merkle tree of the shards it actually holds (blob name → the shard's
verified header checksum), filled by a full scan at start, the inbound shard-RPC hook, and a bounded
scrub (`InventoryScrub` shards per maintenance tick, re-verifying from disk). Each tick it builds the
tree its manifests (live and staged) say it should hold and `merkle.Compare`s them:
missing or rotted → rebuild from the other holders (fetch all others, `DecodeStripe` against the
manifest checksums, re-encode, keep index i, `ChunkStore.Put`; at most `MaxRebuilds` = 8 at once);
extra → garbage candidate (3.4). The comparison is local (the node computes the expected tree itself
from replicated manifests); peers do not exchange inventory trees. An inventory whose expected and
actual trees are both empty is stopped (bucket migrated away). Cost: expected trees are rebuilt from the
manifests every tick (decoded manifests are cached by record ETag).

### 3.4 Shard garbage collection: evidence, not absence
**Superseded by section 10.3 (the collection rule as repaired: every deletion path and its evidence).**
The first build's text follows. A shard whose version no live or staged manifest references is collected after `GCGrace` only if there
is evidence its version is dead: a replicated marker `~gc/<version>` in the bucket's metadata, or the
node recorded the version as an LWW loser. Markers are written by the node that overwrites or deletes an
object (for the replaced version), by every node that records a manifest-key discard (for the loser),
and by a coordinator whose put fails after its shards were written (bucket moved, quota, precondition).
Shards with no evidence at all (a coordinator crashed mid-put; a put whose storage phase failed — the
storage coordinator does not return the manifest on failure) are collected after `OrphanGrace`
(default 1 h). A migration purge (3.6) deletes shards immediately and marks the versions so they are
never rebuilt. History: the first rule was "unreferenced for GCGrace"; shortening the DST grace to 10 s
made `TestClusterPutGetDST` lose data (a region cut off for 10 s collected live shards whose manifest had
not reached it). Remaining risk, stated: a holder partitioned for longer than `OrphanGrace` before the
manifest arrives collects live shards; repair rebuilds them if k others survive, and otherwise the
version is declared lost under the rule of section 14 once every holder of the stripe can be heard from.

### 3.5 Last-writer-wins plumbing (brief point 2)
`occ.Config.OnDiscard` → the node's sink (`Config.DiscardSink`, used by the oracle in tests) and a
deferred handler. Quota key discard → recount scheduled `RecountSettle` (3 s) after the last quota
discard of that bucket (re-armed by each new one); the recount (`occ.Node.RecountQuota` with a recount
over live manifests) writes only if the stored figure is wrong. Manifest key discard → loser recorded
and marked dead (3.4); never the winner (different random version ID; the DSTs check every collected
version is not live). Health exposes `Discards` (occ's persisted counter), the recent tail (64) and
counters (`discards_seen`, `discarded_objects`, `quota_discards`, `recounts`, `recount_writes`,
`collected_loser_shards`, …); discards are logged through occ's `Log` → `Config.Log` (objnode: stderr).
No read ever returns two versions (one OCC key per object; no 300, no conflict listing).

### 3.6 Migration on real components
**Repaired in section 10.2 (pointer view agreement, copy intents, purge of a named list of dead
versions, retire after every live peer holds the swap) and 10.5 (per-saga registrations, flip
owner).** The table below is the first build, corrected where the repair changed it.
Location = region. Buckets created with `?region=` are routed (`occ.RouteKey`) and their shards are placed
only in that region; unrouted buckets spread across regions and cannot migrate (`ErrNoMigration`, 400).
Writes are accepted only when route == live (`~live`); a bucket is not writable between flip and promote.
Every op runs on the coordinator (no relays other than shard RPCs):

| Step | Op | Real implementation |
|---|---|---|
| 0 allocate | Claim / Unclaim | claim `~claim/<to>` (refused `ErrBusy` if another saga holds it), register `~reg/<saga>` / remove both; route must be `From` (else permanent) |
| 1 copy | Copy / Discard | for each live object: skip if this saga's staged entry has the current source ETag, else GET from source shards, PUT re-encoded into the target region (new version), write staged record `~stage/<saga>`; drop staged entries of deleted objects; return the target root. Naturally idempotent, no cache: step 4's catch-up is a second real copy. Discard: fence the saga (in memory), wait for its in-flight copy to drain, tombstone its staged entries, purge the target region (keep = live and other sagas' staged versions) |
| 2 verify | Root | Merkle root (`merkle.Build`) over (object key, SHA-256 of content hash ‖ size): target = this saga's staged entries; source = live manifests; chosen from the saga input, not from the gossip view |
| 3 flip | Flip | route must be `From`; write `~flip` (saga ID and target), then `SetRoute(bucket, to)` (both LWW by HLC) |
| 4 delete-source | Pointer, Seal, Copy, Root, Retire, Promote | Pointer: every live peer must reply with the same pointer view (route, flip owner, bucket manifest digest) as the coordinator, after a `ViewSettle` stable view; another flip owner is "superseded"; Seal: `~live` = "" once no other `~reg/*` is live (`retired=true` if `~retired` names it); Retire: swap each live manifest for its staged copy `IfMatch` the source ETag and mark the replaced version dead, mark non-live intents dead, wait for every live peer's pointer view to match, purge the bucket's dead versions, drop intents, write `~retired`, deregister; Promote: `~live` = to, release the claim |

Idempotency keys: claim, registration, discard, seal, retire and promote key on `c.Key.Saga` only; Copy
has no cache. Fencing: a discarded saga's copy stops before its next staged write and Discard waits for
it to drain; purged versions are never rebuilt (`purged` set; an in-flight rebuild re-checks it before
writing). Defects found while running the matrix and fixed: (a) a just-restarted coordinator's Pointer
agreed with zero peers (empty view) → view-settle rule; (b) its Root used gossip to locate shard
regions and differed after restart → root from the saga input; (c) its purge skipped target nodes
missing from a partial view → purge also waits for a settled view; (d) a rebuild in flight could
re-create a purged shard → re-check. A permanently lost coordinator leaves its saga unfinished
(stated limit; its registration blocks other migrations of that bucket).

### 3.7 Real transport and disk
NodeID = advertised `host:port`; UDP and TCP share the port. One TCP connection per request (no pooling).
A failure after the request is written is reported as `ErrTimeout` (outcome unknown); a dial failure
as `ErrUnreachable`. Requests to self are delivered through the loop (errors round-trip the code table
like remote ones). **Disk durability is section 10.7** (temp fsync before rename, ancestor fsync,
short writes, crash model); the sentence below about the first build's disk is wrong and kept only
for the record. `MaxFrame` = 16 MiB (= `sim.DefaultMaxMessage`), checked against `merkle.MaxReplyBytes`
+ header and the largest default shard request; `Config.Validate` refuses a layout whose shard request
would not fit. The file disk writes through `<data>/.tmp/` + rename (visible, not durable), `Sync` fsyncs
the file and its directory (a deletion is made durable by the directory fsync); a crash therefore loses
exactly the unsynced writes. Cost: the OCC state blob is the whole document, rewritten and fsynced on
every metadata write (that dominates PUT latency, section 8).

### 3.8 HTTP bounds
`MaxHeaderBytes` 16 KiB, `ReadHeaderTimeout` 5 s, `ReadTimeout` 60 s, `WriteTimeout` 60 s,
`IdleTimeout` 60 s, per-request context 30 s, object body ≤ `MaxObjectSize` (32 MiB), other bodies
≤ 64 KiB (`http.MaxBytesReader`, 413). Objects are held whole in memory on PUT and GET (no per-stripe
streaming; limit, section 6). The runtime bounds inbound TCP connections (`MaxConns` 256) and frames.

### 3.9 Region scope and the cluster profile
The 3-node cluster profile uses k=2, m=1 (3 shards fit 3 nodes). With 4+2 a 3-node cluster cannot place a
stripe. The six-node profile from Chunk 0a passed flags objnode never had (`-gossip`, `-rpc`).

## 4. Claims table (corrected in the repair round)

| Claim the book may publish | Test | Measured |
|---|---|---|
| The joined code table has no collision; every code round-trips to its sentinel; unknown → `ErrRemote` | `TestJoinedCodeTableHasNoCollision`, `TestEveryCodeRoundTripsToItsSentinel`, `TestNodeAndAPICodesStayInTheirBlock` | 37 application codes + 8 env codes |
| No GET returns wrong bytes; every acknowledged PUT reads back after heal; metadata, membership and usage converge (21 nodes, 3 regions, 1% loss, region partition, crash) | `TestClusterPutGetDST` | 500 seeds: every ack read back; converge p50 3.3 s, max 11.0 s |
| Concurrent writes on both sides of a partition converge to one version per key on every node; every acknowledged write is current, superseded by a later acknowledged write that held it (per key), or named as a loser in a discard record — an oracle built from what each write held, independent of occ's classifier; the discards are visible through `Health` | `TestClusterPartitionHealDST` | 500 seeds: discard events p50 435 (238–611), distinct losers p50 54 (27–81); oracle p50 16 current, 9 superseded, 7 discarded; converge p50 7.1 s, max 12.0 s |
| Usage equals a recount of the live manifests on every node after every heal (discard-triggered recount, plus the quota audit) | `TestClusterPartitionHealDST`, `TestClusterPutGetDST`, `TestClusterCrashRestartDST`, `TestQuotaRecountsAfterAManifestDiscardDST`, `TestCrashScenarioUsageMatchesARecountOnTheSeedsThatDrifted`; canaries `TestCanaryNodeSkipsDiscardRecount`, `TestCanaryNodeSkipsQuotaAudit` | 500 seeds: partition usage matches p50 8.6 s, max 12.7 s after heal; overshoot p50 500 B, max 700 B over 800 B; drift seeds 7 and 383 fixed by the audit |
| No node ever deletes a shard of a version that is, or can still become, the winner on any node (oracle on every disk delete, every commit and every 500 steps, from all nodes' metadata, independent of the product's bookkeeping); the losing version's shards are collected after the grace | every full-node DST (`newHarness`); `TestClusterPartitionHealDST` for loser collection | 500 seeds of every DST: no delete of a possible winner; partition loser shards collected p50 23 (11–37) per run |
| A write acknowledged just before the migration flip survives the migration; a pre-flip write of a peer that was away is never purged | `TestMigrationHoldsAWriteAcknowledgedJustBeforeTheFlipDST`, `TestMigrationLateWriteOfAnIsolatedPeerStaysReadableDST` | 500 seeds each |
| PUT, overwrite and DELETE traffic on non-coordinators through a migration, under 2% loss and up to 150 ms latency: every acknowledged write is readable, superseded or on a discard record; every live object lives in the final region; both outcomes | `TestClusterMigrationUnderTrafficDST` | 500 seeds: acked p50 45 (12 during the migration); oracle p50 19 current, 17 current-copy, 15 superseded, 6 discarded |
| Two migrations of one bucket started at the same instant (same or different target) end with exactly one moved; a third on another bucket moves | `TestClusterDuellingMigrationsDST` | 500 seeds: all three terminal p50 14.0 s, max 19.9 s |
| A bit-rotted dead shard is collected; death evidence gets its own grace; an orphan collection that destroys a later-live write is recorded, and the key is declared lost under section 14's rule when a stripe is left with fewer than k shards; shard deletes over the wire are refused | `TestCorruptShardOfADeadVersionIsCollected`, `TestDeathEvidenceGetsItsOwnGracePeriod`, `TestAWriteWhoseShardsWereCollectedAsOrphansIsRecordedLost`, `TestANodeRefusesShardDeletesOverTheWire` | |
| A deleted object stays deleted through anti-entropy, a partition heal and a crashed node rejoining | `TestClusterCrashRestartDST`; canary `TestCanaryDeleteResurrected` | deleted 4 per run |
| A restarted node gets its missing shards back; a wiped node is refilled | `TestClusterCrashRestartDST` | 500 seeds: restarted node whole p50 6.0 s, max 8.4 s after restart; wiped node refilled p50 15.0 s, max 17.4 s |
| With up to m shards per stripe rotted every GET is correct and every rotted shard is rebuilt, moving the damaged bytes only | `TestClusterBitRotDST` (fails if nothing rotted or rebuilt ≠ rotted bytes) | 500 seeds: 9–33 rotted shards per run, repaired p50 153 ms, max 159 ms; rebuilt/rotted bytes 1.000 every seed |
| Chunk 5's 78-cell failure matrix holds on real components | `TestClusterMigrationFailurePointsDST` (pinned seeds and the base seed on every cell; the other sweep seeds spread over the cells) | 577 runs, all in their expected state; begin→terminal p50 11.4 s, max 18.9 s (bound 20 s) |
| A bucket that migrates away leaves no inventory running on the source; a stopped node runs no worker | `TestBucketMigratingAwayStopsItsWorkers`, `TestNodeRefusesMalformedAndPostStopTraffic` | |
| Object round-trip through the full path | `TestPropObjectRoundTripThroughTheFullPath` (rapid) | 2,000 checks at the gate |
| A duplicated request and a lost response never double-apply | `TestPropRemoteOperationsAreIdempotent` (rapid; fails if no check lost a reply) | 2,000 checks at the gate |
| The same Node code runs on the simulator and on real sockets | `TestConformanceOnTheSimulator`, `TestConformanceOnLoopback` (`internal/noderuntime`) | 4 scenarios |
| Frame codec: malformed, truncated, oversized input is refused; never a panic | `TestFrameDecodersRejectMalformedInput`, `FuzzFrame` (`internal/noderuntime`) | 30 s at the gate |
| The frame limit fits a full Merkle reply and a shard request | `TestFrameLimitCoversMerkleRepliesAndShardRequests` | |
| One HTTP status table, tested row by row through real HTTP; every node row mapped through real HTTP; on a real node for the rows a node produces (`ErrNoMigration`, `ErrUnknownSaga`) | `TestStatusTableRowByRowThroughHTTP` (24 rows), `TestEveryNodeStatusRowThroughHTTP` (10 rows), `TestStatusRowsThroughHTTPOnARealNode` | 34 rows |
| No present precondition header is ever applied as an unconditional write (row table in 10.6) | `TestHTTPObjectAPIAgainstTheModel`, `FuzzObjectAPI`, `TestPreconditionsOnTheNodeNeverDegradeToUnconditional` | 30 s at the gate |
| Arbitrary HTTP requests give a status in the table and never a GET that disagrees with the last acknowledged PUT | `FuzzObjectAPI` | 30 s at the gate |
| Oversized bodies (413), oversized headers (431), slow header/body clients are cut off by the timeouts | `TestServerBoundsHeadersBodiesAndSlowClients` | |
| A crash at any step of a disk write, sync, delete or append leaves the old or the new bytes (the simulator's states), and the new bytes once `Sync` returned | `TestFileDiskCrashLeavesOldOrNewBytes` (POSIX crash model; real power loss not covered) | 5 operations × every step × 3 modes × 2 metadata outcomes |
| Lifecycle: start, double start, use after close, double close, close while start is in progress, canceled start (synctest) | `TestRuntimeLifecycleInABubble`, `TestCloseWhileStartIsInProgress`, `TestRuntimeStartHonoursItsContext`, `TestRuntimeCallsBeforeStartAreNotReady` | |
| A call canceled before the loop takes it never applies; one canceled after is applied once and reported (put, get, migration start) | `TestACallCanceledBeforeItReachesTheLoopNeverApplies`, `TestACallCanceledAfterTheLoopTookItAppliesOnceAndIsReported` | |
| Shutdown under load returns every call; shutdown during repair and restart on the same disk | `TestShutdownUnderLoadReturnsEveryCall`, `TestShutdownDuringRepairAndRestartOnTheSameDisk` | |
| No goroutine survives (leak check in `TestMain` of `internal/noderuntime` and `cmd/objnode`); an untracked goroutine fails it | `TestRealRuntimeLeavesNoGoroutineBehind`, canary `TestCanaryShutdownLeak` | |
| A loop panic takes the whole runtime down (sockets closed, every goroutine exited, no Close needed; objnode exits 1); a stalled name lookup never stalls the loop; datagram overflow drops and counts | `TestALoopPanicTakesTheWholeNetworkedRuntimeDown`, `TestServeExitsNonZeroWhenTheNodeLoopPanics`, `TestAStalledNameLookupNeverStallsTheLoop`, `TestRuntimeRecoversAPanicOnTheLoop`, `TestRuntimeDatagramsToSelfAndQueueOverflow` | |
| Three Docker peers in three regions start, join, serve a put on one and a get on another, and refuse stale conditional writes; two runs can overlap | `agent_work/06-integration/cluster-smoke.sh` (host) | section 10.14 |

## 5. Changes inside landed packages

- `internal/occ/node_merge.go`: `func (n *Node) Merge(d *Doc) (bool, error)` — delegates to
  `Store.Merge`, `ErrNotReady` before load. Why: the Merkle repairer must fold a fetched record into the
  node's store with the same LWW merge, discard recording and persistence; the landed `Node` only merges
  from its own wire messages (calling `OnRequest` with occ's unexported push byte would couple to its
  wire format). Test: `TestNodeMergeAppliesLastWriterWins`, `TestNodeMergeBeforeReadyIsNotReady`.
  The determinism lint covers it at the gate (G0 detlint).
- `compose.yaml` cluster profile (allowed by the brief).
- Nothing in gossip, merkle, saga, storage's landed files, env, sim or simtest.

## 6. Limits and things not done

- **No authentication anywhere.** Any datagram is accepted by gossip (a forged death notice evicts a
  node); request frames carry a self-declared sender; the HTTP API is open. Stated limit of the project;
  no partial scheme. (For the help-text seat: the `-id`, `-listen`, `-http` flags expose an
  unauthenticated service.)
- Shards of a node that never returns are not re-placed; stripes stay one shard short (still readable
  with k). Re-placement would need a manifest rewrite (new ETag).
- GET and PUT hold the whole object in memory (bounded by `MaxObjectSize`, 32 MiB default; storage's
  `Node.Get` allocates `Manifest.Size` up front). No per-stripe streaming.
- Layout is per node (`Config.Layout`), not per bucket; GET honours each manifest's layout.
- Tombstones and GC markers are retained forever (metadata grows with deletes and overwrites).
- Every node holds all metadata; OCC rewrites and fsyncs the whole state blob per metadata write.
- Migration status is known only to the coordinator (`404` elsewhere). A permanently lost coordinator
  leaves its saga unfinished (OQ-13). Copy is sequential per object.
- The migration fence is in memory: a coordinator crash ends the copy with the process; the engine never
  runs a forward step after compensation began, so no persisted fence is needed.
- A quota is a pre-check on the coordinating node (no cross-node reservation); overshoot under
  replication lag and partitions is measured (section 8), corrected by a recount after a quota or manifest
  discard and by the periodic quota audit against a recount once a bucket's metadata is still.
- Concurrent PUTs to one key from two nodes in one partition side may both be acknowledged (LWW).
- Pointer reads require every live peer (in the coordinator's view) to agree: a peer that is up but
  unreachable keeps delete-source retrying until it is declared dead or heals.
- One TCP connection per request; no connection pooling.
- (Repair) `ErrStaleRequest` removed. Strays: a write a non-live peer acknowledged before the flip may
  live in the old region after a migration (readable; moved by the next migration). Disk I/O runs on
  the node loop (`env.Disk` is synchronous). A shard collected as an orphan whose write later appears
  is rebuilt if k shards of its stripe remain; otherwise the version is declared lost with a record
  under section 14's rule, which needs every holder of the stripe to be heard from. Tombstones, GC markers, intents' tombstones, orphan and loss
  records are kept forever: one record per deleted key, per dead version, per copy, per orphan
  collection; every metadata write re-encodes and fsyncs the whole document, so write cost grows
  with that history (the CPU profile of the new DSTs is dominated by it, section 10.14).
- (Repair, from REVIEW-1 finding 11) Pending recounts (`n.recountDue`) and recorded losers (`n.losers`) are in memory; a node that
  crashes after recording a discard and before its recount drops that recount, and forgets which
  versions it saw lose (other nodes normally record the same discard; the `~gc/` marker it wrote for
  the loser is durable).

### Uncovered branches (internal/node at 86.1% at the gate)
Disk I/O failures other than "not found" in `occ` persist paths reached through the node; UDP write
errors; `net.ResolveUDPAddr` failures; listener `Accept` errors that are not `ErrClosed`; the
connection-semaphore refusal (needs 257 concurrent connections); a response frame over `MaxFrame`;
`Runtime.Start` listen failures after a successful UDP bind; `crypto/rand` failure fallback; merkle
`Tree.Set` errors (`ErrLeafFull` needs > 4,096 keys in one leaf); `storage.NewNode` failure for a scoped
coordinator (same config as the main one, cannot fail); migration paths only reachable when an OCC
persist fails mid-step (Retire, Promote, discard error returns).

### Intentionally ignored errors (`_ =`)
- `internal/storage/httpapi/http.go`: the JSON encoder and `ResponseWriter.Write` errors (the client is gone; the
  status is already sent).
- `internal/noderuntime/runtime.go`: `net.Conn.Close` on refused, untracked, timed-out or finished connections;
  re-encoding the error frame for an oversized response (the error frame is small and cannot fail).

### Panics added
None (`grep panic( ` over the new code is empty); no allowlist line needed.

## 7. Requests to the orchestrator
**Superseded by section 10.12.** The first build's list follows.

1. **Matrix sweep shape (deviation, needs a ruling).** `TestClusterMigrationFailurePointsDST` runs every
   one of the 78 cells on the base seed and then spreads the sweep's seeds across the cells (seed i goes
   to cell i mod 78): at `DST_SEEDS=500`, 578 full-node runs, each cell 7–8 seeds. Chunk 5 ran 78 × 500
   model runs; 78 × 500 runs of a 21-node cluster measured about 2 h on this machine (0.36 core-s per
   run, twice for determinism), far over the 10-minute budget. Per-run cost was already cut (6 objects of
   ≤ 2 stripes of 4 KiB). If a full 78 × 500 run is wanted, it fits a hardening budget (Chunk 7), not the
   gate.
2. **Gate: cluster smoke.** Run `agent_work/06-integration/cluster-smoke.sh` on the host (it needs Docker
   and ports 18081–18083). It cannot run inside the `dev` container.
3. **Gate: coverage floors.** Add `internal/node` (86.1% at the gate) to `floor_pkgs`; for `internal/storage`, the
   package is at 94.2% overall and the added files at 85.3% (api.go + http.go).
4. **Gate: determinism lint.** `internal/node` and `internal/storage` are outside detlint by design
   (goroutines, sockets, HTTP). The protocol-package file I touched (`internal/occ/node_merge.go`) is
   covered by the gate's G0 detlint run.
5. **C4 corrections at landing.** (a) Metadata anti-entropy is one Merkle tree over the whole OCC
   document per node, not a manifest tree per bucket; (b) shard-inventory trees are per bucket per node and
   compared locally against the tree the node's manifests expect (`merkle.Tree` + `merkle.Compare`, not a
   `merkle.Worker`); (c) the HTTP API is served by `httpapi.Server` inside the objnode process and calls
   `node.Runtime` through `httpapi.Service`; (d) OCC push-pull still runs alongside the Merkle path.
6. **PLAN wording that turned out wrong or needs narrowing.** "Shards no manifest refers to are collected
   after a grace period" (7.2) is unsafe as written (section 3.4); the as-built rule is evidence-based
   with a long orphan grace. "Manifest tree per bucket" (7.4): see 5(a). The Chunk 0a cluster profile's
   flags (`-gossip`, `-rpc`) never existed.
7. **Dockerfile / dev service**: no change needed.

## 8. Measurements
**First build's numbers. Superseded by section 10.14 wherever the repair moved them** (the full-node
DST table, wall times, coverage, the real runtime and the cluster smoke).

### Full-node DSTs, 500 seeds (base 58144794855322), virtual time, 21 nodes in 3 regions
| Metric | min | p50 | p99 | max |
|---|---|---|---|---|
| PutGet: metadata + membership convergence after heal | 1.06 s | 3.25 s | 9.59 s | 10.24 s |
| Partition: convergence after heal | 3.75 s | 7.45 s | 11.41 s | 12.23 s |
| Partition: LWW discard events per run (all nodes' sinks) | 310 | 520 | 679 | 715 |
| Partition: distinct lost versions per run | 41 | 67 | 90 | 95 |
| Partition: distinct lost object versions per run | 3 | 5 | 9 | 10 |
| Partition: quota overshoot over an 800 B limit (100 B objects) | 700 B | 1,200 B | 1,400 B | 1,500 B |
| Partition: quota usage equals manifests on every node, after heal | 4.28 s | 8.47 s | 11.88 s | 12.23 s |
| Partition: recount writes per run (21 nodes) | 6 | 24 | 41 | 43 |
| Partition: loser shards collected per run | 18 | 30 | 46 | 52 |
| Crash: convergence after heal | 1.24 s | 2.06 s | 3.60 s | 4.86 s |
| Crash: restarted node holds all its shards, from restart | 5.24 s | 6.06 s | 7.60 s | 8.86 s |
| Crash: wiped node refilled, from the wipe | 14.2 s | 15.1 s | 16.6 s | 17.9 s |
| Crash: shards rebuilt per run | 0 | 4 | 10 | 11 |
| Bit-rot: rotted shards per run | 6 | 20 | 32 | 36 |
| Bit-rot: all rotted shards repaired, from the rot | 133 ms | 152 ms | 159 ms | 159 ms |
| Bit-rot: rebuilt bytes per rotted byte | 1.000 | 1.000 | 1.000 | 1.000 |
| Matrix: begin → terminal state (577 runs) | 0 | 6.10 s | 10.17 s | 10.70 s |

Zero winner shards were collected in any run (checked every 1,000 events and at the end). Recounts
"settle" in the sense that usage matches on every node at the quota-settle times above; recount writes
are counted per run, not per round (several nodes recount the same figure; a write only when wrong).
The wiped-node refill time includes gossip rejoin and full metadata reload before the inventory runs.

### Wall time
| Run | Wall |
|---|---|
| `go test ./internal/node` (default seeds) | 40–63 s |
| `go test ./internal/storage` | 4–7 s |
| `go test -race -count=3 ./internal/node/...` (gate G4, first run) | 541 s |
| `DST_SEEDS=500 go test ./internal/node -run DST` standalone | **839 s (over the ~10-minute guideline)** |
| same, inside the gate's whole-module sweep (all packages in parallel), final run | 1,488 s |
| whole-module gate G5 (500 seeds), final run | 1,493 s |
Per-seed cost was cut (lighter scrub and maintenance cadences in the DST config, GC grace 10 s, 4 KiB
stripes, 6–18 objects); seeds and checks were not cut except the matrix shape (request 1). Further cuts
available if needed: fewer nodes than 21 is not allowed by the brief; the partition scenario is the
heaviest (about 1.9 core-s per seed).

### Real runtime (3 nodes, loopback, one process, k=2 m=1, 1 MiB stripes, `MEASURE_RUNTIME=1`)
| Metric | Value |
|---|---|
| 1 MiB PUT (8 sequential, round-robin over nodes) | 9.9 MiB/s, p50 100 ms, max 154 ms |
| 1 MiB GET (from another node) | 42.0 MiB/s, p50 25 ms, max 36 ms |
| 32 MiB PUT (4 sequential) | 19.4 MiB/s, p50 1.70 s, max 1.81 s |
| 32 MiB GET | 71.4 MiB/s, p50 441 ms, max 515 ms |
| goroutines at idle (3 nodes + test) | 11 (loop, UDP reader, acceptor per node) |
| goroutines, peak under 16 concurrent 1 MiB puts | 90 |
| heap at idle (3 nodes) | 1.7 MiB |
PUT latency is dominated by fsync: every shard is fsynced with its directory, and every metadata write
rewrites and fsyncs the whole OCC state blob (two metadata writes per PUT: manifest and usage).

### Cluster smoke (host, `agent_work/06-integration/cluster-smoke.sh`)
```
SMOKE: starting node-a1 node-b1 node-c1
SMOKE: all three nodes answer /_health
SMOKE: every node sees three alive members in regions a,b,c
SMOKE: bucket created on node-a1 is visible on node-b1 and node-c1
SMOKE: PUT 1500000 bytes on node-a1: ETag "c0f5…6b80" version a762e6449fcc953f
SMOKE: GET on node-c1 returns the same 1500000 bytes (sha256 e5ba…8589)
SMOKE: create-only PUT (If-None-Match: *) on node-b1 over the object: 412
SMOKE: PUT If-Match the current ETag: 200; the same ETag again: 412
SMOKE: health on node-a1 reports 0 last-writer-wins discards
SMOKE: PASS   (exit 0; full output in cluster-smoke.out)
```

## 9. Commands run and results
**First build. The repair round's commands and results are in section 10.15.**

All through `docker compose run --rm dev …` unless marked host.

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...` | clean |
| `go build ./...` | ok |
| `go test ./...` (default seeds) | all ok; node 86.1%, storage 94.2% (added files 85.3%), objnode 82.5%, leakcheck 80.6% |
| `go test -race ./...` and `-race -count=3` on node and storage | ok (gate G4, 1,073 s) |
| rapid at `-rapid.checks=2000` per rapid package | ok (gate G2, 8 packages) |
| every fuzz target 30 s, `-fuzzminimizetime 5s` (incl. `FuzzFrame`, `FuzzObjectAPI`) | ok (gate G3, 14 targets) |
| `DST_SEEDS=500 go test ./internal/node -run DST -v` standalone (base 58144794855322) | ok, 839 s; measurements in section 8 |
| `DST_SEEDS=500 go test ./...` (gate G5, base 213525946061086) | ok, 1,493 s |
| `MEASURE_RUNTIME=1 go test ./internal/node -run TestMeasureLoopbackRuntime -v` | section 8 |
| `agent_work/06-integration/cluster-smoke.sh` (host) | PASS, exit 0 |
| `agent_work/gates/gate.sh 06-integration` (host) on `c233622` | **overall=pass, counts_for_landing=true**, image sha256:c428e45bd9c9…d1e1 |

Gate summary (c233622, stage build, seed 213525946061086):
```
G0 tools/gofmt/vet/build/detlint/panics/deps/comments/selfcontained  pass
G1 unit        pass  103s
G2 property    pass  344s  (2000 checks, 8 packages)
G3 fuzz        pass  457s  (14 targets, 30 s each)
G4 race        pass 1073s
G5 dst         pass 1493s  (500 seeds)
G6 docker      pass
overall=pass counts_for_landing=true
```
The first gate run (34e1e3d) failed G1 and G4 on one timing-dependent test
(`TestShutdownDuringRepairAndRestartOnTheSameDisk` read a restarted node before its bucket record had
arrived); fixed in c233622 (eventual read), plus the continuous GC-safety invariant. Result files are
left uncommitted under `agent_work/06-integration/gates/`.

## 10. Repair round (REVIEW-1: FAIL, one blocker, eight majors)

Seat: Opus builder (fresh). Every finding below got a regression test written before the fix and
seen failing on the code before it (the commit message of each fix says how it failed). Commits are
listed in section 10.13.

### 10.1 Structure change: protocol core and real runtime split
`internal/node` now holds only what the simulator executes (node, objects, records, meta, inventory,
migrate, mux, codes, status rows) and passes the determinism lint by hand
(`detlint -root /src internal/node`: 9 files, 0 findings; the one finding at first, `sortedKeys`
constrained by `cmp.Ordered`, which admits floats, was fixed by a string/integer constraint).
`internal/noderuntime` (new) holds the UDP/TCP transports, the frame codec, the file disk and its
file-system interface, the wall clock and timers, the loop goroutine, `Runtime` and the service
surface. `cmd/objnode` uses it. API moves: `node.Runtime`, `node.NewRuntime`, `node.RuntimeConfig`,
`node.FuzzFrame` and the frame codec now live in `noderuntime`; `node.StatusRows` and
`node.MaxFrame` stay in the core. `cluster-smoke.sh` runs under a compose project named
`objstore-smoke-<pid>-<random>` and the cluster profile publishes each node's HTTP port on an
ephemeral loopback port (`127.0.0.1::8080`) that the script reads back with `compose port`, so two
checkouts (or the gate and CI) can run it at the same time.

### 10.2 Finding 1 (BLOCKER): a write acknowledged just before the flip
**Tests (each failed on the code before the fix):**
- `TestMigrationHoldsAWriteAcknowledgedJustBeforeTheFlipDST` (9 nodes, k=2 m=1): at the instant the
  flip step starts, a non-coordinator P in the source region PUTs a new key while P→coordinator
  datagrams are dropped and coordinator→P is clogged; both are released when P commits, so P's
  delta to the coordinator is lost but P answers the pointer check with the new route. Metadata
  periods are 10 s so the coordinator cannot learn the write by anti-entropy in time. Before the fix
  the purge deleted the write's shards (the deletion oracle fails at the delete).
- `TestMigrationLateWriteOfAnIsolatedPeerStaysReadableDST`: the same write, but P's outbound links
  are dropped right after its shard requests leave and P is isolated when it commits; it rejoins
  after the migration completed. Before the fix its shards were purged; now the write arrives,
  wins or loses last-writer-wins, and is readable or on a discard record.
- `TestClusterMigrationUnderTrafficDST` (9 nodes, k=2 m=1, 2% datagram loss, 1–20 ms in-region and
  20–150 ms cross-region latency): PUT (new key and overwrite) and DELETE traffic every 75–225 ms on
  random non-coordinator nodes from 3 s to 13 s, plus 8 fresh-key PUTs spaced 250 ms from the flip
  instant; odd seeds complete, even seeds are made to compensate (verify
  refused after acting). After the end and convergence: the ack oracle (10.10) explains every
  acknowledged PUT and DELETE, every live key reads back bytes of an acknowledged write, every live
  object lives only in the bucket's final region, and the deletion oracle (10.3) has held
  throughout. On the code before the fix this test failed on the compensated seeds (finding 9's
  unrouted placement window, objects spread over all regions); the random traffic alone did not hit
  the flip race in 120 seeds (nor with 5% loss and fanout to every peer), which is why the targeted
  test above exists.

**What changed (`internal/node/migrate.go`, `meta.go`, `inventory.go`):**
- `Pointer` asks every live peer in the coordinator's view for a *pointer view*: route, flip owner
  (`~flip` = `<saga>|<to>`), and a SHA-256 digest of every manifest record of the bucket (key and
  version ID, tombstones included). It proceeds only when all equal the coordinator's own view;
  otherwise `ErrUnsettled` (retried). A peer that has the new route accepts no write for the bucket
  (route ≠ `~live`), so once every live peer reports the new route and the same manifests, the
  coordinator holds every write a live peer acknowledged before it stopped accepting writes; the
  catch-up copy, both roots and the retire run on that.
- A purge carries an explicit list of versions; the holder deletes exactly those, and refuses any
  its own metadata calls live (`purge_refused`). Retire's list is every version of the bucket with a
  `~gc/` marker, after it has (1) swapped each live manifest for its staged copy (IfMatch the source
  ETag) and marked the replaced version dead, (2) marked dead every copy intent of this saga that is
  not live, and (3) waited until every live peer's pointer view equals its own (so no live peer
  still reads a replaced version). Discard's list is its own copy intents. A version nobody recorded
  as dead is never purged.
- Copy records a per-saga intent `~cv/<saga>/<version>` before it writes any shard, and the copy's
  storage version is chosen by the migrator (a dedicated storage node whose random source returns the
  version the intent names), so Discard and Retire know every version the saga wrote, including a
  copy whose put failed or was fenced.
- Copy skips an object whose live version is already one of its intents (a retire interrupted after
  its swaps), and `Root(To)` counts those, so a retried delete-source step does not re-copy.

**The not-live peer (paused, partitioned, crashed):** its pre-flip write is unknown to the
coordinator, so its version is on no purge list and its shards stay. When the peer rejoins, the
write merges: a new key becomes live with its shards in the old region (readable; it moves with the
next migration, whose purge list names it once it is replaced); an existing key competes with the
retire's swapped copy under last-writer-wins and one of them is on a discard record (its shards are
then collected as a loser). Tested by `TestMigrationLateWriteOfAnIsolatedPeerStaysReadableDST`.
Residual: if the peer stays away longer than `OrphanGrace` (1 h default), the source holders collect
its shards as orphans (10.3, path 2); when the write then arrives it is rebuilt if k shards of every
stripe remain, and otherwise declared lost with a record under section 14's rule once every holder of
the stripe can be heard from (`TestAWriteWhoseShardsWereCollectedAsOrphansIsRecordedLost`). While a
holder of that stripe cannot be heard from, the write stays live and unreadable (section 14, residual).

### 10.3 Finding 6: shard collection, its invariant, and the collection rule
**Invariant (`internal/node/oracle_test.go`, installed in every full-node DST by `newHarness`):**
each simulated disk is wrapped (`watchDisk`), so every `Delete` of a shard blob is seen as it happens,
independent of the product's bookkeeping (`Node.Collected` is no longer used by any check). At each
delete the oracle reads the metadata of every node in the cluster (up, down or paused) and fails if
the version is referenced by a manifest or staged record on any node that no other node's version
of that key covers (version-vector) or beats (HLC stamp, node, ID — re-implemented in the test). It
also fails if a version is committed after any of its shards was deleted (ack time), and every 500
steps it re-checks every version that lost a shard. It caught the over-the-wire delete (10.3, below)
and finding 1 on the old code.

**Collection rule (as built; replaces section 3.4):** a shard blob is deleted only by these paths.
1. *Dead version* (inventory, every maintenance tick): the blob is on disk, no live manifest or staged
   record in the holder's metadata expects it, the version has evidence of death — a live replicated
   `~gc/<version>` marker in its bucket, or the holder itself recorded the version as a
   last-writer-wins loser — and that evidence has been seen by this holder for at least `GCGrace`
   (counted from when the holder first saw the evidence, not from when the blob became unreferenced).
   Markers are written by: the node that overwrites or deletes an object (replaced version); every
   node that records a manifest discard (loser); a coordinator whose put fails after its shards were
   written (bucket moved, quota, precondition); Retire (replaced source versions; copy intents not
   live); Discard (its copy intents); a lost-object declaration (path 2).
2. *Orphan* (inventory): not expected, no evidence, unreferenced for `OrphanGrace` (1 h default; 10 min
   in the DSTs). Before deleting, the holder writes a replicated record per shard,
   `~orphan/<version>/<stripe>.<index>/<node>` (one key per (version, node) before the review 2 fixes,
   section 13); if that write fails, nothing is deleted. Orphan records no longer decide loss
   (section 14 replaced the "more than m orphan records" rule): if a manifest for that version is (or
   later becomes) live, its holders rebuild missing shards as for any missing shard, and a stripe left
   with fewer than k shards is declared lost under section 14's rule (the lowest-ID live node
   tombstones that exact version, IfMatch its ETag, writes `~lost/<version>` on the object, marks the
   version dead and decrements usage; health reports `lost_objects`).
   Evidence: none — this is the stated residual risk (a holder that does not hear of a write for
   `OrphanGrace`).
3. *Migration purge*: an explicit list of versions, each with a `~gc/` marker (path 1's evidence, no
   grace), sent to every live member; each holder refuses a version its metadata calls live.
   Retire purges only after every live peer holds the swapped manifests.
Nothing else deletes a shard: a rebuild overwrites a missing or corrupt shard of an expected version
(never of a dead or purged one), and the node now refuses the storage layer's `OpDeleteShard` over
the wire (`TestANodeRefusesShardDeletesOverTheWire`, failed before).

**Bit-rotted dead shards:** collection now walks every shard file on disk, including one the scrub
found corrupt (it used to consider only the healthy-inventory tree), and an inventory stops only when
no file of its bucket is left, which ends the start/stop loop the reviewer saw
(`TestCorruptShardOfADeadVersionIsCollected`, failed before). **Grace after evidence:**
`TestDeathEvidenceGetsItsOwnGracePeriod` (failed before: collected 5 s after the marker arrived).

### 10.4 Finding 3: quota drift
A manifest-key discard (live or tombstone loser) now schedules the same settled recount as a
quota-key discard; the recount writes only when the stored figure is wrong.
`TestQuotaRecountsAfterAManifestDiscardDST` is the reviewer's reproduction (one side overwrites with
a different size, the other later with the same size, heal); before the fix usage stayed wrong.
The partition DST's contested overwrites now have sizes 50–149 B on one side, and the put/get, crash
and partition DSTs each wait until every node's usage equals a recount of the live manifests after
their heal. **That check found a second drift** (crash scenario, seeds 7 and 383 of the 500-seed
sweep): a recount written during the partition by a node whose manifests were still incomplete is
causally newer than every quota version it saw, so after the heal nothing conflicts with it and no
discard triggers another recount; usage stayed one object high forever. Fix: a **quota audit** in
maintenance — up to four buckets per tick, round-robin; when stored usage differs from a recount of
the live manifests and neither the quota record's version nor the bucket's manifest digest has
changed for `RecountSettle`, the lowest-ID live node writes the recount (only if wrong; counted as
`QuotaAudits`). The discard-triggered recount stays as the fast path.
`TestCrashScenarioUsageMatchesARecountOnTheSeedsThatDrifted` replays both seeds;
`TestCanaryNodeSkipsQuotaAudit` requires both to fail with the audit off; the discard-recount canary
switches off both layers and also runs the reproduction.

### 10.5 Finding 9: concurrent migrations; routed bucket creation
Registrations are one key per saga (`~reg/<saga>`), so concurrent registrations commute.
Flip records its owner in `~flip` and refuses a bucket that is not at the migration's source;
`Pointer` returns a different owner as superseded (`~<owner>`), and if every peer agrees this saga
owns the flip but the route disagrees, it re-writes the route and retries — so a same-target duel ends
with one `moved`. A region-routed bucket is not writable until its route arrives (`writable` checks
the bucket record's region). `TestClusterDuellingMigrationsDST` (9 nodes, default simulated network): two
coordinators migrate one bucket at the same instant (odd seeds to different targets, even seeds to
the same target) while a third migrates another bucket; all three must end, the other bucket must
move, exactly one of the duel must be `moved`, both buckets must have route = live, no staged,
registration or claim key may remain, and every object must read back from the final region. Before
the fix, same-target seeds ended with both `moved` and different-target seeds left a registration.

### 10.6 Finding 5: conditional headers (one row each, PUT, DELETE and tag/ACL writes)
| Header and value | Result |
|---|---|
| `If-Match` absent | unconditional |
| `If-Match: *` | applies only if the object exists, else 412 |
| `If-Match: "<etag>"` (one or a list) | applies only if one listed strong tag equals the current ETag, else 412 |
| `If-Match: W/"<etag>"` | weak tags never match (RFC 9110 strong comparison): 412 if no strong tag matches |
| `If-Match: ""` (empty quoted tag) | a valid tag that matches nothing: 412 |
| `If-Match:` empty field, whitespace only, or only empty list elements (`" , ,"`) | an empty list matches nothing: 412 |
| `If-Match: abc` (unquoted), `"abc` (unterminated), `""""` (quote inside), `*, "a"` (* in a list) | malformed: 400 |
| `If-None-Match: *` | applies only if the object does not exist, else 412 |
| `If-None-Match:` anything else (empty, whitespace, a tag, a list) | unsupported: 400 |
| both headers | 400 |
| an etag that is not one of the node's (through the Go API) | 412 (was 400) |
Tests: rows added to `TestHTTPObjectAPIAgainstTheModel`; `FuzzObjectAPI` sends either header with any
value and fails if a PUT or DELETE carrying one was applied when an independent evaluation says it
could not hold (the counterexample it found in 15 s on the old code, `If-Match: " "` applied
unconditionally, is kept in `internal/storage/httpapi/testdata/fuzz/FuzzObjectAPI`);
`TestPreconditionsOnTheNodeNeverDegradeToUnconditional`. API change: `storage.Condition` gains
`IfMatchList bool` and `IfMatchAny []string` (the parsed strong tags).

### 10.7 Finding 4: file disk durability
`Write`: create a temp file in `<data>/.tmp/`, write all bytes (short writes looped), fsync it, close,
rename over the target. `Sync`: fsync the file, its directory, and every ancestor directory this disk
created since the last sync (each fresh directory's parent). The first `Append` to a missing blob goes
through the same temp-and-rename path, so a new log never survives a crash as an empty file. Opening
the disk removes temp files a crash left behind. `Delete` is made durable by `Sync` (directory fsync).
**What a crash leaves** (power loss, file-system model below): a blob is its last synced bytes or the
bytes of the write in progress, never torn; an append keeps its old bytes plus possibly a prefix of
the unsynced append (exactly the simulator's torn crash); a blob whose `Sync` returned nil survives.
Unlike the simulator, the real disk may keep an *unsynced* write after a crash (the rename or the
page cache reached the device); protocol code relies only on synced writes surviving.
**Test:** `TestFileDiskCrashLeavesOldOrNewBytes` runs the disk on an in-memory POSIX model where only
fsynced file data survives a crash and un-fsynced directory changes are either all lost or all kept
(both are checked); for overwrite, create-under-new-directories, delete, append and first-append it
crashes at every step (and separately fails each step, and short-writes), reopens, and requires old
or new bytes, and new bytes whenever `Sync` returned nil. It failed on the old step order (a synced
blob under a new directory vanished), and fails again if the temp-file fsync is removed (an empty
renamed blob). **Not covered:** the real file system's power-loss behaviour (ordering guarantees of a
specific file system, device write caches, fsync honesty) cannot be tested here; the model encodes
POSIX's guarantees only.

### 10.8 Finding 8: runtime hazards
- No lookup on the loop: a send to a `host:port` not in the address cache parses numeric addresses
  directly; a host name starts a lookup on a tracked goroutine with a timeout
  (`RuntimeConfig.Lookup`, `ResolveTimeout` 5 s) and drops that datagram (`unresolved_sends`); the
  result is posted back to the loop. UDP writes carry a 100 ms deadline (`SendTimeout`). Dials already
  ran off the loop. `TestAStalledNameLookupNeverStallsTheLoop`.
- A loop panic records `Err()` (`ErrPanicked`), closes the UDP socket, the listener and every tracked
  connection from the loop's own recovery, cancels the runtime context and closes `Done()`; readers,
  acceptor, handlers, outbound requests and lookups all exit with no `Close`, and `Shutdown` still
  returns. `TestALoopPanicTakesTheWholeNetworkedRuntimeDown` (real sockets; leak check before any
  Close; the listener refuses). `cmd/objnode` waits on its signal context or `Done()` and exits 1 on
  a loop failure (`TestServeExitsNonZeroWhenTheNodeLoopPanics`).
- **Not done:** disk I/O still runs on the loop. `env.Disk` is synchronous by contract (frozen), and
  every protocol package writes and syncs inline; a stalled disk stalls the node. Making it
  asynchronous is a simulator change (request 10.12.6).

### 10.9 Finding 7: lifecycle and cancellation tests
`call` checks the caller's context before queueing and again when the loop takes the call. A call is
in one of five states (queued, running, canceled, delivered, left), moved by compare-and-swap: a call
canceled while queued never runs and returns the context error ("not applied"); a call canceled after
the loop took it runs once, returns `ErrOutcomeUnknown` joined with the context error, and when it
finishes the runtime counts it (`abandoned_calls`) and logs "<op> finished after its caller left".
Tests: `TestACallCanceledBeforeItReachesTheLoopNeverApplies` (20 pre-canceled bucket creates never
apply; a PUT canceled while queued behind a held loop never reaches storage);
`TestACallCanceledAfterTheLoopTookItAppliesOnceAndIsReported` (PUT, GET and migration start canceled
at the moment the loop takes them — a test hook on the loop cancels and waits until the caller has
left — the PUT applied exactly once, three reports); `TestCloseWhileStartIsInProgress` (start paused by
a hook after it entered start-up; Close runs concurrently; start returns `ErrClosed`). `Runtime.Inspect`
runs a function on the loop. Removed: `TestRuntimeCloseDuringStartup`,
`TestRuntimeCancelMidPutLeavesOnlyCollectableShards`, `TestCancelMidGetAndMidMigrationReturnContextErrors`.
A canceled PUT never leaves partial state behind by itself: either it never ran, or it ran to its end
(success, or failure with its version marked dead when the manifest is known). A storage-phase
failure leaves shards of an unknown version, collected as orphans (10.3, path 2).

### 10.10 Finding 2: the last-writer-wins oracle
`ackOracle` (`oracle_test.go`), fed by `Config.CommitSink` (every client commit on any node: the
record the writer held, the version written): an acknowledged write is explained as *current* (the
final record is it), *current-copy* (the final record holds the same content: the migration's copy),
*superseded* (a later acknowledged write's held record, or the acknowledged write whose content that
held record carries, had it in its ancestry — ancestry is per key by construction), or *discarded*
(named as a loser by a discard callback). Nothing uses version-vector coverage across keys. With the
oracle fixed the 500-seed sweep found no failure (section 10.11).

### 10.11 Findings 10 and 11; measurements that moved
- `migrator.Copy` schedules its first step (`TestMigrationCopyNeverCallsBackInsideTheCall`).
- Health renders discard keys as `kind:part/part` (no `%q`); the partition DST checks that a node
  reports the discards through `Health` (count and recent records).
- `node.StatusRows` (10 rows) are each mapped through real HTTP (`TestEveryNodeStatusRowThroughHTTP`);
  a real node produces `ErrNoMigration` (400) and now `ErrUnknownSaga` (404, `Node.Migration`).
  `ErrStaleRequest` (code 186), which nothing returned, is removed.
- `TestClusterBitRotDST` fails if nothing was rotted or rebuilt bytes ≠ rotted bytes; the idempotency
  property dropped its vacuous loop and fails if no check lost a reply.
- `matrixSeeds` runs every pinned seed and the base seed on every cell and spreads the other sweep
  seeds over the cells (`TestMatrixSeedsRunEveryPinnedSeedOnEveryCell`); at `DST_SEEDS=500` that is
  78 + 499 = 577 runs (section 7's 578 was wrong).
- Dead code removed: `Node.orphanSince`, the sorted-set record (`encodeSet`/`decodeSet`, replaced by
  per-saga keys).
- `realNet.Request` refuses a non-positive timeout and a self-request over the frame limit.

### 10.12 Requests to the orchestrator (replaces section 7)
1. **Determinism lint:** add `internal/node` to the roots in `detlint_dirs` of `gate.sh`
   (`for d in env sim gossip erasure merkle occ saga node`). Run by hand on this branch:
   `detlint -root /src internal/node` → 9 files, 0 findings.
2. **Coverage floor:** add `internal/node` and `internal/noderuntime` to `floor_pkgs` (section 10.14
   has the figures). `internal/storage` stays above the floor as a package; its added files are
   reported separately in 10.14.
3. **Race:** add `internal/noderuntime` and `cmd/objnode` to the `-race -count=3` loop of G4.
4. **Cluster smoke on the host:** after the container run, call
   `agent_work/06-integration/cluster-smoke.sh` from the repository root with no arguments (needs
   Docker on the host; it uses its own compose project name and ephemeral loopback ports, so it can
   run next to another checkout's run; exit status non-zero on any failure).
5. **Sweep budget:** see 10.14 — state whether the measured 500-seed wall time of `internal/node`
   is acceptable or which DST should move to the hardening chunk's budget.
6. **Simulator change request (informational, nothing blocked):** `env.Disk` is synchronous, so the
   real runtime does file I/O and fsync on the node loop (finding 8 asked for no blocking I/O there).
   Moving it off the loop needs an asynchronous disk interface in the frozen `env` package and in
   every protocol package's persistence path. Not attempted.
7. **PLAN wording:** 7.2's collection rule is the one in 10.3 (three deletion paths, evidence for
   each); tombstones, GC markers, orphan and loss records are retained indefinitely (7.5 mentions a
   retention period); migration "verify pointer" is "every live peer agrees on route, flip owner and
   the bucket's manifest digest" (7.7); the migration purge deletes a named list of dead versions,
   not "everything not live". 7.4's "manifest tree per bucket" is one metadata tree per node
   (unchanged from section 7 item 5).
8. **C4 corrections at landing:** as section 7 item 5, plus: the real runtime is its own component
   (`internal/noderuntime`) between `cmd/objnode` and the node core.

### 10.13 Commits of the repair round (oldest first)
`0117c5b` split protocol core / runtime · `7547131` smoke under its own project and ports ·
`30f67c8` failing tests for findings 1, 2, 3, 6, 9 · `b1a5fcd` evidence-only purge, pointer agreement,
per-saga keys, collection rule, manifest-discard recount · `df394f4` late-flip set-up deadline ·
`03d84b1` failing precondition tests · `cc85906` entity-tag parsing · `14f717d` file disk durability
and crash test · `71feb10` runtime hazards and exact cancellation · `838619a` findings 10 and 11 ·
`16297f4` usage recount checked after every heal · `9505b80` shard deletes over the wire refused ·
`f2a1691` two set-up fixes the sweep exposed, per-seed cost cuts · `994c005` quota audit ·
`de2a763`, `4dfcd2a` pointer and accessor tests, gofmt · `97b9850` smoke output · then these notes.

### 10.14 Numbers that moved (all in the `dev` container unless "host")
**Full-node DSTs, 500 seeds (`DST_SEEDS=500 go test ./internal/node -run DST -v`, default base),
virtual time.** Every one of the 10 DSTs held all its seeds (the matrix 577 runs).
| Metric | min | p50 | p99 | max |
|---|---|---|---|---|
| PutGet: metadata + membership convergence after heal | 1.21 s | 3.27 s | 9.66 s | 10.96 s |
| Partition: convergence after heal | 3.61 s | 7.09 s | 11.51 s | 12.01 s |
| Partition: acknowledged writes per run | 16 | 23 | 26 | 26 |
| Partition: discard events per run (all nodes' sinks) | 238 | 435 | 601 | 611 |
| Partition: distinct lost versions per run | 27 | 54 | 74 | 81 |
| Partition: distinct lost object versions per run | 2 | 4 | 7 | 7 |
| Partition: acknowledged writes explained as current / superseded / discarded (oracle) | 13 / 2 / 3 | 16 / 9 / 7 | 17 / 14 / 11 | 18 / 15 / 12 |
| Partition: quota overshoot over an 800 B limit | 200 B | 500 B | 600 B | 700 B |
| Partition: usage equals a recount on every node, after heal | 4.56 s | 8.62 s | 12.26 s | 12.75 s |
| Partition: recount writes per run (21 nodes) | 3 | 18 | 35 | 38 |
| Partition: loser shards collected per run | 11 | 23 | 35 | 37 |
| Crash: convergence after heal | 0.99 s | 2.04 s | 3.74 s | 4.38 s |
| Crash: restarted node holds all its shards, from restart | 4.99 s | 6.04 s | 7.74 s | 8.38 s |
| Crash: wiped node refilled, from the wipe | 14.0 s | 15.0 s | 16.7 s | 17.4 s |
| Bit-rot: rotted shards per run | 9 | 20 | 31 | 33 |
| Bit-rot: all rotted shards repaired, from the rot | 134 ms | 153 ms | 159 ms | 159 ms |
| Bit-rot: rebuilt bytes per rotted byte | 1.000 | 1.000 | 1.000 | 1.000 |
| Matrix: begin → terminal state (577 runs) | 0 | 11.4 s | 16.5 s | 18.9 s (bound 20 s) |
| Migration under traffic: begin → terminal | 0 | 11.7 s | 25.9 s | 27.6 s |
| Migration under traffic: acknowledged writes per run (during the migration) | 13 (0) | 45 (12) | 65 (32) | 68 (39) |
| Migration under traffic: writes refused busy (bucket moving) | 0 | 17 | 57 | 62 |
| Migration under traffic: oracle current / current-copy / superseded / discarded | | 19 / 17 / 15 / 6 | 30 / 27 / 29 / 14 | 33 / 29 / 30 / 16 |
| Duel: begin → all three terminal | 8.4 s | 14.0 s | 19.5 s | 19.9 s |
The matrix's begin→terminal time roughly doubled (p50 6.1 s → 11.4 s, max 10.7 s → 18.9 s against
the 20 s bound) because delete-source now waits for every live peer's pointer view twice (pointer,
then retire). The partition scenario's discard counts fell because its contested phase is 6 rounds
instead of 10. The "0" minima are compensated runs of an empty bucket, which finish within one
virtual instant (every step completes through zero-delay timers).

**Wall time (dev container, 4 cores shared):**
| Run | Wall |
|---|---|
| `go test ./internal/node` (default seeds) | 45 s |
| `go test ./internal/noderuntime` | 9 s |
| `DST_SEEDS=500 go test ./internal/node -run DST` standalone, every DST in parallel | **1,345 s (22.4 min): over the ~10 min budget** |
Per test (wall of its slowest shard, while running alongside the others): put/get 255 s, traffic
257 s, partition 216 s, crash 164 s, duel 136 s, bit-rot 133 s, late-flip 45 s, isolated 37 s,
quota 21 s, matrix 673 cell-seconds. A CPU profile of all DSTs: about half the time is the landed
OCC store re-encoding and hashing the whole metadata document on every merge, then the metadata
Merkle worker's scrub (13%) and the inventory maintenance (13%). What was cut: the partition
scenario's contest (10 → 6 rounds) and grace (10 s → 6 s); the traffic DST's fanout, loss and load;
idle grace waits in the duel and traffic DSTs; sequential sweeps sharded; top-level DSTs in parallel.
Not cut: seeds, node counts, invariants. Further cuts that would work but change measured numbers or
the landed OCC package: lower `ScrubBatch`/`InventoryScrub` in the DST configurations, or OCC
persistence that does not re-encode the whole document per merge (request 10.12.5).

**Coverage (default seeds):** `internal/node` 86.2%, `internal/noderuntime` 87.7%, `cmd/objnode` 82.5%,
`internal/testutil/leakcheck` 80.6%, `internal/storage` package above the floor, its added files
(`api.go` + `http.go`) 87.3% of statements. Uncovered in `internal/node`: OCC persist failures inside
migration steps (Retire, Promote, Discard, dropIntents error returns), `RecordMeta` and `setScalar`
error paths, the `designated` refusal branch of the audit on the nodes that are not lowest, merkle
`Tree.Set` errors, the scoped storage-node construction failure. In `internal/noderuntime`: listener
`Accept` errors other than close, UDP write errors, resolver failures logged on the loop, the
connection-semaphore refusal, a response frame over the frame limit.

**Real runtime (3 nodes, loopback, one process, k=2 m=1, 1 MiB stripes,
`MEASURE_RUNTIME=1 go test ./internal/noderuntime -run TestMeasureLoopbackRuntime -v`):**
| Metric | Before the repair | Now |
|---|---|---|
| 1 MiB PUT (8 sequential, round-robin) | 9.9 MiB/s, p50 100 ms, max 154 ms | 5.7 MiB/s, p50 174 ms, max 241 ms |
| 1 MiB GET (from another node) | 42.0 MiB/s, p50 25 ms | 48.0 MiB/s, p50 16 ms, max 39 ms |
| 32 MiB PUT (4 sequential) | 19.4 MiB/s, p50 1.70 s | 11.5 MiB/s, p50 2.70 s, max 3.32 s |
| 32 MiB GET | 71.4 MiB/s, p50 441 ms | 61.2 MiB/s, p50 522 ms, max 538 ms |
| goroutines at idle (3 nodes + test) | 11 | 11 |
| goroutines, peak under 16 concurrent 1 MiB puts | 90 | 96 |
| heap at idle (3 nodes) | 1.7 MiB | 0.6 MiB |
PUTs are slower because every blob write now fsyncs its temp file before the rename, in addition to
the file and directory fsync at `Sync` (finding 4); GET numbers vary run to run on the shared machine.

**Cluster smoke (host):** `agent_work/06-integration/cluster-smoke.sh` → PASS, exit 0, 7 s with a
warm build cache (output in `cluster-smoke.out`); two copies started at the same moment both passed
(projects `objstore-smoke-851512-…` and `…851513-…`, disjoint ephemeral ports) and left no container.
Compose warns that the shared `objstore-gomod`/`objstore-gocache` volumes were created by project
`objstore`; harmless (they are named volumes shared on purpose).

### 10.15 Commands run in the repair round and their results
All through `docker compose run --rm -T dev …` unless marked host.
| Command | Result |
|---|---|
| `gofmt -l internal cmd`, `go vet ./...`, `go build ./...` | clean |
| `detlint -root /src internal/node` (hand-run, tools built from `agent_work/gates/detlint`) | 9 files, 0 findings |
| `go test -count=1 -cover ./internal/node ./internal/noderuntime ./cmd/objnode ./internal/testutil/leakcheck` | ok; 86.2%, 87.7%, 82.5%, 80.6% |
| `go test -race -count=3 ./internal/noderuntime ./cmd/objnode ./internal/storage` | ok (21 s, 2 s, 37 s) |
| `DST_SEEDS=500 go test ./internal/node -run DST -v` standalone (default base) | ok, 1,345 s; every DST held every seed |
| two earlier 500-seed runs of the same | found the set-up and quota-audit issues fixed in `f2a1691` and `994c005` (each listed there) |
| `DST_SEEDS=200 DST_SEED_BASE=7000` on the new DSTs | 7 late-flip set-up deadlines (fixed in `df394f4`), all else held |
| `go test ./internal/storage -run '^$' -fuzz '^FuzzObjectAPI$' -fuzztime 20s` on the old code (the target now lives in `./internal/storage/httpapi`) | found `If-Match: " "` applied unconditionally (corpus file committed) |
| `MEASURE_RUNTIME=1 … TestMeasureLoopbackRuntime` | section 10.14 |
| `agent_work/06-integration/cluster-smoke.sh` (host), once and twice concurrently | PASS, exit 0 each |
| `agent_work/gates/gate.sh 06-integration` (host) on `97b9850` | **overall=pass, counts_for_landing=true** (summary below; `repair-runs/gate-summary.txt`) |
| `agent_work/adversarial/06-integration/run-all.sh` (host) | exit 2: held 18, broken 0, setup errors 4 (below) |

Gate summary (`97b9850`, stage build, seed 166823056793601, comment baseline 631b44e):
```
G0 tools/gofmt/vet/build/detlint/panics/deps/comments/selfcontained  pass
G1 unit        pass    67s
G2 property    pass   360s  (2000 checks, 8 packages)
G3 fuzz        pass   460s  (14 targets, 30 s each)
G4 race        pass   963s  (internal/node -race -count=3: 671 s)
G5 dst         pass  1726s  (500 seeds, whole module)
G6 docker      pass
overall=pass counts_for_landing=true
```
This gate run predates the notes commit that follows it; no Go file changed after `97b9850`.

**Independent harness (`run-all.sh`, not edited; output in `repair-runs/harness-run-all.txt`):**
`SUMMARY held=18 broken=0 setup_errors=4`. Why, exactly:
- a01 simulator, a02 loopback, a04 CLI: the harness module does not compile — its `real.go` uses
  `node.Runtime`, `node.NewRuntime` and `node.RuntimeConfig`, which moved to `internal/noderuntime`
  (the structure change the repair brief ordered). Not a product regression. To give evidence anyway,
  a scratch copy of the harness with only those three references changed to `noderuntime` (copy
  deleted afterwards, nothing committed) ran all three Go drivers: **247 HELD, 0 BROKEN** (240
  simulator, 6 loopback, 1 CLI; `repair-runs/harness-moved-path.txt`).
- a03 existing checks: 18 HELD; 19 named tests "no result" because they moved to
  `internal/noderuntime` (`TestConformanceOnTheSimulator`, `TestConformanceOnLoopback`,
  `TestLoopbackClusterServesObjectsAcrossNodes`, `TestFrameRoundTrips`,
  `TestFrameDecodersRejectMalformedInput`, `TestFrameLimitCoversMerkleRepliesAndShardRequests`,
  `TestRuntimeConfigValidation`, `TestStatusRowsThroughHTTPOnARealNode`, `TestRuntimeLifecycleInABubble`,
  `TestRuntimeStartHonoursItsContext`, `TestRuntimeCallsBeforeStartAreNotReady`,
  `TestShutdownUnderLoadReturnsEveryCall`, `TestShutdownDuringRepairAndRestartOnTheSameDisk`,
  `TestRealRuntimeLeavesNoGoroutineBehind`, `TestRuntimeRecoversAPanicOnTheLoop`,
  `TestRuntimeDatagramsToSelfAndQueueOverflow`; all pass there, in the gate) or were replaced as
  finding 7 asked (`TestRuntimeCloseDuringStartup` → `TestCloseWhileStartIsInProgress`;
  `TestRuntimeCancelMidPutLeavesOnlyCollectableShards` and
  `TestCancelMidGetAndMidMigrationReturnContextErrors` →
  `TestACallCanceledBeforeItReachesTheLoopNeverApplies` and
  `TestACallCanceledAfterTheLoopTookItAppliesOnceAndIsReported`).
- No check pinned a status code that changed. One status did change: an `If-Match` with an etag that
  is not one of the node's is 412 now (was 400); a malformed (unquoted) one is still 400.
The harness's `evidence/` files were restored from a copy taken before the run.

### 10.16 Chosen not to do, and why
- **Sweep budget.** The 500-seed sweep of `internal/node` is 22.4 min, over the ~10 min guideline;
  cutting it further means cutting seeds, nodes or checks, or changing the landed OCC store's
  whole-document persistence (10.14). Left for the orchestrator (request 10.12.5).
- **Asynchronous disk.** Needs a simulator/`env` change (request 10.12.6).
- **Re-homing strays.** A late write that lands in the old region stays readable there and is moved
  by the next migration; no background mover was built (it would add concurrent swaps on many nodes).
  Not counted in health.
- **`ErrOutcomeUnknown` crossing HTTP.** It is joined with the context error, so the status table maps
  it as the context error (503/504); no separate status row.
- **Persisting `recountDue`/`losers`.** Still in memory (section 6); the quota audit now repairs a
  dropped recount, and the `~gc/` marker for a loser is durable.
- **`PLAN.md`, `STATE.md`, `README.md`, gate files, docs:** not touched (rules); requests in 10.12.

## 11. HTTP package split

`internal/node` became a determinism-lint root; the lint also checks every module package a root
imports, so `internal/storage` is now linted and `http.go` (and `api.go`'s `context` import) failed it.
A move, no behaviour change, no allowlist entry.

| From | To | Why |
|---|---|---|
| `internal/storage/http.go` | `internal/storage/httpapi/http.go` (package `httpapi`) | `net/http`, `net`, `encoding/json`, `sync`, context deadlines, goroutines |
| `internal/storage/http_test.go` (package `storage_test`) | `internal/storage/httpapi/http_test.go` (package `httpapi_test`) | tests the moved code; `FuzzObjectAPI` included |
| `internal/storage/testdata/fuzz/FuzzObjectAPI/` | `internal/storage/httpapi/testdata/fuzz/FuzzObjectAPI/` | the corpus follows its target |
| `Service` interface in `internal/storage/api.go` | `internal/storage/httpapi/service.go` | its methods take `context.Context`, not on the lint's import list |
| `StatusRow` type in `http.go` | stays in `internal/storage/api.go` | a plain `{Err, Status}` struct; `internal/node.StatusRows` returns it |
| `TestAPICodesAreInTheirBlockAndDistinct` in `http_test.go` | `internal/storage/api_test.go` | checks `storage.APICodeEntries`, which stays in `api.go` |

Everything else in `api.go` (request/response types, API sentinels and codes) stays in `internal/storage`.
`StatusTable`, `StatusOf`, `Limits`, the `Default*` HTTP limits, `MaxListLimit`, `HeaderVersion`,
`HeaderContentSHA`, `NewHandler`, `NewServer` and `Server` are now `httpapi.*`. Importers updated:
`internal/noderuntime/service.go` (`var _ httpapi.Service`), `internal/noderuntime/runtime_ops_test.go`,
`cmd/objnode/main.go`. The status-table literal is now keyed (`{Err: …, Status: …}`) because `go vet`
flags unkeyed literals of a type from another package; rows and order are unchanged. Error strings keep
their `storage:` prefix. `agent_work/adversarial/` still uses the old `storage.*` names (not mine to edit).

**Coverage (default seeds):** `internal/storage` 97.8%, `internal/storage/httpapi` 87.3% of statements.

**Commands run (Docker unless marked host):**

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...`, `go build ./...` | ok |
| detlint over the `detlint_dirs` roots and their module imports, `-allow agent_work/gates/detlint-allow.txt` (same invocation as `g0_detlint`) | clean, 72 files in 11 packages |
| `go test ./internal/storage/... ./internal/node/... ./internal/noderuntime/... ./cmd/...` | ok |
| same with `-race` | ok |
| `go test -run '^FuzzObjectAPI$' -v ./internal/storage/httpapi` | the committed corpus entry runs |
| `go test -cover ./internal/storage ./internal/storage/httpapi` | 97.8%, 87.3% |
| `agent_work/gates/canaries.sh '^partial-run$'` (host) | `canaries: 1 of 1 caught` |
| `agent_work/06-integration/cluster-smoke.sh` (host) | `SMOKE: PASS`, exit 0 |

## 12. Entity-tag list fix

**What.** The independent harness (C31) found that `parseEntityTags` in
`internal/storage/httpapi/http.go` split `If-Match` on every comma before reading quoted strings. RFC 9110
8.8.3 allows a comma inside an entity-tag (`etagc` includes it). So the valid list `"a,b", "<current>"` got
400 on object PUT, object DELETE, tag PUT and ACL PUT. The parser now makes one left-to-right pass. It skips
OWS (SP/HTAB only) and reads either an empty element, `*`, or `[W/]"etagc*"`. It then skips OWS and needs
a comma or the end of the field. Anything else is 400 (`storage.ErrInvalid`). `etagc` is accepted as
0x21, 0x23–0x7E and obs-text 0x80–0xFF, the same byte set as before. `W/` is case-sensitive, and no
whitespace is allowed between `W/` and the quote. `*` must be the only non-empty element.

**Empty elements (RFC 9110 5.6.1), decided.** Empty elements are accepted and ignored: leading, trailing,
doubled, or the whole field. No count limit is applied beyond the server's header-size limit. A present
header never becomes an unconditional write. A field with only empty elements, or only weak tags, gives an
empty strong list (`IfMatchList` true, no candidates), so the write gets 412. `*` with empty elements
(`, * ,`) is treated as `*`.

**Tests (commit 4cf432f, failing before the fix; fix in 28a05d0).**
- `TestHTTPObjectAPIAgainstTheModel`: adds the harness's four reproductions: PUT `/b/k`,
  PUT `/b/k?tag=color`, PUT `/b?acl=alice` and DELETE `/b/k`, each with `If-Match: "a,b", <current>`
  (200/200/200/204). It also adds a non-matching `"a,<current>"` row (412). The test now tracks the current
  ETag for each path, so tag and ACL rows use their own ETags.
- `TestParseEntityTagsTable` (42 rows, through `export_test.go`'s `ParseEntityTags`): quoted commas;
  several tags with OWS; weak validators, including a weak tag with a comma; `*` alone, in a list, and with
  empty elements; empty, whitespace-only and all-empty fields; trailing comma; unterminated quotes, including
  one holding a comma; unquoted tags; doubled quotes; two tags with no comma; SP, HTAB, control bytes and DEL
  inside quotes (reject); obs-text (accept); lowercase `w/`; `W/` followed by a space; VT is not OWS. Each row
  is also checked against the reference tokeniser.
- `FuzzObjectAPI`: every header value is now compared with `referenceEntityTags`, an independent byte-level
  state machine. They must agree on accept or reject, the strong-tag list and the star flag. A parser error
  must also wrap `storage.ErrInvalid`. `mayApply` now uses the reference tokeniser to decide whether a
  write may apply, instead of splitting on commas. Two aligned seeds cover quoted commas, weak tags with
  commas, empty elements and two tags with no comma.

**Commands (Docker unless marked host).**

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...` | ok |
| `go test ./internal/storage/... ./internal/node/... ./internal/noderuntime/... ./cmd/...` | ok |
| same with `-race` | ok |
| `go test -cover ./internal/storage/httpapi` | 87.8% |
| `go test ./internal/storage/httpapi -run '^$' -fuzz '^FuzzObjectAPI$' -fuzztime 60s -fuzzminimizetime 5s` | PASS, 379,052 execs, 43 new interesting |
| `agent_work/adversarial/06-integration/run-all.sh` (host) | `SUMMARY held=662 broken=0 setup_errors=0 elapsed_seconds=490`, exit 0; evidence restored afterwards with `git checkout -- agent_work/adversarial` |

**Note.** The existing seed `"x", "@"` in `FuzzObjectAPI` has its fields misaligned, because each step
needs four NUL-separated fields. That seed was left unchanged. The two new seeds use the correct layout.

## 13. Review 2 fixes (REVIEW-2: FAIL, two new majors, four nits)

Each item had its regression test committed first, failing on the code before the fix.

**1. (MAJOR) Orphan records overwritten across ticks.** Test:
`TestOrphanRecordsAccumulateAcrossMaintenanceTicks` (`internal/node/gc_test.go`) is the reviewer's case
made deterministic: 4 nodes, k=2 m=2 (a shard of every stripe on every node), fixed 2 ms latency, a
5-stripe object. Stripe 4 launches only after the first window's quorum. The test clogs the writer's
link to holder `a` after `a` stores stripes 0–3, and drops the link to holder `y`. Stripe 4 reaches `a`
1.5 maintenance periods later and never reaches `y`. The writer is then cut off past `OrphanGrace`, and
the test asserts the arrival timing it depends on. On the old code `a`'s second record replaced its
first, so every stripe showed 2 orphaned shards (not more than m), and the object stayed live while
reads failed with `ErrUnavailable` (seen failing). `TestAWriteWhoseShardsWereCollectedAsOrphansIsRecordedLost`
now runs 1, 2 and 6 stripes (6 > `DefaultWindow`), muting the writer only after every stripe has
landed. Its symmetric 3-node layout still detected the loss on the old code, so it is coverage rather
than a regression test. New DST invariant `checkLiveReadableOrLost` (`helpers_test.go`) runs at the end
of the put/get, bit-rot, crash and partition DSTs: every key the metadata lists as live is read
through a random node, and a failed read fails the run unless the cluster has a `~lost/<version>`
record. `TestLiveReadableOrLostOracleCatchesAnUnreadableLiveKey` shows the check fails on a live key with
2 of 3 shards rotted. Change (`inventory.go`): one record per collected shard, key
`~orphan/<version>/<stripe>.<index>/<holder>`, with a value set by the key (the same encoding,
holding one pair). Only that holder writes the key and every write of it is identical, so a later
tick, a restart, a concurrent write or last-writer-wins can add entries but cannot erase one.
`noteOrphan` was unchanged and still read records in the old format (section 14 removed it: orphan
records no longer decide loss). Cost: one metadata key per
orphaned shard (orphans are the rare case of a writer away longer than `OrphanGrace`). The
`orphan_records` counter now counts shards.

**2. (MAJOR) `realNet.Request` called back twice.** Tests (`internal/noderuntime/request_once_test.go`):
`TestRequestCallsBackExactlyOnceOnEveryPath` runs in a synctest bubble and counts callbacks for a zero
and a negative timeout (to itself and to a peer), an oversized request to itself, no network, and a
normal request to itself. `TestNetworkedRequestCallsBackExactlyOnce` runs on loopback and covers an
oversized request to a peer and a peer that is not listening. Four rows called back twice on the old
code. Change: the non-positive-timeout check and the oversized check (now for any destination, so
`-race` slowness in encoding cannot turn it into a timeout) run before the request is registered or
its timer armed. Each calls back once, from the loop. Every later path goes through `complete`.
Audit: `Send` returns its error and has no callback. `serve` guards its reply (`replied`), `call`
guards `done` (`once` plus the CAS state), and timers fire once per generation. After shutdown no
callback runs at all, because the loop has stopped. That is the runtime's documented closed state.

**3. Flip after a crash between its writes.** Test: `TestFlipAfterACrashBetweenItsWritesLeavesTheBucketWritable`
(`internal/node/flip_test.go`, 9 nodes). A wrapped flip step's first attempt writes this saga's flip
record and then a route to r2, as a losing concurrent migration would, and fails like a crash. The
retry must complete the migration. The test checks that route and flip owner agree on r1 on every
node, that puts through three nodes succeed and that the old object is readable. On the old code the
saga was compensated with "routed to r2" (seen). Change (`migrate.go` Flip): when the flip record
names this saga and target, set the route to the target and succeed, as Pointer already does.

**4. Oracle content map.** Test: `TestAckOracleKeepsIdenticalPayloadsOnTwoKeysApart`. It writes equal
payloads to k1 and k2, overwrites k2, then deletes k1 behind the oracle. The old oracle called k1's
lost write "superseded". Change: `byContent` is keyed by `key + "\x00" + content`. A migration copy
keeps its key, so copies are still linked.

**5. `syncChain` retry.** Test: `TestSyncRetriedAfterAMidChainFailureSyncsEveryNewAncestor`
(`syncchain_test.go`). It injects one fsync failure on `shards/b` while syncing `shards/b/00/v/0.0`
under four new directories, then retries. On the old code the retry returned nil while `shards` was
not durable in the data directory. Change: walk every ancestor up to the data directory, sync the
parent of each one still marked new, and clear the mark only after that fsync succeeds.

**6. Nits.** `track` refuses a connection when the runtime context is cancelled or the loop panicked
(`TestNoConnectionIsTrackedAfterALoopPanic`, `...AfterTheRuntimeContextEnds`; both tracked it on the
old code). The panic path cancels before it takes the lock to close tracked connections, so a
connection is either closed by that sweep or refused. The negative-count config error now names
`occ fanout` (validation table rows check the field names). Two stale sentences in sections 6 and
7 were corrected, and section 10.3's orphan key was updated. The `fuzz-short.sh` nit is in
`agent_work/gates/`, which is the orchestrator's to change.

**Residual limit, restated (superseded by section 14; the paragraph below claimed more than the code
did, see ARBITRATION-1).** A write is lost only if its writer is unreachable for longer than
`OrphanGrace` (1 h by default) while other holders hold its shards without its manifest. In that
case the holders record every shard they collect, one key per shard. Once the write's manifest is
live anywhere and more than m shards of any stripe appear on those records, the lowest-ID live node
tombstones that version and writes `~lost/<version>`. For objects of every size, the write is
therefore either readable or declared lost on record, never live and unreadable. A stripe that is
unreadable for another reason (for example, more than m shards rotted on disk) is not covered by this
rule. The DST invariant would report it.

### Commands run (Docker `dev` unless "host")
- `gofmt -l .`: empty. `go vet ./...`: clean.
- `go test -count=1 ./internal/node/... ./internal/noderuntime/... ./internal/storage/... ./cmd/...`:
  ok (node 49.2 s, noderuntime 9.6 s, storage 3.1 s, httpapi 1.9 s, objnode 0.2 s, objsim 0.1 s).
- `go test -race -count=3 ./internal/noderuntime/... ./internal/storage/... ./cmd/objnode/...`: ok
  (25.4 s, 35.4 s, 7.0 s, 1.5 s). The first run failed only on the oversized-peer row (timeout
  before too-large), which led to the any-destination size check in item 2.
- `go test -count=1 ./internal/node -rapid.checks=2000 -run 'Prop'`: ok (96.5 s).
- detlint over the gate's roots: `checked 72 files in 11 packages`, exit 0. paniccheck: exit 0.
- `DST_SEEDS=100 go test -count=1 ./internal/node`: ok (253.0 s).
- host `agent_work/06-integration/cluster-smoke.sh`: `SMOKE: PASS`.
- host `agent_work/adversarial/06-integration/run-all.sh`: `SUMMARY held=662 broken=0 setup_errors=0
  elapsed_seconds=390`. Its evidence files were restored with `git checkout`.
- Not run: the 500-seed gate and the full gate (the orchestrator runs them).

## 14. Arbitration fix (ARBITRATION-1: FAIL, one MAJOR)

**The defect.** A PUT is acknowledged at k+1 durable shards, so a holder may never store its shard.
If the writer is then cut off before its manifest spreads, holders that did store shards collect them
as orphans. The old detector counted only orphan records and declared loss above m per stripe. A
stripe with m or fewer shards collected and one shard never written kept fewer than k shards. It was
never declared lost, so the key stayed live and every GET failed.

**When is a version declared lost, and on what evidence (as built, `internal/node/inventory.go`).**

1. *Absence record.* A placement holder H of shard (version v, stripe s, index i) writes the replicated
   record `~absent/<v>/<s>.<i>/<H>` (bucket-scoped, one immutable value per key, like the orphan
   records) only when all of the following hold in one step of its loop:
   - v's manifest is live in H's own metadata (a staged migration copy never qualifies), v is not
     purged and has no death evidence, and H holds no shard of (v, s, i) whose checksum matches the
     manifest;
   - H's rebuild of that shard failed, and that rebuild got an answer from the holder of every other
     index of stripe s: a shard, `not found` or `corrupt`. A timeout, an unreachable node, a
     not-ready node or any other error is not an answer, and then no record is written;
   - fewer than k of the shards collected by that rebuild verify against the manifest's checksums.
2. *The record is binding.* After writing it, H never rebuilds (v, s, i), and it refuses that shard
   over the wire with `ErrFenced` (counted as `absent_refused`). The check reads H's own metadata, so
   it survives a restart.
3. *Declaration.* On each maintenance tick, the node that has the lowest ID in its own view checks
   every live manifest. If some stripe has more than m indices whose manifest holder wrote an
   absence record, that node declares the version lost. Only a record from that index's manifest
   holder counts. It writes `~lost/<v>` on the object first and then tombstones the manifest key
   (IfMatch the version's ETag), so a crash between the two writes leaves a live key with a loss
   record, which the next tick finishes, and never a deleted key without one. It marks the version dead (`~gc/<v>`) and decrements usage. It counts
   `LostDeclared` and logs `object <b>/<k> version <v> lost: the holders of <c> of the <n> shards of
   stripe <s> recorded that they hold none, fewer than k=<k> remain`. The remaining shards are no
   longer expected, so every holder collects them through the death-evidence path (10.3 path 1)
   after `GCGrace`. A GET then answers `ErrNotFound` (HTTP 404). Health shows `lost_objects` and the
   new counters `absent_records` and `absent_refused`. Orphan records are still written but no
   longer decide anything, and `lostByOrphans` and `noteOrphan` are removed.

**Why it cannot fire while a good shard can still be produced.** A record's index held no verifiable
shard when the record was written. Because the record is binding, that index never gets one later.
More than m such indices leave at most k-1 indices of the stripe that can hold a shard, at the
declaration and afterwards. Rebuild needs k verifiable shards, so it cannot add one. A late request
from the writer is refused. Safety therefore does not depend on timing, view agreement or a grace
period. The declaration waits for evidence, not for a timer.

**A holder that is down or partitioned blocks the decision.** It cannot write its own record. Every
other holder's rebuild times out on it, so they write none either. While any holder of the stripe
cannot be heard from, no new absence record for that stripe is written. A declaration in that time can
rest only on more than m records written earlier, while every holder answered. `TestAHolderThatCannotBeHeardFromBlocksALossDeclaration` covers this. View agreement
is not required. A designated node in each half of a partition can declare only on records that are
already sufficient.

**Last-writer-wins and replication.** Records are written only by their holder, and every write of a
key is identical, so no merge or discard can erase one. The declaration's tombstone is an ordinary
metadata write. A newer write of the same key made concurrently on a side the declarer has not heard
from competes with the tombstone under last-writer-wins, and the loser is counted and logged as any
discard is. When the newer write wins, it stays live and readable, and only v's `~lost` record remains.

**Cost.** One replicated, permanently retained metadata key per missing shard of an unrecoverable
stripe: at least m+1 per lost version, written only when a stripe is already below k. One metadata
lookup per incoming shard write (`OpPutShard`). A failed rebuild re-verifies the blobs it gathered.
Latency: after a heal, the declaration takes a manifest sync to each missing holder, one maintenance
tick and one rebuild round (bounded by `RequestTimeout`), a metadata sync of the records, and one more
tick on the designated node. All tests settle within 30 simulated seconds at a 1 s maintenance period.
The answers a rebuild collects are not a snapshot. If another holder received its shard (only through
a late writer request) between answering and the record, H's record is stale, and that index's
redundancy is given up for good. Safety is unaffected, because the record remains binding.

**Residual limit, restated exactly as the code behaves.** Suppose a stripe of a live version has
fewer than k verifiable shards. It is declared lost only after more than m of its holders have each
written an absence record, and each of them must have heard from every holder of the stripe during
one rebuild. So the guarantee "every key listed live is readable, or declared lost on record" holds
once every holder of every such stripe is up, holds the manifest and can reach the others. Until
then the key stays live and unreadable, with GET failing with `ErrUnavailable` (503). A holder that
never returns, such as a node removed for good, keeps it so forever: the code cannot tell such a
holder from a partitioned one. A wiped disk (`WipeRejoin`) loses the holder's own copy of its record
until metadata sync returns it, and with it the refusal of that shard. A writer's shard request sent
before the wipe and delivered to the new incarnation in that window is stored. The simulator
delivers such a request (it addresses a node, not an incarnation). In the real runtime a TCP
connection already established cannot survive the receiving process's restart, but a request
carries no destination incarnation and dials after it is scheduled
(`internal/noderuntime/runtime.go`, `Request`), so one still waiting for scheduling, address
resolution or its connection can reach the restarted holder before its deadline (read from the
code, not reproduced on the real runtime). The refusal therefore assumes the holder keeps its
absence metadata; losing that metadata plus a late enough shard request is the residual on either
transport. Two cases follow.
(a) The version is already declared lost: the stored shard belongs to a version that is dead on
record. The holder gets the death marker by metadata sync and collects the shard after `GCGrace`; the
key stays not live with its loss record on every node
(`TestAStaleShardWriteToAWipedHolderOfADeclaredLostVersionIsCollected`, the first confirm pass's
scenario). Between the write and the collection the disks hold k shards of a version already
declared lost.
(b) The version is not yet declared: the record is false until the holder's shard goes. If the other
k-1 good shards exist, the stripe can be read and rebuilt, and no further absence record is written
for it; if more than m records were already written, the version is declared lost although k shards
exist at that moment. This needs a disk loss on a record-holding holder of a stripe already below k
and a shard write held in the network across that holder's wipe and restart. Not closed and not
tested in the package; the confirm pass reproduced it independently (ARBITRATION-2.md, "Independent
residual check": the converse oracle fails the run, as it should). A closure was built and withdrawn: a node that starts with empty metadata refused shard
writes until its first metadata sync round, which also refused the first writes of every new
cluster (`TestMigrationHoldsAWriteAcknowledgedJustBeforeTheFlipDST` failed on all three shards).
The tombstone
discards a concurrent newer write of the same key only under last-writer-wins, counted and logged.
The orphan rule itself is unchanged: a write whose writer stays away longer than `OrphanGrace` can
lose its shards and then be declared lost.

**Tests** (`internal/node/lost_test.go` unless named; committed in `2d34871` before the code):
| Test | What it proves | Old code |
|---|---|---|
| `TestAnOrphanedWriteWithAnUnwrittenShardIsDeclaredLost` | The arbiter's reproduction (k=2, m=2, quorum 3; n002 never stores, n000 and n001 collect after the orphan grace; writer cut off on acknowledgement). After heal it is declared lost on every node: manifest not live, `~lost/<v>` present, GET not found, `lost_objects` > 0, the log line present, the declaration seen by the harness oracle, and no shard of v left on any disk after `GCGrace`. | FAIL (still live) |
| `TestAnOrphanedWriteWithEnoughShardsLeftStaysReadable` | Control: the same isolation, but every shard lands and one holder stays with the writer, so 2 are collected (≤ m) and k remain. It stays live, reads back the bytes on every node, is never declared, and all 4 shards are rebuilt. | pass |
| `...DeclaredLostAtFourPlusTwo` / `...StaysReadableAtFourPlusTwo` | The same at 4+2 with quorum 5 on 6 nodes (2 collected, 1 never written, 3 left → lost; 2 collected, 4 left → readable and fully rebuilt). | FAIL / pass |
| `TestAMultiStripeWriteWithOneUnrecoverableStripeIsDeclaredLost` | 4+2, 5 stripes. Stripe 4 never reaches one holder that has stripes 0–3, and 2 holders are collected. The test asserts before the heal that only stripe 4 is below k. Declared lost. | FAIL |
| `TestAMultiStripeWriteWithOneStripeRottedDuringThePartitionIsDeclaredLost` | 4+2, 2 stripes. 2 shards are collected as orphans and a kept holder's stripe-1 shard is bit-rotted (mixed causes). Only stripe 1 is below k. Declared lost. | FAIL |
| `TestAHolderThatCannotBeHeardFromBlocksALossDeclaration` (`partitioned`, `paused`) | k=2, m=2. Two holders rot and the third good holder is isolated or paused for 2 minutes, so only 1 shard is reachable. Nothing is declared. After the heal the object reads back and every shard is rebuilt. With the "heard from every holder" condition removed, this test fails (checked by a temporary edit, not committed). | pass |
| `TestMoreThanMShardsRottedBeyondRepairIsDeclaredLost` (`k2m2`, `k4m2`) | 3 shards of stripe 1 rotted, no orphan record anywhere (asserted). Declared lost as above. | FAIL |
| `TestOneRottedStripeWithinMIsRepairedNotDeclaredLost` | m rotted shards are rebuilt and the object is never declared. | pass |
| `TestAHolderThatRecordedItHoldsNoShardRefusesItLater` | After its absence record, a holder refuses the original shard over the wire (`ErrFenced`, `AbsentRefused`), and the shard does not reappear on disk. | (new behaviour) |
| `TestAWriteWhoseShardsWereCollectedAsOrphansIsRecordedLost` (1, 2, 6 stripes), `TestOrphanRecordsAccumulateAcrossMaintenanceTicks` | Kept unchanged, and both pass under the new rule. | pass |

**Oracle changes** (`helpers_test.go`, `oracle_test.go`, `dst_putget_test.go`):
- *Converse oracle, in every harness test and DST.* The harness records every committed manifest by
  storage version. When any node commits a `~lost/<v>` record (the declaration now reports its
  tombstone and loss record to `CommitSink`), the oracle reads the disks of every manifest holder,
  including nodes that are down or paused. It fails the run unless some stripe has fewer than k
  shards that decode and match the manifest checksum at that instant.
- *Put/get DST extended (`TestClusterPutGetDST`, `orphanedAfterAck`).* After the existing checks, the
  scenario does the following. Two random nodes are made unreachable from a random writer, and a
  1-stripe object is put. The writer is muted 1–3 ms later, after its shards are sent and before the
  manifest commits. On the acknowledgement, the writer and k-1 or k (random) shard-holding holders
  form one side of a partition. The run lasts `OrphanGrace` + 20 s (this scenario's `OrphanGrace` is
  60 s, up from 10 min) and then heals. After that, `checkLiveReadableOrLost` runs. A version with k
  verifiable shards of every stripe at the heal must not be declared lost and must read back its
  bytes. The GC oracle exempts only this key (`gcOracle.residual`), because its orphan collection is
  the stated residual case. Measures: `late_lost`, `late_recoverable` and
  `late_orphans_plus_unwritten` (seeds whose late version, at the heal, had a stripe below k while no
  stripe had more than m shards collected; that is the arbitration's class).
  The pre-fix product code was run against these tests with a temporary copy and
  `DST_SEEDS=24`. Seeds 1, 10 and others failed with `pg/late version … is live in metadata,
  unreadable … and declared lost on no record: … 3 of 6 shards verified, need 4`. The fixed code
  passes.

**Takeover (Nick's ruling of 2026-11-22, `/root/briefs/objstore-chunk6-fable-finish-ruling-2026-11-22.md`).**
The Opus round that built this section was stopped during its race run, and the orchestrator seat
(Fable) finished the fix as builder. Checked by reading and by running: the rule above, every test in
the table, and that the six `...DeclaredLost` tests fail on the test-only commit `2d34871`. Changed
after the takeover:
- `noteAbsent` splits a record name into three parts at most, so a holder ID that contains `/` is
  read back (`TestAbsenceRecordKeyRoundTripsForAnyHolderName`, with
  `TestMalformedAbsenceRecordNamesAreIgnored`, in `absent_internal_test.go`). Before, such a
  holder's records were ignored and its stripes could never be declared lost.
- `serveStorage` refuses a shard write with the metadata store's error while the node's metadata is
  not loaded. Before, the absence check was skipped in that state and the shard was stored.

- `declareLost` writes the loss record before the tombstone
  (`TestTheLossRecordIsCommittedBeforeTheManifestIsTombstoned`, committed failing in the commit
  before the change). In the earlier order, which the orphan-count rule also had, a crash between
  the two writes deleted the key and left no record of the loss. Still open after a crash right
  after the tombstone: the death marker and the usage decrement of that declaration are skipped;
  the shards are then collected as unreferenced after `OrphanGrace` and the quota audit corrects
  the usage.

**Not done.** No canary switch was added, since the brief names none. The PLAN error table has no row
for a permanently lost object. A declared-lost key answers 404 (`ErrNotFound`, the table's row)
because its manifest is tombstoned.

**Requests to the orchestrator.** (1) If a distinct status for a lost object is wanted (for example
410 with the `~lost` record), it is a PLAN table change. (2) `absent_records` and `absent_refused` are
new health counters for the prose seat.

## 15. Second-eyes round (NEON-SECOND-EYES: FAIL, four MAJORS, two NITS)

The owner ordered a second independent pass over the section 14 fix: a code review plus the
reviewer's own scenarios (20 of them; 17 held). Record: `agent_work/06-integration/NEON-SECOND-EYES.md`;
the scenarios as written: `agent_work/adversarial/06-integration/neon-second-eyes/`. All six findings
were accepted. Fixed by the orchestrator seat as this round's builder.

| # | Finding | Change | Test (in `internal/node`) |
|---|---|---|---|
| 1 MAJOR | A disk read error on the holder's own shard was taken as "holds none": `stateAbsent` wrote a binding absence record for an intact shard, and a healthy object could be declared lost (k=2, m=0 with two read errors; k=2, m=1 with one rotted shard and read errors on the two good holders). | `stateAbsent` writes a record only when its own read of the shard answers not found, corrupt, or a blob whose checksum is not the manifest's. Any other error writes nothing. The scrub drops a shard from the inventory only when its read answers not found or corrupt; a read error leaves the inventory entry as it was (see the focused confirm below). | `TestLocalTransientDiskIOErrorMustNotDeclareHealthyObjectLost` (both layouts); failed before |
| 2 MAJOR | A rebuild already waiting for replies stored its shard even when the holder's own absence record had arrived meanwhile (a wiped holder that gets the manifest before its record). The record did not bind on that path. | The rebuild's completion reads the metadata again and stores nothing when the holder has an absence record for the shard, the version has death evidence, or the metadata is not loaded. | `TestWipedHolderRebuildRacesOwnAbsenceRecord`; failed before |
| 3 MAJOR | The converse oracle failed any migrated version that was declared lost: a migration installs its manifest without the commit callback, so the oracle had never seen the version. | Test oracle only: for a version it has not seen, `onCommitLoss` reads the manifest the declaring node still holds (the loss record is written before the tombstone) and checks the disks against it. | `TestLossDeclarationAfterBucketMigration`; failed before |
| 4 MAJOR | The refusal of shard writes before metadata loads had no test. | Test added. With the refusal removed (temporary edit, not committed) it fails with `OpPutShard before metadata loaded returned <nil>`. | `TestServeStorageRefusesShardWriteBeforeMetadataLoads` |
| 5 NIT | `expectedAll` read absence records from tags of any scope. | Only bucket-scoped tags (empty object part) count. | covered by the existing record tests |
| 6 NIT | The refusal test did not restart the holder; the write-order test did not crash between the two writes. | Both behaviours now have their own scenarios. | `TestHolderIntactRestartPreservesAbsenceRefusal`, `TestDeclarerCrashBetweenLossRecordAndTombstone` (declarer restarts; declarer stays down and a successor finishes) |

The reviewer's other scenarios are kept as regression tests in `secondeyes_*_test.go`: late shards
(after orphan collection, during a rebuild, after an absence record, residual case (b) with the oracle
catching it), declarations racing an overwrite and a delete, two declarers in a split cluster,
non-holder and cross-stripe records ignored, a staged migration copy never recorded absent, node IDs
containing `/` end to end, staggered orphan collection, a manifest arriving just inside the grace
period, edge layouts (k=1, m=0; k=2, m=0; k=1, m=1; short last stripe; empty object), pause and resume.

**Section 14 claims corrected by this round.** "A timeout, an unreachable node, a not-ready node or
any other error is not an answer" was true of the other holders and false of the holder's own disk
until finding 1 was fixed; "H never rebuilds (v, s, i)" was false for a rebuild in flight until
finding 2 was fixed. Both now hold as written.

**Residual added.** A holder whose own disk keeps failing reads of a shard (not "not found", not
"corrupt") never writes an absence record for it, so it blocks a loss declaration for that stripe the
same way an unreachable holder does: the key stays live and unreadable until the disk answers. A
rebuild cannot rewrite such a shard, because the chunk store reads the existing blob before it writes.

**Focused confirm (NEON-CONFIRM.md: NITS).** All six findings confirmed closed with nine further
scenarios (kept as `secondeyes_confirm_test.go`); full package, race, 2,000-check properties and a
50-seed sweep passed. One new nit, accepted and fixed: the first version of fix 1 kept the scrub
dropping a shard from the inventory on any read error, which this section wrongly said let a rebuild
rewrite it. It could not, and a rebuild that could not store ran on every maintenance tick (10
failures in 10 s in both of the reviewer's schedules; safety was unaffected). Two changes: the scrub
leaves the inventory entry alone on a read error; and a rebuild that fails while the holder's own
shard reads back intact now enters that shard in the inventory instead of trying again every tick
(this second loop predates the round: with m=0 a rebuild can never succeed without the holder's own
shard, so an intact shard not yet scrubbed was retried until the scrub reached it).
`TestConfirmAReadErrorOnAnIndexedShardStartsNoRebuild` fails without the two changes (10 and 10
needless rebuilds) and passes with them (0 and 1). Still true and stated: a shard that is not yet in
the inventory and whose reads keep failing is retried, and fails, once per maintenance tick until the
disk answers.
