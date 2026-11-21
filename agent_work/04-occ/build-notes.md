# Chunk 4 build notes — optimistic concurrency control for metadata (`internal/occ`)

Builder: Opus 5.5 (fresh seat), branch `chunk/04-occ`, 2026-11-21.
Spec: PLAN.md 4.2 "Chunk 4", 7.5, 2.2/2.3/2.6, OCC amendment in 9.1; brief
`agent_work/04-occ/objstore-p2-04-occ-build.md`.

History: tests first (`079bab7`, does not compile), then the implementation (`cd718fb`), then
coverage, benchmark and notes commits (see `git log`).

## What was built

| File | Contents |
|---|---|
| `errors.go` | sentinels `ErrPreconditionFailed`, `ErrMalformed`, `ErrNotFound`, `ErrConflict`, `ErrQuotaExceeded`, `ErrClockExhausted`, `ErrClockAhead`, `ErrNotReady`; typed `*ConflictError{Key, ETag, Siblings}` (unwraps to `ErrConflict`); wire codes 80–87 (`CodeEntries`, `Codes()` = `env.NewCodeTable(CodeEntries()...)`) |
| `vv.go` | `Actor{Node, Epoch}`, `Entry`, `VersionVector` (sorted, zero entries dropped) with `Compare` → `Equal/Before/After/Concurrent`, `Descends`, `Join`, `Get` |
| `hlc.go` | `Stamp{Wall, Logical, Node}` with a total `Compare`; `HLC` over `env.Clock` (`Now`, `Observe`, `Last`), `MaxClockLead = 1h` |
| `version.go` | `Dot`, `Version` (dot, causal context, tombstone flag, value, HLC stamp, cached `VersionID`), `Descends`, `Supersedes`, `Clock` |
| `record.go` | `Record` (sibling set, normalized), `ETag`, `ParseETag`, `Record.Merge`, `Covers`, `Live`; `Precondition` (`IfMatch`, `IfNoneMatch`, `Unconditional`, `Holds`) |
| `scalar.go` | `Scalar` LWW register: highest `Stamp`, then larger value bytes |
| `doc.go` | `Doc` (records + scalars), `Merge`, `MergeRecord`, `MergeScalar`, `Subset`, `Clone`, `Encode` (canonical, cached), `Digest`, strict `Decode` |
| `encoding.go` | encoder/decoder primitives (uvarint, zig-zag varint, length-prefixed strings) |
| `store.go` | `Store`: `Get`, `Put`, `Delete`, `SetScalar`, `Scalar`, `Merge`, `Record`; epoch handling |
| `quota.go` | `Usage`, `ReadQuota`, `WriteQuota`, `UpdateQuota(key, attempts, recount, fn)`, `AddUsage` |
| `meta.go` | typed document: `Kind`, key builders (`TagKey`, `ACLKey`, `QuotaKey`, `ObjectKey`, `ManifestKey`, `RouteKey`, `ContentTypeKey`, `BucketPrefix`), `ParseKey`, `Permission`, `ObjectMeta`, typed accessors on `Store`, `RecountObjects` |
| `wire.go` | message kinds, client op encoding, `Result` |
| `node.go` | `Membership` interface (declared here; Chunk 1's gossip implements it later), `StaticMembership`, `Config`, `Node` (`env.Process`), `NewFactory`, `Client` |

No CRDT type is built. No client replica (OQ-7). Only `internal/occ` was touched (plus this file).

## Design choices and why

**Dotted version vectors, not plain vectors.** Each sibling carries a *dot* (actor, counter) and a
causal *context* (the version vector of what its writer read). A plain vector cannot say "this
write by A did not see A's earlier write": an unconditional write by A after A's own blind write
would get `{A:2}`, which dominates `{A:1}` and would silently drop it. With a dot plus context,
the event set of a version is `downset(context) ∪ {dot}`, and the blind write's event set is just
`{(A,2)}`, which is concurrent with `{(A,1)}`.
`TestUnconditionalWriteStaysConcurrentWithItsOwnEarlierWrite` proves it.

**Dominance = strict inclusion of event sets.** `w.Supersedes(v)` iff `events(v) ⊊ events(w)`.
Strict set inclusion is a strict partial order on *any* pair of versions, including hostile
decoded ones (e.g. two versions whose contexts cover each other's dots: equal event sets, so
neither supersedes and both are kept). That is what makes `Merge = maximal(union by version ID)`
commutative, associative, idempotent and total on every decoded state without an error return.
The honest-run reading is the plan's: `v` is dropped only when a kept version's context covers
`v`'s dot, which means its writer's ETag covered `v` or a descendant of `v`.

**Dot counters are per key**: `max(counter of this actor in the record's dots and contexts) + 1`.
A conditional write's context is the join of the clocks of every sibling it covered (the record's
whole sibling set: an ETag names the whole set). The inflation of a dot into a downset (`(A,7)` →
`{A:7}`) is safe because whole records travel together. A replica holding `(A,7)` also holds A's
earlier versions of that key, or versions that supersede them.

