# Chunk 4 build notes: optimistic concurrency control for metadata (`internal/occ`)

Branch `chunk/04-occ`. Spec: PLAN.md 4.2 "Chunk 4", 7.5, 2.2/2.3/2.6, and the "LWW ruling"
in 9.1. The first build (Opus, 2026-11-21) kept concurrent versions as siblings. The owner's
ruling of the same day replaced siblings with last-writer-wins. The repair round
(`objstore-p2-04-occ-repair1.md`) rewrote the package to that ruling and fixed the six
review-1 findings. This file describes the package **as it now is**. The "Repair round"
section at the end lists what changed and which test proves each point.

## The design in plain words

Each metadata key has **exactly one version** on every replica. A version is a value or a
tombstone (a delete), plus the metadata that makes it comparable.

A writer can say what it believes the current version is. `If-Match: <etag>` means "only if
the key is still at this version". `If-None-Match: *` means "only if the key does not exist
(or is deleted)". If the belief is wrong, the store refuses the write with
`ErrPreconditionFailed` and changes nothing. On one replica this check is atomic. Of two
writers holding the same ETag, exactly one wins.

Across a partition the check cannot see the other side. Both sides accept their writes, and
both writers get an acknowledgement. When the replicas meet again, **last-writer-wins
decides**. The version with the larger hybrid logical clock (HLC) stamp wins. The writer's
node ID breaks ties, and the version ID bytes break any tie that remains (only forged input
can tie that far). The rule depends on the two versions and nothing else, so every replica
picks the same winner without coordination, and replicas holding the same versions are
byte-identical.

**The trade-off:** an acknowledged write can be lost to a concurrent write under partition.
The system guarantees convergence and a record of the loss, not preservation. Every such loss
(an *LWW discard*) is counted in a per-node counter, written to a log line naming the key,
the losing version, its stamp and its writer, and the winning version and its stamp, and
reported to a callback. A loss can happen, but it is never invisible.

Version vectors stay, but only to *detect* concurrency. When a node replaces the version it
holds, it asks one question: had the winner's writer seen the loser? If yes, the replacement
is an ordinary overwrite and nothing was lost. If no, it is a discard and goes on the record.

"Last" means the highest HLC stamp, not the latest in real time. With skewed clocks, a write
made later in real time can lose to a concurrent write from a node whose clock runs ahead.
That loss is recorded like any other. A write that *saw* a version always beats it, at any
skew, because a write takes a stamp strictly above the version it replaces.

## What was built

| File | Contents |
|---|---|
| `errors.go` | sentinels `ErrPreconditionFailed`, `ErrMalformed`, `ErrNotFound`, `ErrQuotaExceeded`, `ErrClockExhausted`, `ErrClockAhead`, `ErrNotReady`; wire codes in the occ block 128–143 (`CodeBlockMin/Max`): 128 precondition, 129 malformed, 130 not found, **131 unassigned** (the retired conflict code), 132 quota, 133 clock exhausted, 134 clock ahead, 135 not ready; `CodeEntries`, `Codes()` |
| `vv.go` | `Actor{Node, Epoch}`, `Entry`, `VersionVector` (sorted, zero entries dropped), `Compare` → Equal/Before/After/Concurrent, `Descends`, `Join`, `Get` |
| `hlc.go` | `Stamp{Wall, Logical, Node}` with a total `Compare`, `After` (on `(wall, logical)` only) and an internal `successor`; `HLC` over `env.Clock` (`Now`, `Observe`, `Last`), `MaxClockLead = 1h` |
| `version.go` | `Dot`, `Version` (dot, causal context, tombstone flag, value, stamp, cached `VersionID`); `Compare` = the LWW total order; `ETag()`; `Descends`/`Supersedes` (event-set inclusion, a linear scan) |
| `record.go` | `ETag`, `ParseETag`; `Record` (one optional version): `Version`, `Present`, `Live`, `ETag`, `Equal`, `Merge` (the LWW maximum); `Precondition` (`IfMatch`, `IfNoneMatch`, `Unconditional`, `Holds`) |
| `doc.go` | `Doc` (key → one `Version`, cached sorted keys and encoding): `Record`, `Keys`, `KeysWithPrefix`, `MergeVersion`, `Merge`, `Clone`, `Subset`, `Stamps`, `MaxStamp() (Stamp, bool)`, `Encode`, `Digest`, `Equal`, strict `Decode` |
| `encoding.go` | encoder and decoder primitives. The decoder counts its work and stops building as soon as an error is set. |
| `store.go` | `Store` (embeds `Reader`): `Put`, `Delete`, `SetScalar`, `Merge` (returns `(changed, error)`), `EncodeState`; `LWWDiscards`, `DiscardTail`, `Counter`, `Actor`, `HLC`, `Rotations`, `AheadStamps`; `StoreConfig` with `Persist`, `Log`, `OnDiscard`; `Discard{Key, Winner, Loser}` and its log line; the transaction (snapshot, persist, restore); `Reader` (`Get`, `Record`, `Keys`, `Doc` (a copy), `Digest`, `Scalar`) |
| `quota.go` | `Usage`, `DecodeUsage`, `Reader.ReadQuota`, `Store.WriteQuota`, `UpdateQuota(key, attempts, fn)`, `RecountQuota(key, attempts, recount) (Usage, wrote bool, error)`, `AddUsage` |
| `meta.go` | typed document: `Kind`, key builders, `ParseKey`, `Permission`, `ObjectMeta`; typed reads on `Reader`, typed writes on `Store` and `Node` (`PutTag`, `PutACL`, `PutObjectMeta`, `PutManifest`, `SetRoute`, `SetContentType`), `Reader.RecountObjects` |
| `wire.go` | message kinds, client op encoding, `Result` |
| `node.go` | `Membership`, `StaticMembership`, `Config` (with `Log`, `OnDiscard`), `Node` (`env.Process`): `Reader`, `Record`, `Doc`, `Actor`, `Digest`, `LWWDiscards`, `DiscardTail`, `AheadStamps`, `Stats`; writes `Put`, `Delete`, `SetScalar`, `UpdateQuota`, `RecountQuota` and the typed writes; `NewFactory`, `Client` |

