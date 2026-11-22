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
| `records.go` | the node's metadata encodings: object record (content type + storage manifest), bucket record (initial region, quota limits), staged record (migration copy); reserved system names (`~bucket`, `~live`, `~reg/<saga>`, `~retired`, `~flip`, `~claim/<region>`, `~stage/<saga>`, `~cv/<saga>/<version>`, `~gc/<version>`, `~orphan/<version>/<node>`, object-level `~lost/<version>`) |
| `meta.go` | `metaRepairer`, the `merkle.Repairer` over every OCC key (metadata anti-entropy); node RPCs (meta fetch, pointer view, purge of a version list); the two DST canary switches |
| `inventory.go` | per-bucket shard inventory: scrub, expected-vs-actual Merkle comparison, rebuild from k shards, the collection rule of section 10.3 (dead versions, recorded orphans, lost-object declaration), inbound shard-RPC hook (shard deletes refused), purge |
| `migrate.go` | `migrator`, the real `saga.MigrationOps` (section 10.2 for the repaired protocol) |
| `status.go` | `StatusRows`, the node's rows of the HTTP status table |

### internal/noderuntime (new in the repair; these files were in internal/node)
| File | What it is |
|---|---|
| `frame.go` | TCP frame codec (length, version, kind, request id, sender, body / code, message) and the UDP datagram header; `MaxFrame` (= `node.MaxFrame`), `MaxDatagram` |
| `vfs.go` | the small file-system interface under the file disk; `osFS` |
| `realenv.go` | wall clock with loop-posted timers (`realTimer`), `fileDisk` (section 10.7) |
| `runtime.go` | `Runtime`: the real environment, the node loop, socket readers, connection handling, outbound requests, off-loop name lookup, panic handling (`Done`, `Err`), start/shutdown |
| `service.go` | `Runtime` as `storage.Service` (every call posted to the loop; cancellation states of section 10.9), `Inspect` |

### internal/storage (added files)
| File | What it is |
|---|---|
| `api.go` | object-service types (`Condition`, `ObjectInfo`, `PutRequest`, `BucketSpec`, `QuotaInfo`, `TagInfo`, `ACLInfo`, `MigrationInfo`, `MemberInfo`, `Health`, `Counter`), the `Service` interface, API sentinels `ErrNoSuchBucket`, `ErrBucketExists`, `ErrBusy`, `ErrMethod` on codes 160–163 |
| `http.go` | the HTTP handler (routes below), the one status table, `Limits`, `Server` (start, address, shutdown) |

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
| HTTP server (`storage.Server`) | `Server.Start`, `wg.Go` | `Server.Shutdown` | calls `Runtime` service methods, which post to the loop |
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
manifest arrives collects live shards; repair rebuilds them if k others survive.

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

## 4. Claims table