**Version ID and ETag.** `VersionID = SHA-256("occ/version/v1" ‖ actor ‖ counter ‖ contentHash)`,
`contentHash = SHA-256(context ‖ tombstone ‖ stamp ‖ value)`; so a version ID names the whole
version. `ETag = SHA-256("occ/etag/v1" ‖ n ‖ sorted version IDs)`; the empty record has the zero
ETag. 32-byte SHA-256 everywhere; an ETag collision between different sibling sets is a SHA-256
collision.

**Preconditions** (`Precondition.Holds`):
- `If-Match: e` holds iff the record has at least one sibling (tombstones count) and its ETag is
  `e`. The new version covers every sibling, so a successful conditional write leaves exactly one
  version (this is also the resolving write).
- `If-None-Match: *` holds iff the record has no live sibling (absent, or tombstones only); the new
  version covers the tombstones.
- none: always holds; the new version covers nothing and is concurrent with every existing
  sibling (plan: "recorded as concurrent with what it did not read").
- `Delete` checks the precondition first, then returns `ErrNotFound` if there is no live sibling.
- A refused write changes nothing (checked in every table row and in the model property).
- `Get`: no live sibling → `ErrNotFound`; more than one sibling (a tombstone counts) →
  `*ConflictError`; else the value.

**Canonical encoding, strict decode.** `Decode` parses, rebuilds the normal form (sorted keys,
sorted/deduplicated/maximal siblings, normalized vectors) and requires `Encode(result) == input`.
It also rejects a zero dot counter, a tombstone carrying a value, a flag byte not in {0,1}, an
empty record, a logical counter above 32 bits, counts larger than the remaining input, and
trailing bytes. So every decoded state is canonical: `encode∘decode` and `decode∘encode` are
both the identity, and `Merge(a, a) == a` holds for every decoded `a`. Golden files:
`testdata/doc-v1.golden`, `state-v1.golden`, `op-v1.golden` (`-update-golden` to rewrite).

**Actor identity and the wipe hazard (the guarantee).** Actor = `(NodeID, Epoch)`, with `Epoch` a
non-zero `uint64`. The node stores the epoch in the same checksummed blob as its metadata
(`occ/state`: `"OCCS" ‖ SHA-256(payload) ‖ uvarint(epoch) ‖ doc`). On `Start`:
- the blob is intact → reuse its epoch and state;
- the blob is missing (new node, or wiped) or fails its checksum/decode (bit-rot) → take a fresh
  epoch drawn from the node's `env.Rand` (`node/<id>` stream). The simulator contract says that
  stream continues across incarnations, so a wiped-and-rejoined node draws a different value;
  in the real runtime the stream is randomly seeded;
- a read error (`ErrIO`) → do not guess. The node stays not ready (every op returns
  `ErrNotReady`, requests are refused with that code) and retries the load every `LoadRetry`
  (100 ms), as a named periodic round, until it reads the blob.

Guarantee: a node never issues a dot under an actor whose earlier counters it may have lost.
Counters are only ever lost together with the blob, and losing the blob forces a new epoch. The
exception is a collision of two random 64-bit epochs for the same node ID, probability
≈ k²/2⁶⁵ for k epochs. At counter overflow (a record whose max counter for this actor is
`MaxUint64`, reachable only through a hostile peer) the store rotates to a fresh epoch before
writing (`TestEpochRotatesAtCounterOverflow`).

