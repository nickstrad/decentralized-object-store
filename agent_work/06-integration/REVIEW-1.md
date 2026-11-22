VERDICT: FAIL

## Findings

### 1. BLOCKER — Live write acknowledged before migration `Flip` is permanently destroyed by `Retire`/`opPurge`
- **File & lines:** `internal/node/migrate.go:450-501` (`Pointer`), `internal/node/migrate.go:368-428` (`keep`, `purgeRegion`), `internal/node/migrate.go:531-579` (`Retire`), `internal/node/meta.go:106-120` (`opPointer`).
- **What is wrong:** After `Flip` writes `route = m.To`, `Pointer` queries `opPointer` on every live peer and checks only that `rd.Route(bucket) == m.To`. It never checks that the coordinator's OCC store has received all bucket manifests written on other peers just before `route = m.To` reached them. Catch-up `Copy`, `Root(From)`, `Root(To)`, `Retire`, and `keep` all read only the coordinator's local OCC store (`g.n.liveObjects(rd, m.Bucket)`). If any live peer committed an acknowledged `PutObject` just before receiving `route = m.To` and that manifest has not yet reached the coordinator, `Root(From)` and `Root(To)` still match (both omit the object), and `Retire` calls `purgeRegion(m.Bucket, m.From)` with a `keep` set that omits the object's `VersionID`. Every node in `m.From` deletes the object's shards and records `n.purged[v] = true`. When anti-entropy later replicates the manifest from the writing peer, the object is live in metadata across the cluster, but all of its shards are permanently destroyed and every `GetObject` returns `storage.ErrUnavailable` forever.
- **Concrete reproduction:**
  1. Bucket `b` is routed to `r0` (`route=r0`, `~live=r0`). Coordinator `C` begins migration `r0 -> r1`.
  2. Step 3 `Flip` writes `route=r1` on `C`.
  3. Immediately before `route=r1` arrives at peer `P`, a client runs `PutObject("b", "k_late", data)` on `P`. `P` sees `writable() == true` (`route == ~live == r0`), writes shards (version `V_late`) into `r0`, commits manifest `k_late` to `P`'s OCC store, and returns 200 OK. `P`'s OCC delta datagram for `k_late` is dropped or sent to peers other than `C` (OCC period is 3 s, Merkle period is 1 s).
  4. `route=r1` arrives at `P`. Now every live peer returns `"r1"` from `opPointer`.
  5. Step 4 runs `Pointer` (succeeds), `Seal` (`~live=""`), catch-up `Copy` (misses `k_late`), `Root(From) == Root(To)` (both miss `k_late`), and `Retire` -> `purgeRegion("b", "r0")` without `V_late` in `keep`. All `r0` nodes delete `V_late`'s shards and add `V_late` to `n.purged`. `Promote` completes the saga as `OutcomeMoved`.
  6. Anti-entropy delivers manifest `k_late` (`V_late`, shards in `r0`) to all nodes. `GetObject("b", "k_late")` fails with `storage.ErrUnavailable` on every node permanently.
  - Existing tests missed this because `TestClusterMigrationFailurePointsDST` (`internal/node/dst_migration_test.go:57-70`) writes all objects and waits for full `h.converged()` before calling `Begin`.
- **Smallest fix:** Extend `opPointer` (`internal/node/meta.go:106-120`) to return both `rd.Route(bucket)` and a canonical SHA-256 digest of that peer's live manifests for `bucket` (e.g., sorted `(key, etag)` pairs). In `migrator.Pointer` (`internal/node/migrate.go:462-496`), require every live peer to match both `route` and the coordinator's bucket-manifest digest, returning `ErrUnsettled` otherwise. Once all live peers hold `route == m.To` (so no live peer will commit further writes) and agree on the bucket's manifest digest, the coordinator is guaranteed to hold every pre-flip acknowledged write before `Copy`/`Retire`/`purgeRegion`. Add a DST test with live `PutObject` traffic on non-coordinator peers during migration.

---