| Claim the book may publish | Test | Measured |
|---|---|---|
| The joined code table has no collision; every code round-trips to its sentinel; unknown → `ErrRemote` | `TestJoinedCodeTableHasNoCollision`, `TestEveryCodeRoundTripsToItsSentinel`, `TestNodeAndAPICodesStayInTheirBlock` | 38 application codes + 8 env codes |
| No GET returns wrong bytes; every acknowledged PUT reads back after heal; metadata and membership converge (21 nodes, 3 regions, 1% loss, region partition, crash) | `TestClusterPutGetDST` | 500 seeds: every ack read back; converge p50 3.3 s, max 10.2 s |
| Concurrent writes on both sides of a partition converge to one version per key on every node; every acknowledged metadata write is current, causally superseded or named as a loser in a discard record (oracle independent of occ's classifier) | `TestClusterPartitionHealDST` | 500 seeds: discard events p50 520 (310–715), distinct losers p50 67 (41–95); converge p50 7.5 s, max 12.2 s |
| After a quota discard the usage is recounted from the manifests and matches them on every node | `TestClusterPartitionHealDST`; canary `TestCanaryNodeSkipsDiscardRecount` | 500 seeds: overshoot p50 1,200 B, max 1,500 B over an 800 B limit; usage matches p50 8.5 s, max 12.2 s after heal; recount writes p50 24 (6–43) across 21 nodes |
| The losing version's shards are collected after the grace; no live version's shard is ever collected | `TestClusterPartitionHealDST`, every DST (`checkShardsReferenced`) | 500 seeds: loser shards collected p50 30 (18–52) per run; zero live versions collected (invariant every 1,000 events and at the end) |
| A deleted object stays deleted through anti-entropy, a partition heal and a crashed node rejoining | `TestClusterCrashRestartDST`; canary `TestCanaryDeleteResurrected` | deleted 4 per run |
| A restarted node gets its missing shards back; a wiped node is refilled | `TestClusterCrashRestartDST` | 500 seeds: restarted node whole again p50 6.1 s, max 8.9 s after restart; wiped node refilled p50 15.1 s, max 17.9 s after the wipe |
| With up to m shards per stripe rotted every GET is correct and every rotted shard is rebuilt, moving the damaged bytes only | `TestClusterBitRotDST` | 500 seeds: 6–36 rotted shards per run, repaired p50 152 ms, max 159 ms; rebuilt bytes / rotted bytes = 1.000 on every seed |
| Chunk 5's 78-cell failure matrix holds on real components | `TestClusterMigrationFailurePointsDST` | 577 runs (78 cells × base seed + 500 sweep seeds spread over cells), all in their expected state; begin→terminal p50 6.1 s, max 10.7 s (bound 20 s) |
| A bucket that migrates away leaves no inventory running on the source; a stopped node runs no worker | `TestBucketMigratingAwayStopsItsWorkers`, `TestNodeRefusesMalformedAndPostStopTraffic` | |
| Object round-trip through the full path | `TestPropObjectRoundTripThroughTheFullPath` (rapid) | 2,000 checks at the gate |
| A duplicated request and a lost response never double-apply | `TestPropRemoteOperationsAreIdempotent` (rapid) | 2,000 checks at the gate |
| The same Node code runs on the simulator and on real sockets | `TestConformanceOnTheSimulator`, `TestConformanceOnLoopback` | 4 scenarios |
| Frame codec: malformed, truncated, oversized input is refused; never a panic | `TestFrameDecodersRejectMalformedInput`, `FuzzFrame` | 30 s at the gate |
| The frame limit fits a full Merkle reply and a shard request | `TestFrameLimitCoversMerkleRepliesAndShardRequests` | |
| One HTTP status table, tested row by row through real HTTP; on a real node for the rows a node produces | `TestStatusTableRowByRowThroughHTTP`, `TestStatusRowsThroughHTTPOnARealNode` | 24 + 11 rows |
| Arbitrary HTTP requests give a status in the table and never a GET that disagrees with the last acknowledged PUT | `FuzzObjectAPI` | 30 s at the gate |
| Oversized bodies (413), oversized headers (431), slow header/body clients are cut off by the timeouts | `TestServerBoundsHeadersBodiesAndSlowClients` | |
| Lifecycle: start, double start, use after close, double close, close during start-up, canceled start (synctest) | `TestRuntimeLifecycleInABubble`, `TestRuntimeCloseDuringStartup`, `TestRuntimeStartHonoursItsContext`, `TestRuntimeCallsBeforeStartAreNotReady` | |
| Cancellation mid put (leaves only collectable shards), get, migration start and status | `TestRuntimeCancelMidPutLeavesOnlyCollectableShards`, `TestCancelMidGetAndMidMigrationReturnContextErrors` | |
| Shutdown under load returns every call; shutdown during repair and restart on the same disk | `TestShutdownUnderLoadReturnsEveryCall`, `TestShutdownDuringRepairAndRestartOnTheSameDisk` | |
| No goroutine survives (leak check in `TestMain` of `internal/node` and `cmd/objnode`); an untracked goroutine fails it | `TestRealRuntimeLeavesNoGoroutineBehind`, canary `TestCanaryShutdownLeak` | |
| A loop panic stops the node (recovered and logged), datagram overflow drops and counts | `TestRuntimeRecoversAPanicOnTheLoop`, `TestRuntimeDatagramsToSelfAndQueueOverflow` | |
| Three Docker peers in three regions start, join, serve a put on one and a get on another, and refuse stale conditional writes | `agent_work/06-integration/cluster-smoke.sh` (host) | section 8 |

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
  replication lag and partitions is measured (section 8), corrected only on a quota discard (recount).
- Concurrent PUTs to one key from two nodes in one partition side may both be acknowledged (LWW).
- Pointer reads require every live peer (in the coordinator's view) to agree: a peer that is up but
  unreachable keeps delete-source retrying until it is declared dead or heals.
- One TCP connection per request; no connection pooling.
- (Repair) `ErrStaleRequest` removed. Strays: a write a non-live peer acknowledged before the flip may
  live in the old region after a migration (readable; moved by the next migration). Disk I/O runs on
  the node loop (`env.Disk` is synchronous). A shard collected as an orphan whose write later appears
  is declared lost with a record (10.3). Tombstones, GC markers, intents' tombstones, orphan and loss
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
- `internal/storage/http.go`: the JSON encoder and `ResponseWriter.Write` errors (the client is gone; the
  status is already sent).
- `internal/node/runtime.go`: `net.Conn.Close` on refused, untracked, timed-out or finished connections;
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
   `merkle.Worker`); (c) the HTTP API is served by `storage.Server` inside the objnode process and calls
   `node.Runtime` through `storage.Service`; (d) OCC push-pull still runs alongside the Merkle path.
6. **PLAN wording that turned out wrong or needs narrowing.** "Shards no manifest refers to are collected
   after a grace period" (7.2) is unsafe as written (section 3.4); the as-built rule is evidence-based
   with a long orphan grace. "Manifest tree per bucket" (7.4): see 5(a). The Chunk 0a cluster profile's
   flags (`-gossip`, `-rpc`) never existed.
7. **Dockerfile / dev service**: no change needed.

## 8. Measurements

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