Only `internal/occ/` and this directory were touched.

## Design choices and why

**One version per key, everywhere.** `Doc` is `map[string]Version`. There is no sibling set
in memory, in the encoding, or in the API. `Get` returns the value or `ErrNotFound` (absent
or tombstone; the tombstone's ETag comes back with the error so a caller can recreate with
`If-Match`).

**The total order (`Version.Compare`).** HLC stamp `(wall, logical, node)`, then version ID
bytes. The version ID is a SHA-256 over the actor, the dot counter and a hash of
(context, tombstone flag, stamp, value), so two different versions differ in ID except
through a SHA-256 collision. The order is therefore total on every pair of decoded versions.
Merge is the per-key maximum under it: commutative, associative, idempotent and total, with
no error path. The record's winner is decided by nothing else: not arrival order, not which
side is local, not version vectors.

**Scalars share the record implementation.** A scalar field (route pointer, content type) is
an ordinary record written unconditionally (`SetScalar` = `Put(key, v, Unconditional())`).
Reasons: one merge rule, one encoding, one discard record. A scalar overwritten by a
concurrent scalar write is a lost write too and now goes on the record like any other. The
old `Scalar` type, its separate map and its tie-break on value bytes are gone.

**Classification (version vectors detect, they do not decide).** Each version carries a dot
(actor, counter) and a causal context (the vector of what its writer had seen). Its event set
is `downset(context) ∪ {dot}`. When a store replaces a version it *holds* (in a merge), it
tests whether the winner's event set strictly includes the loser's (`Supersedes`):
- strictly included: an ordinary overwrite; nothing is recorded;
- anything else (concurrent; equal event sets, which only forged input produces; or the
  higher-stamped winner causally *below* the loser, also only on forged input): a discard
  that is counted and logged. In doubt, the store records the loss.

Only replacements of a held version are classified. When an incoming version loses to what
the node holds, nothing is recorded on that node: it never held the loser. Its writer's node
holds the loser until something replaces it, and that replacement is recorded or is an
overwrite by a writer that saw it. So every loss is on at least one node's record, and the
same discard is not re-counted each time gossip delivers the loser again. One lost version
held by several replicas is counted once on each of them. That is why the discard counters
(events) exceed the number of distinct lost versions in the numbers below.

**A write always beats what it replaced (A3).** A local write's context is the held version's
clock, so the write causally supersedes it. Its stamp is `max(HLC.Now(), successor(held))`,
where `successor` is `(wall, logical+1)`, carrying into `wall+1` at a full logical counter.
The node's HLC is *not* moved to that successor. Normally `HLC.Now()` is already above the
held stamp, because the HLC observed it when it arrived. The successor path matters when the
held stamp lies more than `MaxClockLead` (1 h) ahead of the local clock (forged or wildly
skewed) and was refused by `Observe`.

The choice, and why: taking a stamp above the held version keeps every acknowledged write
from losing to what it replaced, and it keeps the key writable. Refusing the write would let
one forged far-future stamp lock a key until real time caught up, which at a stamp near the
end of time is forever. Not moving the node clock confines the damage: a far-future stamp
*pins only its own key*. Concurrent writes from other nodes to that key keep losing (and are
recorded) until real time passes the stamp, and every other key on the node keeps normal
stamps. At the absolute end of the stamp space (`(MaxInt64, MaxUint32)`) there is no
successor; the write is refused with `ErrClockExhausted` and changes nothing.

**Dots are never reused.** The dot counter comes from one per-actor counter, persisted in the
state blob with the document. A per-key `max+1` would reissue the dot of a write that was
discarded, and a later write by the same node would then look like a causal successor of
the discarded write on other replicas. `TestDotsAreNeverReused` shows the counter moving past
the discarded dot, and that a replica still holding the discarded write records it as a
discard. A hostile context carrying `MaxUint64` for the node's actor makes the store rotate
to a fresh epoch before writing.

**Actor epochs and the wipe hazard (A9).** Actor = `(NodeID, Epoch)`. The epoch is stored
with the state. A node that starts without readable state (new, wiped, or bit-rotted) draws a
fresh random epoch from its `node/<id>` stream. A read error leaves the node not ready, and
it retries every `LoadRetry` (100 ms). It never guesses. Without epochs, a wiped node would
restart its counter under the same actor. Its new writes would then look like causal
successors of its pre-wipe writes held by other replicas, and the replaced pre-wipe write
would vanish with no record. `TestWipedNodeTakesAFreshEpoch` builds exactly that, with the
test switch `hazardReuseEpoch`, and requires the independent loss oracle to *fail* with the
switch on and pass with it off.