### 2. MAJOR — Broken LWW oracle in `TestClusterPartitionHealDST` mixes versions across all keys
- **File & lines:** `internal/node/dst_partition_test.go:199-224`; headline claim in `agent_work/06-integration/build-notes.md:198`.
- **What is wrong:** `covers(v, w)` (`dst_partition_test.go:48-50`) tests `v.ID() == w.ID() || v.Clock().Get(w.Dot().Actor) >= w.Dot().Counter`. In `internal/occ/store.go:445-452`, `Store.counter` is per-node across *all* keys, not per-key. In `commitPut` (`internal/node/objects.go:255, 265`), a node writes the object manifest with dot `(Actor, C)` and immediately calls `n.usage`, writing the bucket's quota key with dot `(Actor, C+1)`. In `dst_partition_test.go:199-215`, `pool` collects `d.loser` and `cur` from **all keys together** (manifests, tags, and quota keys) into one flat `[]occ.Version`, and line 221 checks `slices.ContainsFunc(pool, func(x occ.Version) bool { return covers(x, a.v) })` without filtering by `a.key`. Consequently, any later write by the same node to *any* other key (including the quota update immediately following `PutObject`) produces a version `x` in `pool` with `x.Clock().Get(a.v.Dot().Actor) >= C+1 > C`, making `covers(x, a.v)` return `true` even if manifest `a.v` disappeared with no discard record and no causal successor on `a.key`.
- **Concrete reproduction:** In `TestClusterPartitionHealDST`, suppress all `occ.KindManifest` entries from `h.discards` (simulating silent loss of every discarded manifest write). For any lost manifest write `a.v` written by node `N` at counter `C`, `N`'s quota write at counter `C+1` (or any subsequent tag/object write by `N`) is in `pool` and satisfies `covers(x, a.v) == true`, so the oracle fails to detect the silent loss.
- **Smallest fix:** Partition `pool` and `losers` by key:
  ```go
  pool := map[string][]occ.Version{}
  losers := map[string]map[occ.VersionID]bool{}
  ```
  Populate `pool[d.key]` and `losers[d.key][d.loser.ID()]`, append `cur` to `pool[a.key]`, and check `slices.ContainsFunc(pool[a.key], func(x occ.Version) bool { return covers(x, a.v) })` (and `!losers[a.key][a.v.ID()]`).

---

### 3. MAJOR — Permanent quota drift when a manifest-key LWW discard occurs without a quota-key discard
- **File & lines:** `internal/node/node.go:504-527` (`handleDiscard`), `internal/node/node.go:566-574` (`usage`).
- **What is wrong:** `handleDiscard` schedules `n.recountDue` only for `occ.KindQuota` discards, never for `occ.KindManifest` discards. Meanwhile, `n.usage` (`node.go:567`) returns early without touching the quota key when `dBytes == 0 && dObjects == 0` (same-size overwrite). When one side of a partition changes an object's size (updating the quota key) and the other side overwrites the same key with its original size (not touching the quota key), the manifest key suffers an LWW discard on heal while the quota key merges as a causal overwrite (zero `occ.KindQuota` discards). No recount is ever scheduled, so quota usage stays permanently wrong on every node.
- **Concrete reproduction:**
  1. Bucket `b` has `QuotaBytes = 500` and object `k` of size 100 B (`UsedBytes = 100, UsedObjects = 1`).
  2. Partition splits Side A and Side B.
  3. Side A overwrites `k` with 500 B at HLC `t1`: `delta = (+400, 0)`, updates quota key to `{500, 1}` (causally above `{100, 1}`).
  4. Side B overwrites `k` with 100 B at HLC `t2 > t1`: `delta = (0, 0)`, `n.usage("b", 0, 0)` is a no-op; quota key untouched.
  5. Partition heals. Manifest `k` settles to Side B's 100 B version (manifest discard). Quota key settles to Side A's `{500, 1}` (causal overwrite, no quota discard).
  6. Every node now holds 100 B of live data while reporting `UsedBytes = 500` forever, rejecting all further PUTs with `507 Insufficient Storage`.
  - `TestClusterPartitionHealDST` missed this because every object has fixed size `phObjectSize = 100` and both sides create new keys (`a0..a9`, `b0..b9`), always colliding on the quota key.
- **Smallest fix:** In `handleDiscard` (`internal/node/node.go:515-526`), for every `occ.KindManifest` discard (both live and tombstone losers), also schedule a recount when `!canaryNoDiscardRecount`:
  ```go
  if !canaryNoDiscardRecount {
      n.recountDue[parts[0]] = n.e.Clock.Now().Add(n.cfg.RecountSettle)
  }
  ```
  `RecountQuota` writes nothing if usage already matches. Add a test for the unequal-size vs same-size partitioned overwrite above.

---