`TestWipedNodeTakesAFreshEpoch` runs the classic hazard twice. Node A writes `k` twice (`(A,1)`
then `(A,2)`, which supersedes it) and the write replicates. A is isolated and wiped, then writes
`k` again with `If-None-Match` (its disk is empty), and the network heals. With epochs the new
write is a sibling of `(A,2)` on every node. With the test-only switch `hazardReuseEpoch` (epoch
fixed at 1 on a fresh start) the new write reuses `(A,1)`, is "dominated" by `(A,2)` and vanishes.
The test requires that loss to happen with the switch on, so the test can see the hazard.

**Durability limit (stated).** A write is acknowledged once it is synced to the writing node's
disk. If that disk is destroyed (wipe, or bit-rot of the state blob) before the write replicated,
the write is lost with the disk. That is disk loss, not protocol loss. The DSTs that assert
no-silent-loss therefore inject crashes, torn crashes, restarts, pauses and read errors, but not
wipes or bit-rot. The wipe and bit-rot paths are tested separately for the epoch guarantee.

**HLC.** `Now()`: if physical time is past the clock's wall, the stamp is `(physical, 0)`;
otherwise the logical counter goes up by one. `Observe(s)` raises the clock to `s` (wall,
logical), so the next `Now()` is strictly after every stamp observed.
- *Skew:* "last writer" means highest HLC, not latest in real time. A node whose clock runs ahead
  wins a scalar field over a write made later in real time on a slower node. Once the slow node
  has observed the fast stamp (any merge observes the doc's max stamp), its next write wins
  (`TestLWWScalarTieBreak/a node whose clock runs ahead ...`).
- *Far-future stamps:* `Observe` refuses (`ErrClockAhead`) a stamp more than `MaxClockLead`
  (1 h) ahead of the local physical clock and does not adopt it. The data still merges, and that
  far-future scalar keeps winning its field until real time passes it. Store and node count these
  (`AheadStamps`).
- *Logical overflow:* at `Logical == MaxUint32` the clock carries into the wall (`wall+1`,
  logical 0). At wall `MaxInt64` with a full counter, `Now` returns `ErrClockExhausted` and the
  write is refused. That is reachable only if a peer sends a stamp at the end of time while the
  local clock is also there (refused otherwise by the lead check).

**Scalars (LWW)**, used for the routing pointer and content type: highest `(wall, logical, node)`.
On a fully equal stamp (impossible between honest nodes) the larger value bytes win, so `Merge`
stays commutative.

**Quotas.** `ReadQuota` returns usage plus the precondition to write with (`If-None-Match` if
absent, else `If-Match` of the read ETag). `WriteQuota` refuses an unconditional write.
`UpdateQuota` loops up to `attempts` times: read → (on siblings: call the recount hook; the write
will be the resolving `If-Match`) → `fn` → conditional write. It retries only on
`ErrPreconditionFailed`, gives up with that error after `attempts`, and with siblings and no hook
returns `*ConflictError`. Nothing is guessed. `AddUsage` refuses wrap-around
(`env.ErrInvalidArgument`) and returns `ErrQuotaExceeded` when a growing update passes a non-zero
limit. `Store.RecountObjects(bucket)` is the hook this package can offer: it sums live
`ObjectMeta` sizes in a bucket. A conflicted object counts once at its largest live sibling size
(every sibling's data is still stored); a tombstoned one does not count. Chunk 6 plugs a manifest
recount into the same hook.

**Replicator (`Node`).** Single-threaded, acts only through `env`. Every `Period` (500 ms,
jittered ±50%) it sends a digest request to a random peer from `Membership.Peers()`. The peer
answers "match" or its full state. The caller merges, and if its own digest still differs from
the peer's state digest, it pushes its full state back (push-pull). After every local write the
node persists (Write + Sync), then sends the changed key as a delta datagram to `Fanout` (2)
random peers, if the delta fits in `MaxDelta` (60,000 bytes). Datagrams give the duplication and
reordering exposure; merges are idempotent. Client operations (get, put, delete with
precondition) arrive as requests (`Client`). Errors go back through `reply(nil, err)` and the
simulator's code table, so a remote caller sees `*env.RemoteError` that `errors.Is` both the occ
sentinel and `env.ErrRemote`. Typed fields of `ConflictError` do not cross the wire (code and
message only).

## Claims and the tests that prove them

| Claim | Test(s) |
|---|---|
| Vector `Compare` table incl. epochs, zero entries, duplicates | `TestVersionVectorCompareTable`, `TestVersionVectorBasics` |
| Vectors are a partial order; `Compare` matches a pointwise model; Concurrent symmetric; Join is the least upper bound | `TestPropVersionVectorPartialOrder` |
| Every precondition × state row (absent, live, tombstone, two live siblings, live+tombstone) for put and delete; refused writes change nothing; conditional writes supersede what they covered; unconditional ones keep it | `TestConditionalPutTable`, `TestConditionalDeleteTable` |
| Single replica: success iff the precondition holds against a reference model; sibling sets equal the model's; one set ↔ one ETag | `TestPropCheckAndSetModel` |
| ETag equal on equal sets regardless of merge order; distinct sets distinct; changes with a new version even with the same value; parse round trip | `TestETagDeterministic` |
| Concurrent writes become siblings; `Get` returns `*ConflictError`; a stale resolve fails; a resolve naming the ETag collapses them on every replica | `TestResolveCollapsesSiblings` |
| Blind writes stay concurrent with the writer's own earlier blind write | `TestUnconditionalWriteStaysConcurrentWithItsOwnEarlierWrite` |
| No silent loss: random conditional/blind writes and deletes, random merges, clock moves, on 2–4 replicas: every acknowledged version is a sibling or is seen by a kept sibling's writer (independent oracle, not the product's dominance), at every step across replicas and after full exchange; replicas converge to identical bytes and ETags; with only conditional writes, a key has at most as many siblings as distinct writing actors; scalars settle on the highest stamp | `TestPropNoSilentLoss`, fuzz `FuzzOCCConditionalWrite` (same property) |
| Merge commutative, associative, idempotent, keeps every non-dominated sibling (independent event-set oracle), invents nothing, keeps nothing dominated; on honest snapshots and generated hostile states | `TestPropMergeLaws`, fuzz `FuzzOCCMergeLaws` |
| Any delivery order with duplicates of full states and deltas → identical bytes, digest and ETags | `TestPropConvergence` |
| Causally ordered writes never leave siblings | `TestPropSiblingsOnlyWhenConcurrent` |
| HLC rules incl. backwards physical time, observe, lead limit, carry, exhaustion | `TestHLCRules`; monotonic and after every observed stamp: `TestPropHLCMonotonic` |
| LWW tie-break order; skew makes a later real-time write lose | `TestLWWScalarTieBreak` |
| Quota CAS: create, limit, stale read retried, bounded retries, partition siblings resolved only by the hook, unconditional write refused, wrap-around refused, canonical usage | `TestQuotaCheckAndSet`; a write from a stale read never applies, final usage = model: `TestPropQuotaNoStaleApply` |
| Canonical encoding with golden bytes for doc, state blob and op | `TestCanonicalEncoding` |
| Malformed decode table (22 rows + every prefix + state blobs + results) all `ErrMalformed` | `TestDecodeMalformedTable` |
| `Merge` is total and lawful on decoded hostile states (mutual contexts, contexts past their own dot, extreme stamps) | `TestMergeIsTotalOnDecodedStates`; `FuzzOCCDecode` (decode never panics, `encode(decode(b)) == b`, laws with honest and hostile partners; also feeds the bytes to every other decoder) |
| Typed document keys, accessors, recount | `TestTypedMetadataDocument`, `TestParseKey` |
| Every code row round-trips; `ConflictError` fields and `errors.As` | `TestErrorCodesRoundTrip` |
| Crash keeps acknowledged writes and the epoch | `TestNodePersistsAndReloadsItsEpoch` |
| Wipe → fresh epoch; reuse hazard demonstrated with the switch | `TestWipedNodeTakesAFreshEpoch` |
| Bit-rotted state → fresh epoch | `TestCorruptStateTakesAFreshEpoch` |
| Read errors at load → `ErrNotReady` for every op, retry, then load own state | `TestNodeRetriesLoadAfterReadErrors` |
| Wire: malformed requests/datagrams, not-found, precondition, conflict cross as codes; client delete | `TestNodeWireProtocol` |
| Clock exhaustion refuses writes; far-future stamps merge but are counted | `TestStoreClockEdges` |
| DST: 6 nodes / 3 regions, skew ±400 ms, drift ±2,000 ppm, 2% datagram drop, 5% duplication, 3 nemesis phases (crash, torn crash, restart, pause, partitions incl. one-way and region, isolate, link loss, clog, read errors) under a mixed workload (conditional, create, delete, blind, stale If-Match, scalars). Invariant every 250 events: every ack is on some replica, live or durable, or seen by a kept version's writer. After the final heal: converged within `ConvergeBound`, byte-identical state, equal ETags, oracle holds | `TestMultiRegionConvergenceDST` |
| DST: region c offline for 60 s while both sides write conditionally; after heal every key is exactly the merge of both sides' heads, no offline version dropped, at least one key contested | `TestOfflineRegionDST` |
| DST: two writers with one ETag. On one node exactly one wins and the other gets `ErrPreconditionFailed` over the wire. Across a partition both win and both survive as siblings on both servers | `TestConditionalWriteRaceDST` |
| DST: quota under partition; siblings resolved by recount; resolved usage = recount; overshoot ≤ `MaxOvershoot` | `TestQuotaOvershootUnderPartitionDST` |
| Canary: a merge that drops concurrent siblings is caught | `TestCanaryMergeDropsSibling` (switch `canaryDropConcurrent` via `export_test.go`; the deterministic two-writer case must fail the oracle, and the convergence DST must fail on at least one of `canarySeeds` seeds) |

## Named bounds (constants in `dst_test.go`) and measured values

| Constant | Value | Measured (500 seeds, `DST_SEEDS=500`, base 1) |
|---|---|---|
| `ConvergeBound` (multi-region, from the final heal; offline and quota, from the heal) | 15 s | multi-region: p50 2.35 s, p99 4.48 s, max 5.14 s. Offline region: p50 0.71 s, p99 1.40 s, max 1.68 s |
| `invariantEvery` (no-silent-loss across live and durable states) | 250 events | multi-region runs ≈ 850–1,200 events. The invariant also runs once more on the converged state |
| `raceConverge` | 10 s | passes on every seed |
| `MaxOvershoot = (quotaWriters-1) × quotaLimit` | 3,000 bytes (4 writers, limit 1,000) | overshoot distribution: 1,000 ×150, 1,100 ×170, 1,200 ×110, 1,300 ×40, 1,400 ×19, 1,500 ×6, 1,600 ×3, 1,700 ×2. It is never below 1,000 because each side of the 2-way partition fills the limit on its own. Anything above that comes from nodes on the same side checking stale views before they sync. I first wrote the bound per side (1 × limit) and the DST broke it on seed 1 (1,200). The honest bound is per independently checking writer |
| `canarySeeds` | 3 | the sibling-dropping merge fails the convergence DST on 3 of 3 seeds (and the deterministic two-writer case) |
| workload | 150 ops multi-region / 200 ops offline / 60 quota ops | multi-region acks ≈ 68–100, refused ≈ 13–35 per seed (sample of 40) |

**Sibling counts.**
- *Multi-region DST* (6 writers, conditional + blind writes, 4 keys). The per-run maximum is
  sampled at each invariant check: distribution over 500 seeds {2: 11, 3: 85, 4: 225, 5: 130,
  6: 39, 7: 8, 8: 2}; max 8, which is above the 6 writers because blind writes add siblings.
  Per key after convergence (2,000 keys): {1: 1,051, 2: 519, 3: 261, 4: 118, 5: 40, 6: 10, 7: 1}.
- *Offline-region DST* (6 writers, conditional writes only). Per-run max {1: 2, 2: 452, 3: 44,
  4: 2}. Per key after heal {2: 1,629, 3: 348, 4: 20, 5: 3}: never above the 6 writers. The rapid
  property proves the bound for the conditional-only case: siblings ≤ distinct writing actors.

**Merge cost** (`BenchmarkMergeDoc`, 10% of keys conflicted, in the dev container on the shared
4-core droplet): Doc merge (clone + merge) 1e3 records 3.7 ms / 0.72 MB allocated; 1e4 records
47.8 ms / 6.9 MB. Encode + strict decode: 1e3 records 12.0 ms, 1e4 records 147 ms. Decode
recomputes every version ID (two SHA-256 per version) and re-encodes for the canonical check;
that dominates.

**Wall times.** Default `go test ./internal/occ` ≈ 2–5 s (whole module `go test ./...` 11 s);
`-race` 14 s; rapid at 2,000 checks 8 s; `DST_SEEDS=500` for the DST and canary tests 134 s
(multi-region 57 s, offline 63 s, race 1 s, quota 12 s).
**Fuzz, 30 s each:** `FuzzOCCConditionalWrite` 78,156 execs; `FuzzOCCMergeLaws` 45,006;
`FuzzOCCDecode` 164,241. No crasher, nothing added to `testdata/fuzz/`.

## Uncovered branches

Coverage of `internal/occ`: 97.3% of statements. Uncovered:
- `Node.persist` error branches and their callers' error returns (`commit`, `Node.Put/Delete/SetScalar/UpdateQuota`, `OnRequest` push): a disk `Write`/`Sync` fails only on a closed env, and a closed env never runs handlers.
- `Node.commit`: the delta-too-large skip (a delta over 60,000 bytes) and `Send` errors (only `ErrClosed`/`ErrTooLarge`/unknown node).
- `Node.peers` with nil `Membership`; `onDigestReply` reply-kind and decode-failure branches (an honest peer never sends them); `OnDatagram` before the node is ready.
- `Node.SetScalar` HLC failure; `Node.EncodeState` for a not-ready node (exercised only if the sim digests during a load retry).
- `Doc.setRecord` empty-record branch (no caller passes an empty record) and `Doc.Digest` before any `Encode` (both defensive).
- `ParseKey` / `DecodeObjectMeta` / `decodeOp` paths where a parse succeeds but re-encodes differently; `UpdateQuota`'s generic write-error return (only reachable through a clock exhaustion between read and write).

## Errors intentionally ignored (`_ =`)

- `node.go` `Node.open`: `_ = n.persist()` of a fresh empty state. If it fails (only on a closed
  env) the next write persists anyway, and a restart without the blob draws another fresh epoch,
  which is safe.
- `node.go` `onDigestReply` and `OnDatagram`: `_ = n.persist()` after merging received state.
  Merged state is replicated data, not this node's acknowledgement. If it is not durable, a crash
  returns the node to its previous durable state, which still holds everything it acknowledged.
  Persist failures are counted in `Stats.DiskErrors`.
- Test files use `_, _ =` for writes whose outcome the property does not depend on.

## Panics added

None. No line was added to `agent_work/gates/panic-allowlist.txt`.

## Determinism lint

`internal/occ` has no findings. Imports are `bytes cmp crypto/sha256 encoding/binary
encoding/hex errors fmt math math/rand/v2 slices strconv strings time` plus `internal/env`.
Every map range uses the collect-then-`slices.Sort` shape (`Doc.Keys`, `Doc.ScalarKeys`). No
floats, and only strings, integers and errors are passed to `fmt`. No exemption is requested.

## Not done, and why

- **Tombstone collection.** 7.5 mentions a retention period. It is not in the Deliverable list,
  and collecting tombstones safely needs a "everyone has seen it" condition that this package
  cannot know (membership history), so tombstones are kept forever. Stated as a limit.
- **Siblings from blind writes are bounded by writes, not writers.** Under conditional writes
  only, a key holds at most one sibling per writing actor (checked in `TestPropNoSilentLoss`).
  Each unconditional write adds a sibling until someone resolves the key, because that is the
  plan's rule ("recorded as concurrent with what it did not read"). The multi-region DST uses
  blind writes on purpose. Its sibling numbers below include them.
- Full-state push-pull costs O(document) per mismatched round. Per-key digests or Merkle ranges
  (Chunk 3) would cut that; not in scope here.

## Requests to the orchestrator

1. Code rows 80–87 are claimed for `internal/occ`. Chunk 6 builds one table from every chunk's
   `CodeEntries()`; please check that the other Wave B chunks did not pick overlapping codes.
2. `Membership` is declared in `internal/occ` (`Peers() []env.NodeID`). Chunk 6 wires gossip's
   live view into it.

## Gate and commands

GATE_PLACEHOLDER