**HLC observation (finding 2).** `Store.Merge` and the store loader call `Observe` on every
stamp in the incoming or loaded document, in sorted key order. A stamp beyond the lead is
refused and counted (`AheadStamps`, one per refused stamp). It does not stop the other stamps
from being observed. An empty document observes nothing, so a node with negative skew starts
at its own clock. `MaxStamp` returns `(Stamp, false)` for an empty document and the real
maximum (negative walls included) otherwise.

**Transactions: snapshot and restore (finding 3).** Every store mutation (write, delete,
scalar, quota write, merge, including inbound pushes, digest replies and deltas) runs as a
transaction. The store records the previous record of each key it touches, the actor (epoch),
the dot counter, the rotation count, the HLC state, the ahead count and the discard count.
It applies the change, encodes the state blob and calls the `Persist` hook (the node's
`Disk.Write` + `Disk.Sync`). If persist fails, everything listed is restored, and the call
returns the disk error. Pending discards are published (tail, log line, callback) only after
the persist succeeded. A failed persist therefore leaves no trace in reads, ETags, later
preconditions, replication, the counter, the tail or the log. I chose snapshot-and-restore
over persist-then-install because the document is mutated in place and a copy of it per
write would cost O(document). The undo log costs O(keys changed). Restoring the HLC is not
needed for safety (a clock that moved forward is harmless), but it makes "state from before
the call" exact.

**Discard record (A5).** `Discard{Key, Winner, Loser}`. The log line (`Discard.String()`) is
`occ: lww discard key="…" loser=<id> loser_stamp=<wall.logical@node> loser_writer=<node@epoch>
winner=<id> winner_stamp=<…>`. It is deterministic and holds no wall time. The counter
(`LWWDiscards`, saturating at `MaxUint64`) is persisted in the local state blob. It survives
restarts and never enters the replicated document. The last `DiscardTailLen` (64) records are
kept in a ring for tests and operators. The `Log` sink and the `OnDiscard(key, winner,
loser)` callback are optional `Config`/`StoreConfig` fields. Both run after the persist, in
the order the discards were decided.

**ETag.** `ETag(v) = v.ID()`: a deterministic function of the one version, equal on every node
holding it, different for every different version, including a new version with the same
value. A tombstone has an ETag. The absent record has the zero ETag, which no `If-Match` can
match.

**Preconditions (A6).** `If-Match e` holds iff the key has a version (tombstones count) whose
ETag is `e`. `If-None-Match: *` holds iff the key is absent or a tombstone. A missing
precondition always holds. `Delete` checks the precondition first, then returns `ErrNotFound`
if there is nothing live. Any write that passes replaces the held version as its causal
successor. A refused write changes nothing.

**Canonical encoding, strict and bounded decode.** Document = `"OCC\x02"`, a key count, then
`(key, version)` pairs in strictly increasing key order. A key repeated (two versions of one
record) or out of order is rejected at that point, before anything else is built. Context
entries must be strictly increasing with non-zero counters, checked as they are read, without
sorting. A dot counter of zero, a tombstone carrying a value, a flag byte outside {0,1}, a
logical counter above 32 bits, a count larger than the remaining input and trailing bytes are
all rejected. The decoder stops building as soon as an error is set (no version IDs hashed
after an error). Finally it requires `Encode(result) == input`, so `encode∘decode` and
`decode∘encode` are both the identity. State blob = `"OCCS" ‖ SHA-256(payload) ‖ payload`,
payload = `epoch ‖ counter ‖ discards ‖ document`. Goldens: `testdata/doc-v2.golden`,
`state-v2.golden`, `op-v1.golden` (the op format is unchanged; its golden bytes changed
because the ETag inside changed). Rewrite with `-update-golden`.

**Quotas (A8).** `UpdateQuota` = read (usage + the precondition to write with), compute,
conditional write, retrying only on `ErrPreconditionFailed`, at most `attempts` times; it
never applies a value computed from a stale read. `WriteQuota` refuses an unconditional
write. Partition-side updates settle by LWW like any key, so after a heal the stored usage
can be one side's figure. The discard of the quota key is the signal. The node's `OnDiscard`
callback names the key, and the integrated node (Chunk 6) calls
`RecountQuota(key, attempts, recount)` after syncing. `RecountQuota` computes the usage from
the hook and **writes nothing when the stored usage already equals it**. Without that, nodes
recounting at the same time would write identical figures as different versions, discard
each other's, and recount again forever. With it, the second round writes nothing. The quota
DST shows this on 500 seeds: always exactly 2 rounds. `Reader.RecountObjects(bucket)` sums
live `ObjectMeta` sizes. It is the recount this package can offer, and Chunk 6 plugs in its
manifest recount.

**Replicator (`Node`).** Single-threaded, acts only through `env`. Every `Period` (500 ms,
jittered ±50%) it sends its digest to a random peer, chosen from the *sorted*,
de-duplicated `Membership.Peers()`. The peer answers "match" or its full state. The caller
merges, then pushes its own state back if the digests still differ. After every local write
the node sends the changed key as a delta datagram to `Fanout` (2) random peers, if it fits
in `MaxDelta` (60,000 bytes). `Node.Store()` is gone. Readers get a read-only `Reader`, and
every write goes through a `Node` method that persists (inside the store transaction) and
broadcasts.