### 4. MAJOR — `fileDisk.Write`/`Sync` renames before fsyncing the temp file and omits ancestor directory fsyncs
- **File & lines:** `internal/node/realenv.go:114-134` (`Write`), `internal/node/realenv.go:163-182` (`Sync`); `agent_work/06-integration/build-notes.md:177-179`.
- **What is wrong:**
  1. `fileDisk.Write` calls `os.WriteFile(tmp, data, 0o644)` and immediately `os.Rename(tmp, p)` without fsyncing `tmp`. This violates `PLAN.md 7.2` ("written as temp file -> fsync -> rename -> directory fsync"). If power is lost between `Write` and `Sync` (or before `f.Sync()` finishes in `Sync`), `p` has already replaced the previous durable file (such as `occ/state`) with an unsynced inode, which on POSIX/Linux filesystems can recover zero-length or corrupt, destroying the previously durable state.
  2. `Write` and `Append` call `os.MkdirAll(filepath.Dir(p), 0o755)` (creating up to 4 nested directories for `shards/<bucket>/<hh>/<version>/`, plus `occ/` and `saga/`), but `Sync` only calls `syncDir(filepath.Dir(p))`. On Linux, fsyncing only the leaf directory does not make newly created ancestor directory entries durable across a power loss.
  3. The build-notes claim (`build-notes.md:178-179`) "a crash therefore loses exactly the unsynced writes" is inaccurate for both crash models: on power loss, rename-before-fsync can corrupt an existing durable blob rather than reverting to it; on a process crash (`SIGKILL`, Chunk 7 Tier R), the already-renamed file remains visible in the OS page cache rather than being lost.
- **Concrete reproduction:** Overwrite `occ/state` via `Write` -> power-cut before `Sync` completes `f.Sync()`: `os.Rename` has already unlinked the prior durable `occ/state` while the new inode's data blocks are not flushed. Similarly, write the first shard in a new bucket `shards/b/00/<ver>/0.0` and call `Sync`: `shards`, `shards/b`, and `shards/b/00` are never fsynced.
- **Smallest fix:** In `fileDisk.Write`, create `tmp` via `os.OpenFile`, write `data`, call `f.Sync()` and `f.Close()`, then `os.Rename(tmp, p)` (cleaning up `tmp` on error). In `fileDisk.Sync`, walk parent directories from `filepath.Dir(p)` up to `d.dir` and `syncDir` each one (or sync each directory created by `MkdirAll`). State in `build-notes.md` that OS page-cache semantics mean a `SIGKILL` process crash can retain unsynced writes whereas power-loss atomicity is preserved by fsync-before-rename.

---