## Claims and the tests that prove them

| Claim | Test(s) |
|---|---|
| Vector `Compare` table incl. epochs, zero entries, duplicates; partial order, pointwise model, least upper bound | `TestVersionVectorCompareTable`, `TestVersionVectorBasics`, `TestPropVersionVectorPartialOrder` |
| Every precondition × state row (absent, live, tombstone) for put and delete; refused writes change nothing; a successful write is the record's one version, causally supersedes and beats the version it replaced, and records no discard | `TestConditionalPutTable`, `TestConditionalDeleteTable` |
| Single replica against a reference model: success iff the precondition holds; one ETag ↔ one version; a new version never keeps the ETag; no discards on one replica | `TestPropCheckAndSetModel` |
| ETag is a function of the version: equal across nodes and merge orders, different for different versions with equal values, tombstones carry one, absent is zero, parse round trip | `TestETagDeterministic` |
| LWW winner: wall, then logical, then node, then version-ID bytes on forged equal stamps; tombstone vs value both ways; the higher stamp wins even when causally below (forged); same answer through `Compare`, `Record.Merge` and `Doc.Merge` in both orders | `TestLWWWinnerTable` |
| A concurrent replacement is counted, logged (key, loser ID, loser stamp, loser writer, winner ID, winner stamp), kept in the tail and reported to the hook; an incoming loser is not a replacement; re-merging does not re-count; deletes discard puts and the reverse; the counter saturates; the tail is bounded | `TestLWWDiscardCountedAndLogged` |
| Causal overwrites (conditional and unconditional, across three nodes) are never counted or logged | `TestCausalOverwriteIsNotADiscard`; property `TestPropNoDiscardWithoutConcurrency` |
| A discarded write's dot is never reissued | `TestDotsAreNeverReused` |
| Every acknowledged version ends current on all replicas, or seen by a later acknowledged write, or named as a loser in some node's discard log. Checked against an independent seen-set oracle (not the product's classifier) at every step and after full exchange, on 2–4 replicas with random conditional/blind writes, deletes, merges and clock moves; counters equal log entries | `TestPropEveryLossRecorded`, fuzz `FuzzOCCConditionalWrite` (same property) |
| A write that saw a version beats it (and strictly supersedes it), at any skew up to `sim.MaxSkew`, with forged stamps anywhere in that range | `TestPropCausalWriteAlwaysWins` |
| Merge laws (commutative, associative, idempotent) and the result is the per-key LWW maximum (independent order in the test), on honest and generated hostile states, including extreme walls and forged equal stamps | `TestPropMergeLaws`, fuzz `FuzzOCCMergeLaws`, `TestMergeIsTotalOnDecodedStates` |
| Any delivery order with duplicates of full states and deltas → identical bytes, digest and ETags | `TestPropConvergence` |
| A far-future version: merged and counted, the node clock not moved; a write over it takes a stamp above it and wins; other keys keep normal stamps; the logical carry; at the end of time the write is refused and changes nothing | `TestFarFutureStampPinsOnlyItsKey`, `TestStoreClockEdges` |
| The HLC observes every in-range stamp even with a far-future stamp on another key; negative skew at start-up; `MaxStamp` reports absence | `TestHLCObservesEveryStamp`; HLC rules and monotonicity: `TestHLCRules`, `TestPropHLCMonotonic`; skew picks the higher clock and the slower node's next write wins: `TestLWWSkewPicksTheHigherClock` |
| A failed persist leaves no trace (doc, ETag, actor, counter, HLC, ahead count, discard count, tail, log, rotation) for put, delete, scalar, quota update, merge with a discard and a write that rotates the epoch; the same call works once the disk recovers | `TestFailedPersistLeavesNoTrace` (store), `TestNodeFailedPersistLeavesNoTrace` (node: fake `env.Disk` whose `Write`, and separately `Sync`, fails; put, delete, scalar, quota, inbound push; nothing replicates; a later conditional write on the old ETag works) |
| Quota CAS: create, limit, stale read retried, bounded retries, unconditional write refused, deleted counter, wrap-around refused, canonical usage; a stale read never applies | `TestQuotaCheckAndSet`, `TestPropQuotaNoStaleApply` |
| Recount writes only when wrong, retries stale reads, reports hook errors, rewrites a corrupt counter; partition-side updates are discarded on the record and identical recounts settle | `TestQuotaRecount` |
| Canonical encoding with golden bytes | `TestCanonicalEncoding` |
| Malformed decode table (19 rows incl. a repeated key, the sibling-era magic and the reviewer's 28 KiB shape) + every prefix + state blobs + results → `ErrMalformed` | `TestDecodeMalformedTable` |
| The reviewer's 400-versions-of-one-key input is rejected at the second version and is a committed `FuzzOCCDecode` seed | `TestReviewerHostileInputIsRejectedEarly` |
| Decode and merge work is linear: maximum-size (16 MiB) hostile messages, many keys and big contexts, decode in ≤ 4 operations per byte and merge (every key a forged discard) in ≤ 1 operation per byte of both messages; work per byte does not grow from 2 MiB to 16 MiB | `TestHostileMessagesBoundedWork` (1 MiB under `-race`) |
| Decode never panics, errors are `ErrMalformed`, round trip, laws with honest and hostile partners, store merge never fails | `FuzzOCCDecode` |
| Typed document keys, accessors, recount; node typed writes are durable and replicate | `TestTypedMetadataDocument`, `TestParseKey`, `TestNodeTypedWritesGoThroughCommit` |
| Every code in 128–143, no duplicate, 131 unassigned; every code round-trips over the wire | `TestErrorCodesRoundTrip` |
| Crash keeps acknowledged writes, the epoch and the dot counter; bit-rot → fresh epoch; read errors → not ready, retry, load own state | `TestNodePersistsAndReloadsItsEpoch`, `TestCorruptStateTakesAFreshEpoch`, `TestNodeRetriesLoadAfterReadErrors` |
| Wipe → fresh epoch, the loss recorded as concurrent; the reuse hazard is visible with the switch | `TestWipedNodeTakesAFreshEpoch` |
| Wire: malformed requests/datagrams, not-found, precondition failures cross as codes; client get and delete results | `TestNodeWireProtocol` |
| The peer drawn does not depend on the membership's order | `TestPeerChoiceIgnoresMembershipOrder` |
| DST, 6 nodes / 3 regions, skew ±400 ms, drift ±2,000 ppm, 2% drop, 5% duplication, 3 nemesis phases (crash, torn crash, restart, pause, partitions incl. one-way and region, isolate, link loss, clog, read errors), mixed workload (conditional, create, delete, blind, stale `If-Match`, scalars). Every 250 events: every-loss-recorded over live and durable states. After the final heal: converged within `ConvergeBound`, byte-identical documents and equal ETags, every acknowledged key holds one version, every-loss-recorded on the final state, counters = hook records = oracle log | `TestMultiRegionConvergenceDST` |
| DST: region c offline 60 s while both sides write conditionally, two forced contests (c's later write on `k0`, the other side's on `k1`); every offline write wins, is superseded by a write that saw it, or is recorded as discarded; both kinds occur on every seed; c's later write on `k0` wins | `TestOfflineRegionDST` |
| DST: two writers with one ETag. On one node exactly one wins, the other gets `ErrPreconditionFailed` over the wire, and no discard is recorded. Across a partition both are acknowledged, after the heal both servers hold the same single version (one of the two), and the other's discard is on a server's record | `TestConditionalWriteRaceDST` |
| DST: quota under partition; a quota-key discard is recorded (the scenario really contested it); recounts driven by those discards settle (no node pending, a further recount writes nothing); every node's stored usage = the recount; overshoot ≤ `MaxOvershoot` | `TestQuotaOvershootUnderPartitionDST` |
| Canary: a discard that is neither counted nor logged fails the deterministic two-writer case and the convergence DST | `TestCanaryDiscardNotRecorded` (switch `canaryUnrecordedDiscard`) |
| Canary: a merge that keeps the local version (arrival-order winner) fails the merge laws and the convergence DST | `TestCanaryMergePrefersLocal` (switch `canaryMergePrefersLocal`) |

## Named bounds and measured values

Measured with `DST_SEEDS=500` (base 1) in the dev container on the shared 4-core droplet
(load average about 6 during the runs).

| Constant | Value | Measured |
|---|---|---|
| `ConvergeBound` (from the final heal) | 15 s | multi-region p50 2.29 s, p99 4.41 s, max 5.16 s; offline region p50 0.76 s, p99 1.56 s, max 2.22 s; quota p50 0.49 s, max 1.35 s |
| `invariantEvery` | 250 events | multi-region runs 835–1,199 events, offline 2,231–2,415 |
| `raceConverge` | 10 s | passes on every seed |
| `MaxOvershoot = (quotaWriters-1) × quotaLimit` | 3,000 bytes (4 writers, limit 1,000) | see below |
| `maxRecountRounds` | 6 | always 2 (round 1 writes the recount, round 2 writes nothing) |
| `canarySeeds` | 3 | each canary fails the convergence DST on 3 of 3 seeds |

**Discards per partitioned workload.**
- *Multi-region DST* (6 writers, partitions and crashes, blind writes and stale `If-Match`
  included). Of 51,781 acknowledged writes over 500 seeds, 3,959 are current at the end,
  33,397 were superseded by a later write that saw them, and **14,425 (27.9%) were lost to
  concurrent writes, each named in a discard log**. Per seed, distinct lost versions
  p50 36 (min 23, max 55) of p50 104 acknowledged writes. Discard events (sum of the node
  counters) p50 75, p99 109, max 136. A lost version is counted once on each replica that
  held it.
- *Offline-region DST* (conditional writes only). Of 74,466 acknowledged writes, 2,000
  current, 61,557 superseded, **10,909 (14.6%) recorded as discarded**. Per seed, distinct
  lost versions p50 24 (12–38), discard events p50 47 (21–76). The offline region's writes:
  830 ended current and 3,335 were recorded as discarded across the 500 seeds; per seed it
  won 1–3 keys and lost 2–12 writes.
- In every seed, counter sum = callback records = oracle log, and no acknowledged write was
  lost without a record.

**Quota overshoot** (4 writers, two per side, limit 1,000 bytes, 100-byte objects):
distribution {1,000: 9, 1,100: 22, 1,200: 63, 1,300: 90, 1,400: 89, 1,500: 82, 1,600: 65,
1,700: 37, 1,800: 22, 1,900: 14, 2,000: 4, 2,100: 1, 2,200: 1, 2,400: 1}; p50 1,400, max
2,400. At least 1,000 because each side fills the limit on its own. More comes from two
nodes on one side updating from the same read before their deltas cross. Those updates now
settle by LWW, the lower update is lost, and the side under-counts and admits more objects.
That is why the tail is longer than under siblings (old max 1,700). Quota-key discard events
per run (heal plus recounts) p50 9, min 3, max 16. After the recounts every node stores the
true usage.

**Merge cost** (`BenchmarkMergeDoc`, 10% of keys written concurrently on both sides, loaded
droplet). Doc merge (clone + merge) 1e3 records 2.3 ms / 0.21 MB, 1e4 records 22 ms / 2.0 MB.
Store merge (observe + LWW + classify + undo log, with ~10% discards) 1e3 records 1.0 ms,
1e4 records 19 ms. Encode + strict decode 1e3 records 8.6 ms, 1e4 records 70 ms; decode
recomputes two SHA-256 per version and re-encodes for the canonical check, which dominates.

**Hostile-message work** (`TestHostileMessagesBoundedWork`, counted operations). Many keys
(16,777,138 bytes, about 800,000 keys): decode 31.2 M operations (1.86 per byte), merge with
every key a forged discard 1.6 M. Big contexts (16,776,993 bytes, 4 keys × about 700,000
context entries): decode 25.2 M (1.50 per byte), merge 11.2 M. Same ratios at 2 MiB. Wall
time on the loaded droplet: 27–38 s for the 16 MiB many-keys case (dominated by allocation
and GC for 800,000 versions), about 3 s for big contexts.

**Wall times** (loaded droplet). `go test ./internal/occ` 31–38 s (the 16 MiB hostile test is
most of it); `-race` 10 s; rapid at 2,000 checks 37 s; `DST_SEEDS=500` for DST + canaries
90 s (multi-region 33 s, offline 41 s, race 1.4 s, quota 15 s).

**Fuzz** (loaded droplet). 30 s each: `FuzzOCCConditionalWrite` 6,515 execs,
`FuzzOCCMergeLaws` 5,336, `FuzzOCCDecode` 247,067. `FuzzOCCDecode` for 120 s (the target
whose first gate run died with `context deadline exceeded`): 1,335,639 execs, about 11,000
per second at the end, ended `PASS`. No crasher. The property targets run fewer execs than in the first build because
their seeds are 32 KiB (so none is skipped) and the corpus grows from them.

## Uncovered branches

Coverage of `internal/occ`: 98.4% of statements. Uncovered:
- `decodeDoc`: the non-canonical-document failure after a clean parse. Every field is now
  checked as it is read (strict key order, strict context order, minimal varints are checked
  by the re-encode only), so a reachable input needs a non-minimal varint inside an
  otherwise valid document; the malformed table has one at the document level, not inside a
  version. Kept as the final guard.
- `Node.onDigestReply` reply-kind and decode-failure branches and the not-ready guard; an
  honest peer never sends them and replies to a previous incarnation are cancelled.
- `Node.broadcast`: the delta-too-large skip and `Send` errors.
- `Node.peers` with nil `Membership`; `Node.OnDatagram` before ready; `Node.EncodeState`
  for a not-ready node.
- `ParseKey` / `DecodeObjectMeta` / `decodeOp` non-canonical re-encode branches.
- `recountQuota`: a non-precondition write error (only a clock exhaustion between read and
  write).

## Errors intentionally ignored (`_ =`)

- `node.go` `Node.open`: `_ = n.persist(...)` of the fresh empty state. If it fails, the
  next write persists the epoch with its data. A restart without a blob draws another fresh
  epoch, which is safe.
- `node.go` `Node.OnDatagram`: `_, _ = n.store.Merge(doc)`. A delta that cannot be
  persisted is rolled back (the transaction restores the state) and counted in
  `Stats.DiskErrors`. The next digest round brings the data again.
- Test files use `_, _ =` for writes whose outcome the property does not depend on.

## Panics added

None. No line added to `agent_work/gates/panic-allowlist.txt`.

## Determinism

No wall clock, goroutines, channels, mutexes or global rand in product code. Every map range
uses collect-then-sort (`Doc.sortedKeys`, the only one). The undo log is a slice. Imports:
`bytes cmp crypto/sha256 encoding/binary encoding/hex errors fmt math math/rand/v2 slices
strconv strings time` plus `internal/env`.

## Not done, and why

- **Tombstone collection.** Tombstones are kept forever. Collecting one safely needs "every
  replica has seen it", which needs membership history this package does not have. Stated
  as a limit.
- **Automatic recount scheduling.** The node does not schedule recounts itself. It reports
  quota-key discards through `OnDiscard` and offers `RecountQuota`. A recount run before the
  node has the other side's objects would write a wrong figure that nothing would correct.
  When to recount (after sync) is Chunk 6's decision. The quota DST drives it from the
  callback after convergence.
- **A smaller in-memory version.** `Version` is a 150-byte value type. A 16 MiB message of
  minimal versions costs several hundred MB of allocation to decode and merge. It is linear,
  but a pointer representation would cut it. Not needed by any bound in the plan.
- Full-state push-pull still costs O(document) per mismatched round. Merkle ranges
  (Chunk 3) would cut that; not in scope.

## Requests to the orchestrator

1. `internal/occ` now uses codes 128–143 (131 deliberately unassigned). `TestErrorCodesRoundTrip`
   checks the block.
2. `Membership` is declared in `internal/occ` (`Peers() []env.NodeID`); Chunk 6 wires gossip's
   live view into it.
3. Chunk 6: wire `Config.OnDiscard` to (a) metrics and (b) a deferred `RecountQuota` for quota
   keys, after the node has synced.

## Repair round (2026-11-21): the LWW ruling and review 1

Commits: `ca14baf` (tests first; they target the new API and do not compile against the
sibling code), `756c26b` (implementation), then a coverage/measurement commit and these notes.

### Owner's ruling (Part A)

| Item | What changed | Proving test(s) |
|---|---|---|
| A1 one version per key | `Doc` holds one `Version` per key; `ConflictError`, `ErrConflict`, `IsConflict` and code 83 deleted, and the conflict slot of the new block (131) left unassigned rather than renumbering; `Get` returns the version or `ErrNotFound`; `Decode` rejects a repeated key; `maximal` and the sibling bound do not exist | `TestDecodeMalformedTable` (repeated key, reviewer shape), `TestErrorCodesRoundTrip` |
| A2 winner is a pure function | `Version.Compare`: stamp, node, version ID; merge = maximum; scalars share the record implementation | `TestLWWWinnerTable`, `TestPropMergeLaws`, `FuzzOCCMergeLaws`, `TestPropConvergence`, `TestCanaryMergePrefersLocal` |
| A3 causality agrees with the clock | stamp = `max(HLC, successor(held))`, the node clock not moved; refused only at the end of time (`ErrClockExhausted`) | `TestPropCausalWriteAlwaysWins`, `TestFarFutureStampPinsOnlyItsKey`, CAS tables |
| A4 vectors only detect | `Supersedes` decides causal vs discard; equal event sets and causally-below winners (forged) are discards | `TestLWWWinnerTable` (causally-below row), `TestCausalOverwriteIsNotADiscard`, `TestPropNoDiscardWithoutConcurrency` |
| A5 every discard counted and logged | saturating persisted counter, deterministic log line via `Log` sink, 64-entry ring tail, `OnDiscard` callback, all published after persist | `TestLWWDiscardCountedAndLogged`, `TestPropEveryLossRecorded`, DSTs (counter = hook = oracle), `TestCanaryDiscardNotRecorded`, `TestFailedPersistLeavesNoTrace` |
| A6 preconditions unchanged | as before, against one version; an unconditional write is a causal overwrite | CAS tables, `TestPropCheckAndSetModel`, `TestETagDeterministic`, `TestConditionalWriteRaceDST` |
| A7 deletes compete | tombstones are versions under the same order | `TestLWWWinnerTable`, `TestLWWDiscardCountedAndLogged` (delete vs put both ways) |
| A8 quotas | check-and-set unchanged; recount driven by quota-key discards; `RecountQuota` writes nothing when right | `TestQuotaCheckAndSet`, `TestQuotaRecount`, `TestPropQuotaNoStaleApply`, `TestQuotaOvershootUnderPartitionDST` |
| A9 wipe epoch | unchanged guarantee; the test now writes three times after the wipe so a reused actor makes the replacement of `(A,2)` look causal, and the independent oracle sees the unrecorded loss with the switch on | `TestWipedNodeTakesAFreshEpoch` |

Renamed or replaced tests: `TestPropNoSilentLoss` → `TestPropEveryLossRecorded`;
`TestPropSiblingsOnlyWhenConcurrent` → `TestPropNoDiscardWithoutConcurrency`;
`TestCanaryMergeDropsSibling` → `TestCanaryDiscardNotRecorded` + `TestCanaryMergePrefersLocal`;
`TestResolveCollapsesSiblings` and `TestUnconditionalWriteStaysConcurrentWithItsOwnEarlierWrite`
deleted (no siblings); `TestLWWScalarTieBreak` → `TestLWWWinnerTable` +
`TestLWWSkewPicksTheHigherClock`.

### Review findings (Part B)

| Finding | Fix | Proving test(s) |
|---|---|---|
| 1 quadratic work on hostile input | (a) the decoder returns before hashing or building once an error is set, and checks key and context order as it reads; (b) `descends` is a two-pointer scan, O(\|a\|+\|b\|); (c) the sibling path is deleted, and a key repeated in a message is rejected at the repeat; (d) `checkLaws` deduplicates keys and compares one version per key | `TestHostileMessagesBoundedWork` (16 MiB, operation counts, linear from 2 to 16 MiB), `TestReviewerHostileInputIsRejectedEarly` (the 28 KiB shape is the seed `testdata/fuzz/FuzzOCCDecode/reviewer-28k-multi-version`), `FuzzOCCDecode` (asserts ≤ 4 operations per byte on every input) |
| 2 HLC observation | observe every stamp in key order; count each refused stamp; empty observes nothing; `MaxStamp` returns `(Stamp, bool)`. Reconciled with A3: a write over an unobserved far-future stamp takes its successor without moving the clock | `TestHLCObservesEveryStamp` (both reviewer reproductions), `TestFarFutureStampPinsOnlyItsKey` |
| 3 failed persist leaves a trace | snapshot-and-restore transaction in `Store` covering doc keys, epoch, dot counter, HLC, discard count, ahead count; discards published after persist; the node passes its persist as the hook, so pushes, deltas and digest replies roll back too | `TestFailedPersistLeavesNoTrace`, `TestNodeFailedPersistLeavesNoTrace` |
| 4 fuzz seeds skipped | seeds are 32 KiB (one zero-filled, three PCG-filled); verified `go test -v -run '^Fuzz'` shows no SKIP | (all `Fuzz*/seed#N` pass) |
| 5 mutable store handed out | `Node.Store()` removed; `Node.Reader()` is read-only; typed writes on `Node` | `TestNodeTypedWritesGoThroughCommit` |
| 6 peers and quota contest | `peers()` sorts and de-duplicates; the quota DST fails unless a quota-key discard was recorded before the recount | `TestPeerChoiceIgnoresMembershipOrder`, `TestQuotaOvershootUnderPartitionDST` |

### API changes

Removed: `ConflictError`, `ErrConflict`, `CodeConflict`, `IsConflict`, `Scalar` (type),
`Record.Siblings/Live() []Version/IDs/Covers/Len`, `NewRecord(vs...)` (now one version),
`Doc.MergeRecord/MergeScalar/ScalarKeys/Scalar`, `Node.Store()`, the `recount` argument of
`UpdateQuota`. Changed: `Store.Merge` returns `(bool, error)`; `SetScalar`/`SetRoute`/
`SetContentType` return `Version`; `Doc.MaxStamp` returns `(Stamp, bool)`; codes moved to
128–143; document magic `OCC\x02`; state blob payload gained the dot counter and the
discard count. Added: `Version.Compare/ETag`, `Record.Version/Present/Live/Merge`,
`Doc.MergeVersion/Stamps`, `Stamp.After`, `Discard`, `DiscardTailLen`, `StoreConfig.{Counter,
Discards, Persist, Log, OnDiscard}`, `Config.{Log, OnDiscard}`, `LWWDiscards`, `DiscardTail`,
`Store.Counter`, `Reader`, `RecountQuota`, node typed writes, `CodeBlockMin/Max`.

### Chosen not to do

- The node does not schedule recounts itself (see "Not done").
- An incoming loser is not recorded on the node that rejects it (see "Classification"); the
  loss is recorded where the loser was held.

## Gate and commands

All through `docker compose run --rm dev` unless noted.

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...`, `go build ./...` | clean |
| `go test -count=1 -cover ./internal/occ` | ok, 98.4% |
| `go test -count=1 -race ./internal/occ` | ok, 10 s |
| `go test -count=1 ./internal/occ -rapid.checks=2000` | ok, 37 s |
| `DST_SEEDS=500 go test -count=1 -v -run 'DST\|Canary' ./internal/occ` | ok, 90 s (quota re-run after the reporting change: ok, 11 s) |
| `go test ./internal/occ -run '^$' -fuzz '^<T>$' -fuzztime 30s -fuzzminimizetime 5s` | `FuzzOCCConditionalWrite` 6,515 execs, `FuzzOCCMergeLaws` 5,336, `FuzzOCCDecode` 247,067; no crasher |
| `go test ./internal/occ -run '^$' -fuzz '^FuzzOCCDecode$' -fuzztime 120s -fuzzminimizetime 5s` | 1,335,639 execs, `PASS`, ended cleanly |
| `go test -run '^$' -bench BenchmarkMergeDoc -benchmem -benchtime 20x ./internal/occ` | numbers above |
| `agent_work/gates/gate.sh 04-occ` (host, first run, `94cb2ca`) | `overall=fail`: everything green except G0 detlint, which flagged `Doc.sortedKeys` ranging over the map into a struct field (detlint accepts collect-into-a-local-then-sort) and the `//go:build race` / `!race` test files (build constraints are not allowed anywhere). Fixed in `0879e71`: a local slice, and the race-only message size read from the test binary's build settings (`-race`) |
| `agent_work/gates/gate.sh 04-occ` (host, `0879e715eeb97f4ed600050eaa97038920ed4ff9`, clean tree) | `gate: overall=pass counts_for_landing=true`. G0 all pass (comments against baseline `a184b38`); G1 pass, `internal/occ` 98.4%, `internal/sim` 95.4%; G2 rapid 2,000 checks (seed 9319661039289) pass, 158 s; G3 fuzz 30 s pass: `FuzzOCCConditionalWrite` 18,893 execs, `FuzzOCCMergeLaws` 8,160, `FuzzOCCDecode` 179,457, `FuzzSimSchedule` 8,761; G4 race pass (`internal/occ` 16 s); G5 500 seeds pass (`internal/occ` 164 s); G6 pass. The commit after it changes only this file; the gate is re-run on the branch tip before hand-back |

The result files under `agent_work/04-occ/gates/` are left uncommitted, as the brief allows.