### 5. MAJOR — `If-Match: ""` (or whitespace-only `If-Match`) silently degrades to an unconditional PUT or DELETE
- **File & lines:** `internal/storage/http.go:161-173` (`condition`), `internal/node/objects.go:120-140` (`precondition`).
- **What is wrong:** `condition` reads `if v := r.Header.Get("If-Match"); v != ""` and sets `c.IfMatch = strings.Trim(strings.TrimSpace(v), `"`)`. When a client sends `If-Match: ""` (quoted empty string), `If-Match: """"`, or `If-Match:   ` (whitespace), `v` is non-empty, but `c.IfMatch` becomes `""`. `precondition` then skips `case c.IfMatch != "":` and returns `occ.Unconditional()`, silently overwriting or deleting the object instead of rejecting the malformed ETag with `400 Bad Request`.
- **Concrete reproduction:**
  ```
  PUT /b/k (body "v1") -> 200 OK
  PUT /b/k -H 'If-Match: ""' (body "v2") -> returns 200 OK and overwrites "v1" (want 400)
  DELETE /b/k -H 'If-Match: ""' -> returns 204 No Content and deletes the object (want 400)
  ```
- **Smallest fix:** In `internal/storage/http.go:169-171`, check `if len(r.Header.Values("If-Match")) > 0` (or `v := r.Header.Get("If-Match"); v != ""`), strip whitespace and at most one pair of surrounding quotes, and if the result is `""` return `fmt.Errorf("storage: empty If-Match: %w", ErrInvalid)`. Add rows for `If-Match: ""` and `If-Match: "   "` to `TestHTTPObjectAPIAgainstTheModel`.

---

### 6. MAJOR — GC-safety continuous invariant is neither independent nor cluster-wide; bit-rotted orphan shards leak forever
- **File & lines:** `internal/node/helpers_test.go:70-102` (`noLiveVersionCollected`), `internal/node/helpers_test.go:200-253` (`checkShardsReferenced`), `internal/node/inventory.go:138-156, 198-219`.
- **What is wrong:**
  1. **Invariant depends on product bookkeeping and only checks local metadata (Question 7):** `noLiveVersionCollected` iterates `n.Collected()` — the node's own in-memory ring buffer (capped at 4,096 entries, wiped on crash/restart, and never populated by `purge` or any deletion path outside `collect`) — and checks `live[c.Version]` only against `n.OCC().Reader()` (the *same* node's own metadata). During a partition, a shard's manifest is live on *other* nodes, not on `n`, so `noLiveVersionCollected` cannot detect `n` collecting live data while partitioned. If `n` crashes before heal (or if `V` is overwritten before reaching `n`, or if a bug deletes via a path that skips `n.collected`), the violation goes completely undetected. The claim in `build-notes.md:200` ("zero live versions collected (invariant every 1,000 events and at the end)") is therefore not backed by an independent invariant.
  2. **Corrupt orphan shards are never collected (Question 7):** In `inventory.go:144-150`, when `scrubShard` reads a bit-rotted shard (`ErrCorrupt`), it removes the key from `inv.actual` without deleting the file. If that shard belongs to a dead/unreferenced version (`!d.HasLocal`), `merkle.Compare(want, inv.actual)` sees `!d.HasLocal && !d.HasRemote` (no diff), so `n.collect` is never called and the corrupt file stays on disk permanently. Worse, once no healthy shards remain in that bucket, `runInventories` (`inventory.go:131-134`) deletes `n.inv[b]` (`inv.actual.Len() == 0`), and on the very next tick `localBuckets()` sees the corrupt file on disk, recreates `n.inv[b]`, scrubs it, and deletes it again in an endless `InventoryStart`/`InventoryStop` loop.
  3. **Related edge case (`inventory.go:206-211`):** Because `inv.orphans[d.Key]` starts counting as soon as a shard is locally unreferenced (e.g., while waiting for a late manifest), if the object is overwritten while the holder was waiting `>= GCGrace` for the manifest, the arrival of `~gc/<version>` triggers immediate collection with zero post-death grace against in-flight readers.
- **Concrete reproduction:**
  1. For (1): plant a bug that deletes a live shard via `n.store.Store().Delete(id)` without appending to `n.collected`, or collect a live shard on a partitioned node `n` and then crash/restart `n` before heal: `noLiveVersionCollected` passes at every step and at the end.
  2. For (2): PUT object `k`, DELETE object `k`, call `s.BitRot(holder, shardName)` before `GCGrace` elapses, then advance past `GCGrace + OrphanGrace`: `scrubShard` drops the key from `inv.actual` and the corrupt blob remains in `s.Disk(holder).Names()` forever.
- **Smallest fix:**
  1. Make the continuous invariant independent of `n.Collected()`: snapshot durable shard blobs on `h.s.Disk(id)` (or compare `h.s.Disk(id).Names()` against the union of live manifests across all up nodes, excluding nodes that were wiped or versions intentionally purged during migration) so any unexpected disappearance of a live version's shard from a healthy holder fails immediately.
  2. In `checkInventory`, track unreferenced disk blobs from `names` (all shard files on disk for the bucket) rather than only `inv.actual` keys, or delete corrupt blobs in `scrubShard` once they are unreferenced and `n.dead(rd, name)` / past grace.

---

### 7. MAJOR — Lifecycle and cancellation tests do not test what their names and claims state; `call` can execute already-cancelled mutations
- **File & lines:** `internal/node/runtime_lifecycle_test.go:57-74` (`TestRuntimeCloseDuringStartup`), `internal/node/runtime_lifecycle_test.go:100-151` (`TestRuntimeCancelMidPutLeavesOnlyCollectableShards`), `internal/node/runtime_ops_test.go:21-62` (`TestCancelMidGetAndMidMigrationReturnContextErrors`), `internal/node/service.go:40-46` (`call`); `agent_work/06-integration/build-notes.md:214-215`.
- **What is wrong:**
  1. **`TestRuntimeCloseDuringStartup` never closes during startup:** Lines 62-70 block the goroutine on `<-block` *before* calling `rt.Start`, let `rt.Close()` run to completion first, and only then close `block`. It only tests `Start` *after* `Close`, never a concurrent `Close` while `Start` is in `stateStarting`.
  2. **`TestRuntimeCancelMidPutLeavesOnlyCollectableShards` never creates an orphan shard or exercises GC:** `rt.post(func() { <-hold })` pauses `rt.loop` *before* `PutObject` runs; `cancel()` cancels the caller while `PutObject` is still queued in `rt.ctrl`; then `close(hold)` unblocks `rt.loop`, which executes `rt.node.PutObject` to full completion on the solo `NoNetwork` (`k=1, m=0`) node and commits the manifest! `HeadObject` succeeds (`live == 4`, `len(names) == 4`, `orphans == 4 - 4 == 0`). Zero orphan shards are ever left behind, and GC collects nothing.
  3. **`TestCancelMidGetAndMidMigrationReturnContextErrors` never cancels mid-flight:** Lines 34-35 and 42-44 pass an already-cancelled / already-expired context *before* invoking `GetObject`, `StartMigration`, and `Migration`.
  4. **`service.go:40-46` executes already-cancelled mutations:** `call` enters `select { case rt.ctrl <- f: case <-ctx.Done(): }` without checking `ctx.Err()` first. When `ctx` is already cancelled and `rt.ctrl` has buffer space (almost always), Go picks uniformly at random between the two ready cases — so ~50% of already-cancelled `PutObject`, `DeleteObject`, `CreateBucket`, or `StartMigration` calls still enqueue and execute on `rt.loop` while returning `context.Canceled` to the caller.
- **Concrete reproduction:**
  - In `TestRuntimeCancelMidPutLeavesOnlyCollectableShards`, assert `n.Stats().Collected > 0` or `isNotFound(headErr)`: both fail because the put actually committed.
  - In `service.go:call`, call `rt.CreateBucket(canceledCtx, ...)` 20 times: roughly half create the bucket while returning `context.Canceled`.
- **Smallest fix:**
  1. In `internal/node/service.go:call`, add `if err := ctx.Err(); err != nil { return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, err) }` before the `select`.
  2. Fix `TestRuntimeCloseDuringStartup` to pause `Start` after transitioning to `stateStarting` (e.g., via a test hook or blocked `ctrl` before `node.Start`) and call `rt.Close()` while `Start` is in progress.
  3. Fix `TestRuntimeCancelMidPutLeavesOnlyCollectableShards` so shards are actually written to disk while the put fails to commit (e.g., cancel mid-put against a slow/blocked peer or conflicting precondition so shards exist without a manifest, assert `len(names) > 0 && isNotFound(headErr)`, advance past grace, and assert `len(names) == 0` and `Collected > 0`).
  4. In `TestCancelMidGetAndMidMigrationReturnContextErrors`, cancel contexts while requests are actually in flight (e.g., blocked peer or paused loop) rather than pre-cancelling.

---

### 8. MAJOR — Real-runtime hazards: synchronous DNS lookup on the node loop; loop panic leaves UDP/TCP sockets and goroutines alive
- **File & lines:** `internal/node/runtime.go:441-448` (`realNet.Send`), `internal/node/runtime.go:272-290` (`loop` panic recovery).
- **What is wrong:**
  1. **Blocking DNS on the single-threaded node loop (`runtime.go:443`):** `realNet.Send` calls `net.ResolveUDPAddr("udp", string(to))` synchronously on `rt.loop` with no timeout or context. Because `rt.loop` owns all protocol state, timers, and `Shutdown`'s `Node.Stop()`, a slow DNS lookup or unresolvable peer hostname (which can be injected via an unauthenticated gossip packet or misconfigured seed) stalls the entire node.
  2. **Loop panic does not stop sockets/listeners on a networked runtime (`runtime.go:273-279`):** When `rt.loop` recovers a panic, it sets `rt.panicked`, logs, calls `rt.cancel()`, and exits — but leaves `rt.udp` and `rt.tcp` open and `rt.state == stateRunning`. `readDatagrams` stays blocked in `rt.udp.ReadFromUDP` and `accept` stays blocked in `rt.tcp.Accept` (continuing to accept TCP connections, read frames, fail `rt.post`, and close them) until an external caller happens to call `rt.Close()`. This violates `PLAN.md 2.6` ("At the node boundary the real runtime recovers, logs, and stops that node"). `TestRuntimeRecoversAPanicOnTheLoop` (`runtime_more_test.go:114-132`) missed this because it used `NoNetwork: true` and explicitly called `rt.Close()`.
- **Concrete reproduction:**
  1. Start a networked `Runtime` (`NoNetwork: false`), trigger a panic on `rt.loop`, wait without calling `rt.Close()`: `rt.tcp` continues accepting TCP connections and `readDatagrams`/`accept` remain alive indefinitely.
  2. On a networked `Runtime`, send a datagram to an unresolvable hostname whose DNS server stalls: `rt.loop` freezes for the full OS resolver timeout (blocking `Health`, `PutObject`, timers, and `Shutdown`).
- **Smallest fix:**
  1. For `realNet.Send`: parse numeric `host:port` with `netip.ParseAddrPort` on the fast path; for hostnames not yet cached in `rt.addrs`, resolve asynchronously in a `rt.wg.Go` helper using `net.DefaultResolver.LookupHost` with a bounded timeout derived from `rt.ctx` and post the cached result back to `rt.loop` (dropping the single UDP datagram while resolution is pending).
  2. In `rt.loop`'s panic `defer`, close `rt.udp`, `rt.tcp`, and all tracked `rt.conns` under `rt.mu` (and stop timers once safe, or transition `state` and close sockets so `readDatagrams`/`accept` exit immediately without waiting for `Close`). Test panic recovery on a networked `Runtime` without calling `Close` first to verify sockets close and reader/acceptor goroutines exit.

---

### 9. MAJOR — Concurrent migrations lose registrations under LWW (`~reg` single key); routed bucket creation has an unrouted-placement window
- **File & lines:** `internal/node/migrate.go:140-145, 167-174, 515-520` (`~reg`), `internal/node/objects.go:81-86, 385-402` (`bucketState.writable`, `CreateBucket`).
- **What is wrong:**
  1. **Single-key `~reg` drops concurrent registrants under LWW:** `claim` writes all active migration saga IDs into one OCC key `~reg` (`encodeSet`) via unconditional `SetScalar`. If two coordinators start dueling migrations of the same bucket across a partition, their `~reg` writes (`[A]` vs `[B]`) collide under LWW and one registration is discarded. After heal, the surviving coordinator sees only itself in `~reg` during `Seal` and proceeds to seal and retire the source while the other migration is still active. (`docs/knowledge/chunk-05-saga.md:43-44` specifically warns about concurrent registration consistency.)
  2. **Unrouted-placement window after `CreateBucket` with `?region=`:** `CreateBucket` writes three separate OCC keys (`~bucket`, `Route`, `~live`), each broadcast as its own single-key UDP delta datagram. On another node that receives `~bucket` (carrying `rec.Region = "r0"`) before `Route` arrives, `bs.route == ""` and `bs.hasLiv == false`. `bucketState.writable()` (`objects.go:81-86`) checks `if b.route == "" { return !b.hasLiv || b.live != "" }` -> `true`, and `PutObject` calls `n.scope("")`, placing shards of a region-routed bucket across **all regions** instead of refusing with `ErrBusy` until `Route` and `~live` arrive.
- **Concrete reproduction:**
  1. Partition two nodes; run `Claim` for saga `A` on side 1 and saga `B` on side 2 on bucket `b`; heal: `~reg` holds only one ID, so `Seal` does not detect the second registrant.
  2. Create bucket `b` with `Region: "r0"` on node 0; deliver the `~bucket` delta to node 1 while dropping the `Route` and `~live` deltas; call `PutObject` on node 1: shards are placed across `r0`, `r1`, `r2` instead of `r0`.
- **Smallest fix:**
  1. Replace the single `~reg` set key with one key per saga (`~reg/<saga>`), written in `claim`, deleted in `deregister`, and scanned by prefix in `Seal` — concurrent registrations then commute under LWW without losing any registrant.
  2. In `bucketState.writable()` (`internal/node/objects.go:81-86`), check `if b.rec.Region != "" && b.route == "" { return false }` so a routed bucket is never writable before its routing pointer arrives.

---

### 10. NIT — `migrator.Copy` invokes its callback synchronously on empty buckets or immediate reader errors
- **File & lines:** `internal/node/migrate.go:220-269` (`Copy`).
- **What is wrong:** `Copy` calls `step(0)` directly on line 268. When a bucket is empty (`len(objs) == 0`) or `g.n.reader()` fails on `i == 0`, `finish` invokes `done(...)` synchronously inside `Copy`, violating the brief's rule that callbacks never run inside the call that registered them (which every other `migrator` method and landed package keeps).
- **Concrete reproduction:** Call `n.mig.Copy(c, m, done)` on an empty routed bucket: `done` runs before `Copy` returns.
- **Smallest fix:** Change line 268 from `step(0)` to `g.n.later(func() { step(0) })`.

---

### 11. NIT — Test-claim and coverage gaps (`Health().RecentDiscards`, `node.StatusRows()`, `TestClusterBitRotDST`, `TestPropRemoteOperationsAreIdempotent`, `matrixSeeds`, dead code)
- **Files & lines:**
  - `internal/node/objects.go:636-639` (`Health`): no test checks `Health().Discards > 0` or `Health().RecentDiscards` after an LWW discard (`TestClusterPartitionHealDST` inspects only the test-only `DiscardSink`). Also `Key: fmt.Sprintf("%q", d.Key)` double-quotes keys in JSON.
  - `internal/storage/http_test.go:364-391` & `internal/node/runtime_ops_test.go:131-202` vs `build-notes.md:211`: the claim says `24 + 11 rows` tested through HTTP, but `TestStatusTableRowByRowThroughHTTP` only iterates `storage.StatusTable()` (24 rows) and `TestStatusRowsThroughHTTPOnARealNode` hits only 1 of the 11 `node.StatusRows()` rows (`ErrNoMigration`). In addition, `node.ErrUnknownSaga` (`internal/node/codes.go:20`) is never returned anywhere (`Node.Migration` returns `saga.ErrSagaNotFound`) and is not listed as unused in `build-notes.md` (unlike `ErrStaleRequest`).
  - `internal/node/dst_bitrot_test.go:138-160`: `TestClusterBitRotDST` logs `rebuilt_per_rotted_x1000` but never asserts `res.rotted > 0` or any bound on `res.rebuiltBytes` relative to `res.rottedBytes`.
  - `internal/node/prop_test.go:163-175`: the `seen[name] > 1` loop over `Disk.Names()` is vacuous (cannot fail because `Names()` returns sorted map keys), and `lost > 0` is never asserted.
  - `internal/node/dst_migration_test.go:214-225`: `matrixSeeds` runs only `seeds[0]` on all 78 cells; if multiple regression seeds are pinned in `testdata/seeds.txt`, pinned seeds `seeds[1..]` run only on one cell (`j % 78`), not necessarily the cell where they failed. Also `build-notes.md:279` says `578` runs while lines 204 and 323 correctly say `577`.
  - `internal/node/runtime.go:456-506`: `realNet.Request` does not return `env.ErrInvalidArgument` for `timeout <= 0` (unlike `sim`), and skips the `MaxFrame` check for self-requests (`to == rt.cfg.ID`).
  - `internal/node/node.go:207, 246`: `Node.orphanSince` is allocated and never read or written (dead field).
  - `internal/node/node.go:496-527`: `n.recountDue` and `n.losers` are in-memory only and `onDiscard` defers `handleDiscard` via `n.later` *after* `occ.Store.drain()` clears the persisted outbox on disk, so a crash between `drain()` and `runRecounts()` on the sole node observing a discard drops the pending recount (low-probability edge case since other peers normally observe the discard too, worth noting in limits).
- **Smallest fix:** Add a `Health()` check in `TestClusterPartitionHealDST`; test all `node.StatusRows()` rows through `httptest` in `internal/node`; assert `res.rotted > 0 && res.rebuiltBytes == uint64(res.rottedBytes)` in `TestClusterBitRotDST`; remove the vacuous `seen` check in `prop_test.go`; run all pinned seeds across all 78 cells in `matrixSeeds`; enforce `timeout <= 0` and `MaxFrame` on self-requests in `realNet.Request`; delete `Node.orphanSince` and either return `ErrUnknownSaga` or document/remove it.

---

### 12. NOTE — Review answers to questions 6, 8, 11, and 12 (rules, deletes, one-core design, open orchestrator decisions)
- **Question 6 (Rules):** Verified clean. Standard library only in product code; zero comments in new Go files (only `//go:embed help.txt` in `cmd/objnode/main.go`); zero `panic(` calls in new code; frozen packages (`internal/env`, `internal/sim`, `internal/sim/simtest`) and landed packages (`gossip`, `erasure`, `merkle`, `saga`, landed `storage` files) are untouched; the single landed-package addition (`internal/occ/node_merge.go`, 8 lines) is minimal, tested, and covered by `detlint`. The 8 protocol-core files in `internal/node` contain no goroutines, channels, mutexes, wall clock, global rand, floats, or unsorted map iterations.
- **Question 8 (Deletes & tombstone retention):** Deletes are OCC tombstones included in both OCC push-pull and the metadata Merkle tree (`metaRepairer`), and `TestCanaryDeleteResurrected` fails for the planted reason (`res.resurrected == true`). Tombstones (and `~gc/` markers) are retained **indefinitely** (no collection/expiry), and this rule plus its unbounded-metadata-growth cost is explicitly stated in `build-notes.md:107-108, 243`. Because tombstones never expire, there is no post-retention resurrection hazard for a node down longer than a retention window (and `internal/occ` has no tombstone-GC API); note only that `PLAN.md 7.5` mentions a retention period, so the book/plan wording should match the indefinite-retention rule actually built. In `dst_crash_test.go`, adding a check that `crashed` actually held `k0..k3` before crashing at `t=10s` would make the old-disk rejoin guarantee explicit.
- **Question 11 (One protocol core, two environments):** Verified clean. `Node` is constructed solely from `env.Env`; there is no `if simulated` branch anywhere in `Node`. The full-node DSTs (`helpers_test.go:67`), conformance tests (`conformance_test.go`), and `cmd/objnode` (`runtime.go:208-209`) all run the exact same `*node.Node`. (`RuntimeConfig.NoNetwork` lives only on `Runtime` for `synctest` bubbles.)
- **Question 12 (Judgement on the builder's three open decisions):**
  - **(a) Migration matrix sweep shape:** The claims-table row (`build-notes.md:204`, "577 runs (78 cells x base seed + 500 sweep seeds spread over cells)") is worded accurately (fix the off-by-one `578` on line 279). Running 577 full 21-node cluster simulations (1,154 runs with determinism doubling, ~7-8 seeds/cell) is enough for Chunk 6 landing provided Chunk 7 hardening runs a deeper matrix sweep and `matrixSeeds` is fixed so pinned seeds run on all cells (Finding 11).
  - **(b) 14-minute 500-seed sweep on `internal/node`:** Acceptable as a whole-system integration ceiling (839 s standalone vs ~600 s guideline), though trimming `TestClusterPartitionHealDST`'s partition window/rounds slightly (e.g., 6 contested rounds instead of 10, shorter `GCGrace`) would bring it under 10 minutes without cutting node count, seeds, or invariants.
  - **(c) What else `agent_work/gates/gate.sh` should check for Chunk 6:**
    1. Add `internal/node` and `internal/storage` to `floor_pkgs` (and check `api.go`/`http.go` coverage specifically so the 94% landed chunk-store coverage cannot mask regressions in the HTTP layer).
    2. Run `agent_work/06-integration/cluster-smoke.sh` from the host side of `gate.sh` as a post-container check so the `cluster` compose profile is gated as PLAN 4.2 specifies.
    3. **Close the `detlint` gap on `internal/node`:** `internal/node`'s 8 protocol-core files (~2,700 lines of simulator-executed code) are currently completely outside `detlint` because `runtime.go`/`realenv.go`/`service.go` share the package. Either add `internal/node` to `detlint_dirs` with explicit file-level allowlist entries for the runtime files, or move the runtime files to a subpackage so `detlint` enforces determinism on `Node` in Chunk 7+.
    4. Add `cmd/objnode` to the `-race -count=3` loop alongside `internal/node` and `internal/storage`.

---

## Verification Summary

### Verified by running (inside Docker `dev` container only)
- `docker compose run --rm -T dev go test -count=1 ./internal/occ -run TestNodeMerge` — PASS (0.008 s).
- `docker compose run --rm -T dev go test -count=1 -cover ./internal/node ./internal/storage ./cmd/objnode ./internal/testutil/leakcheck` — PASS (`internal/node` 64.5 s, 85.9% coverage; `internal/storage` 6.4 s, 94.2%; `cmd/objnode` 0.08 s, 82.5%; `leakcheck` 0.14 s, 80.6%).
- `docker compose run --rm -T dev go test -count=1 ./internal/node -run 'Canary' -v` — PASS (23.5 s; `TestCanaryDeleteResurrected`, `TestCanaryNodeSkipsDiscardRecount`, `TestCanaryShutdownLeak` all passed).
- `docker compose run --rm -T dev go test -race -count=3 ./internal/storage ./cmd/objnode ./internal/testutil/leakcheck` — PASS (38.7 s, 1.2 s, 1.3 s).
- `docker compose run --rm -T dev go test -race -count=3 -run 'Runtime|Conformance|Frame|Status|Cancel|Shutdown|CanaryShutdown|Real|FileDisk|Loopback' ./internal/node` — PASS (23.0 s).
- `docker compose run --rm -T dev go test -count=1 ./internal/node -run 'Prop' -rapid.checks=2000` — timed out at 120 s (gate G2 in `result.json` ran it for 344 s across 8 packages).
- Host read-only git/grep checks: `git log main..HEAD --stat`, `git diff --stat main..HEAD`, grep checks for comments, `panic(`, `time.Sleep`, `range`, banned calls in `internal/node` protocol files, and `git status --short` (clean).
- Not run in this review (as instructed): the full 500-seed sweep and `agent_work/06-integration/cluster-smoke.sh` (reviewed via `agent_work/06-integration/gates/result.json`, `cluster-smoke.sh`, and `build-notes.md`).

### Verified by reading
- Every product and test file in `internal/node/` (all 12 product files and 18 test files), `internal/storage/api.go`, `internal/storage/http.go`, `internal/storage/http_test.go`, `internal/occ/node_merge.go`, `internal/occ/node_merge_test.go`, `cmd/objnode/main.go`, `cmd/objnode/help.txt`, `cmd/objnode/main_test.go`, `internal/testutil/leakcheck/`, `compose.yaml`, `agent_work/06-integration/cluster-smoke.sh`, and `agent_work/gates/gate.sh`.
- Interacting landed package code: `internal/occ/node.go`, `internal/occ/store.go`, `internal/occ/doc.go`, `internal/merkle/worker.go`, `internal/saga/migration.go`, `internal/saga/engine.go`, `internal/storage/node.go`, `internal/storage/chunkstore.go`.
- Specifications and notes: `PLAN.md`, `agent_work/06-integration/objstore-p2-06-integration-build.md`, `agent_work/06-integration/objstore-p2-builder-rules.md`, `agent_work/06-integration/build-notes.md`, `agent_work/06-integration/gates/result.json`, `agent_work/0b-simulator/build-notes.md`, `agent_work/0b-simulator/frozen-api.txt`, and `docs/knowledge/chunk-01-*.md` through `chunk-05-*.md`.
