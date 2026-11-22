# Chunk 4 build notes: optimistic concurrency control for metadata (`internal/occ`)

Branch `chunk/04-occ`. Spec: PLAN.md 4.2 "Chunk 4", 7.5, 2.2/2.3/2.6, and the "LWW ruling"
in 9.1. The first build (Opus, 2026-11-21) kept concurrent versions as siblings. The owner's
ruling of the same day replaced siblings with last-writer-wins. The repair round
(`objstore-p2-04-occ-repair1.md`) rewrote the package to that ruling and fixed the six
review-1 findings. Review 2 (`REVIEW-2.md`) and the independent test report
(`agent_work/adversarial/04-occ/ATTACKS.md`) found two schedules where a loss left no record,
a crash window that could drop a record, and two quota bugs; LWW round 2
(`objstore-p2-04-occ-lww-round2.md`) closed them. This file describes the package **as it now
is**. The "Repair round" and "LWW round 2" sections at the end list what changed in each round
and which test proves each point.

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
reported to a callback. A loss to a concurrent write can happen, but it is never invisible.

**Where a loss is recorded.** A node records a discard whenever LWW makes it drop a version
that its winner never saw, in either of the two ways that can happen on one node:
- it *replaces* the version it holds with an incoming concurrent winner, or
- it *rejects* an incoming version because the one it holds wins, and the held version never
  saw the incoming one.

So every node that ever held or received a losing version records it, as long as that node
had the loser in hand when the winner met it. The writer's own node is not needed for the
record: a writer that crashes for good, is wiped, or loses its state blob after its version
reached any other node leaves the record on that other node.

**The one remaining limit.** A write acknowledged by a node whose disk is lost before the
version reached any other node is lost with that disk and has no record. That is a disk
loss, not a concurrency loss: no node ever saw the version lose. The general form is: a
version is unrecorded only if every node that ever received it (its writer included) lost
its state while still holding it. The tests' oracle allows exactly this case and no other
(see "LWW round 2", item 2).

Version vectors stay, but only to *detect* concurrency. When LWW drops one of two versions,
the node asks one question: had the winner's writer seen the loser? If yes, the drop is an
ordinary overwrite (or old news arriving late) and nothing was lost. If no, it is a discard
and goes on the record.

"Last" means the highest HLC stamp, not the latest in real time. With skewed clocks, a write
made later in real time can lose to a concurrent write from a node whose clock runs ahead.
That loss is recorded like any other. A write that *saw* a version always beats it, at any
skew, because a write takes a stamp strictly above the version it replaces.

## What was built

| File | Contents |
|---|---|
| `errors.go` | sentinels `ErrPreconditionFailed`, `ErrMalformed`, `ErrNotFound`, `ErrQuotaExceeded`, `ErrClockExhausted`, `ErrClockAhead`, `ErrNotReady`, `ErrUsageOverflow`; wire codes in the occ block 128–143 (`CodeBlockMin/Max`): 128 precondition, 129 malformed, 130 not found, **131 unassigned** (the retired conflict code), 132 quota, 133 clock exhausted, 134 clock ahead, 135 not ready, 136 usage overflow; `CodeEntries`, `Codes()` |
| `vv.go` | `Actor{Node, Epoch}`, `Entry`, `VersionVector` (sorted, zero entries dropped), `Compare` → Equal/Before/After/Concurrent, `Descends`, `Join`, `Get` |
| `hlc.go` | `Stamp{Wall, Logical, Node}` with a total `Compare`, `After` (on `(wall, logical)` only) and an internal `successor`; `HLC` over `env.Clock` (`Now`, `Observe`, `Last`), `MaxClockLead = 1h` |
| `version.go` | `Dot`, `Version` (dot, causal context, tombstone flag, value, stamp, cached `VersionID`); `Compare` = the LWW total order; `ETag()`; `Descends`/`Supersedes` (event-set inclusion, a linear scan) |
| `record.go` | `ETag`, `ParseETag`; `Record` (one optional version): `Version`, `Present`, `Live`, `ETag`, `Equal`, `Merge` (the LWW maximum); `Precondition` (`IfMatch`, `IfNoneMatch`, `Unconditional`, `Holds`) |
| `doc.go` | `Doc` (key → one `Version`, cached sorted keys and encoding): `Record`, `Keys`, `KeysWithPrefix`, `MergeVersion`, `Merge`, `Clone`, `Subset`, `Stamps`, `MaxStamp() (Stamp, bool)`, `Encode`, `Digest`, `Equal`, strict `Decode` |
| `encoding.go` | encoder and decoder primitives. The decoder counts its work and stops building as soon as an error is set. |
| `store.go` | `Store` (embeds `Reader`): `Put`, `Delete`, `SetScalar`, `Merge` (returns `(document changed, error)`), `EncodeState`; `NewStore`, `OpenStore(cfg, state)` (open from a state blob and emit the outbox); `LWWDiscards`, `DiscardTail`, `Counter`, `Actor`, `HLC`, `Rotations`, `AheadStamps`; `StoreConfig` with `Persist`, `Log`, `OnDiscard`; `Discard{Key, Winner, Loser}` and its log line; `DiscardTailLen`, `DiscardDedupLen`, `DiscardOutboxLen`; the transaction (snapshot, persist, restore, emit, drain); classification of replaced and rejected versions; the state blob; `Reader` (`Get`, `Record`, `Keys`, `Doc` (a copy), `Digest`, `Scalar`) |
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
is `downset(context) ∪ {dot}`. In a merge, for every key whose incoming version differs from
the held one, the store tests whether the LWW winner's event set strictly includes the
loser's (`Supersedes`):
- strictly included: an ordinary overwrite (held replaced) or old news (incoming rejected);
  nothing is recorded;
- anything else (concurrent; equal event sets, which only forged input produces; or the
  higher-stamped winner causally *below* the loser, also only on forged input): a discard
  that is counted and logged. In doubt, the store records the loss.

Both directions are classified: a held version replaced (`Store.replaced`) and an incoming
version rejected (`Store.rejected`). The test that the classification is right is an
independent oracle built only from what each write actually held when it was made
(`TestPropClassificationMatchesOracle`, below).

**De-duplication of records.** Anti-entropy re-sends a losing version until its sender
catches up, and every node still holding a loser offers it again. Each node keeps the last
`DiscardDedupLen` (256) recorded `(key, losing version ID)` pairs, as 16-byte hashes, in its
persisted state, and records a pair it finds there no second time. The window is a FIFO.
Beyond it, a loser that is offered again after 256 newer records is recorded again: an
over-count of events, never an under-count, and every repeated record names the same losing
version ID. A wiped node starts with an empty window (and a zero counter).

**What the counter counts.** `LWWDiscards` is *discard events observed by this node*: the
number of distinct `(key, loser)` pairs this node recorded, by replacement or by rejection,
within the window. It is not the number of distinct lost versions. One lost version is
typically recorded on every node that held or received it, so the sum of the counters over
the nodes exceeds the number of distinct lost versions (the numbers below give both). To
count distinct losses, join the log lines on `loser=<version ID>`; every record carries the
losing version's ID, its stamp and its writer. A record can also name a version that some
other writer had causally overwritten elsewhere (the record is made where that overwrite
had not arrived yet). That is an over-count of losses, not a missing one.

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

**Dots are never reused, and a clock never claims a dot its writer did not see.** The dot
counter comes from one per-actor counter, persisted in the state blob with the document. A
per-key `max+1` would reissue the dot of a write that was discarded. That alone is not
enough. A version vector entry `a:N` means *all* of `a`'s dots `1..N`. After node `a`'s own
version `(a,2)` is discarded for a concurrent winner that never saw it, `a`'s next write
`(a,3)` is fine (its context holds `a:1`), but the write after that takes `(a,3)`'s clock
`{a:3, …}` as its context, which claims `(a,2)`. A third node still holding `(a,2)` would then
classify the replacement as a causal overwrite and record nothing (REVIEW-2 finding 1).

The fix: **a node retires its actor epoch when a discard leaves a gap in its own dots.**
When a merge replaces a held version with a concurrent winner, and the loser's clock holds
more of this node's current actor's dots than the winner's clock does
(`loser.clockAt(actor) > winner.clockAt(actor)`), the node draws a fresh epoch and restarts
its counter at 0 (`Rotations` counts it). Every later write uses the new actor, so no later
clock can reach the discarded dot through a downset. The test for the condition is on the
current actor only, because retired epochs never issue dots again.

Why this rule is enough: a write by `a` claims `a`'s dots below its own counter. Such a claim
is false only for a dot of a version of the same key that this write did not see. `a`
created that version and held it, so it left `a`'s replica through a chain of replacements.
If every step was causal, the current held version saw it and its clock already includes the
dot. So some step was a concurrent replacement whose loser's clock had the dot and whose
winner's clock (exact, by induction) did not: exactly the condition that retires the epoch.

Chosen over the alternative (an exact context that does not inflate over gaps, i.e. dotted
version vectors with an exception set): retiring the epoch keeps vectors as plain downsets,
keeps the encoding, the linear `descends` scan and every decoder bound unchanged, and is a
few lines in one place. Its cost is one more vector entry per gap-creating discard on the
writer's node (measured below as the largest context in the final documents). The exact
context would avoid that growth but changes the encoding, `descends`, `Join` and the
hostile-input work bounds, and would need its own review round.

A hostile context carrying `MaxUint64` for the node's actor still makes the store rotate to
a fresh epoch before writing.

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
`Disk.Write` + `Disk.Sync`). If persist fails, everything listed is restored, together with
the tail, the outbox and the de-duplication window, and the call returns the disk error.
Pending discards are emitted (log line, callback) only after the persist succeeded. A failed
persist therefore leaves no trace in reads, ETags, later preconditions, replication, the
counter, the tail, the window or the log. I chose snapshot-and-restore
over persist-then-install because the document is mutated in place and a copy of it per
write would cost O(document). The undo log costs O(keys changed). Restoring the HLC is not
needed for safety (a clock that moved forward is harmless), but it makes "state from before
the call" exact.

**Discard record (A5).** `Discard{Key, Winner, Loser}`. The log line (`Discard.String()`) is
`occ: lww discard key="…" loser=<id> loser_stamp=<wall.logical@node> loser_writer=<node@epoch>
winner=<id> winner_stamp=<…>`. It is deterministic and holds no wall time. The counter
(`LWWDiscards`, saturating at `MaxUint64`) is persisted in the local state blob. It survives
restarts and never enters the replicated document. The last `DiscardTailLen` (64) records
are the readable tail (`DiscardTail`, oldest first); it is persisted with the state, so it
survives restarts. The `Log` sink and the `OnDiscard(key, winner, loser)` callback are
optional `Config`/`StoreConfig` fields. Both run after the persist, in the order the
discards were decided.

**The outbox: records survive a crash before they are emitted.** The records a transaction
makes are written into a persisted *outbox* in the same durable write as the merge (and the
counter). After that write succeeds, the store emits them (log line, callback) and then
*drains* the outbox with a second state write. When a node opens its state (`Node.Start`,
`OpenStore`), it emits whatever the outbox still holds and drains it. So a crash anywhere
between the durable merge and the drain loses no record: the restarted node emits it.
Delivery is therefore **at least once** across crashes: a record is emitted twice only if
the process dies after emitting and before the drain write lands, or if the drain write
fails (the record stays in the outbox and is emitted again at the next open). A repeated
record carries the same losing version ID, and the counter is never incremented twice (it
was persisted with the merge). Bound: the outbox holds the records of the current
transaction (at most one per key of the incoming document) plus, after failed drain writes,
up to `DiscardOutboxLen` (1,024) already-emitted records; beyond that the oldest
already-emitted ones are dropped, which can only cost a re-emission after a crash, never a
first emission. In normal operation the drained outbox is empty on disk.

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
`decode∘encode` are both the identity. State blob = `"OCS2" ‖ SHA-256(payload) ‖ payload`,
payload = `epoch ‖ counter ‖ discards ‖ tail ‖ outbox ‖ window ‖ document`, where tail and
outbox are counted lists of `(key, winner version, loser version)` and window is a counted
list of 16-byte loss hashes. The decoder bounds every count by the remaining bytes and
rejects a tail over 64 or a window over 256 entries. A blob in the round-1 format (`OCCS`)
is unreadable, so the node treats it as corrupt and starts fresh (no deployed data exists).
Goldens: `testdata/doc-v2.golden`, `state-v3.golden`, `op-v1.golden`. Rewrite with
`-update-golden`.

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
DST shows this on 500 seeds: always exactly 2 rounds. The no-op is held to the same
check-and-set discipline as a write: the recount reads the stored usage and its version,
runs the hook, and if the result equals what it read it reads the key again and returns the
no-op only if the version is still the one it read. If the key changed in between (the hook
or anything it called wrote it), the attempt counts as stale and the loop retries, within
the same `attempts` bound; after the last attempt it returns `ErrPreconditionFailed`.
`Reader.RecountObjects(bucket)` sums live `ObjectMeta` sizes with checked addition and
returns `ErrUsageOverflow` (wire code 136) when the byte or object total does not fit in 64
bits; it never wraps and never saturates, so an impossible total cannot be written as a
quota. The other sum on the quota path, `AddUsage`, was already checked (overflow and
underflow return `env.ErrInvalidArgument`). It is the recount this package can offer, and
Chunk 6 plugs in its manifest recount.

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
| A concurrent replacement is counted, logged (key, loser ID, loser stamp, loser writer, winner ID, winner stamp), kept in the tail and reported to the hook; a rejected concurrent incoming loser is recorded too; re-merging does not re-count on either node; deletes discard puts and the reverse; the counter saturates; the tail is bounded | `TestLWWDiscardCountedAndLogged` |
| A node that rejects an incoming concurrent version records it once (counter, log line naming the loser, hook, tail); five re-sends add nothing; old news one and two steps below the held version records nothing; a failed persist of the rejection leaves no trace (state blob byte-identical) and the retry records once | `TestIncomingConcurrentLoserIsRecorded`; `TestFailedPersistLeavesNoTrace` row "merge rejecting a concurrent incoming loser" |
| The de-duplication window: 257 distinct losers give 257 records; re-sends inside the window add nothing, also after a restart from the persisted state; a loser re-sent after it left the window is recorded again (the stated over-count) | `TestDiscardDedupIsBounded` |
| Causal overwrites (conditional and unconditional, across three nodes) are never counted or logged | `TestCausalOverwriteIsNotADiscard`; property `TestPropNoDiscardWithoutConcurrency` |
| A discarded write's dot is never reissued, and no later version (six writes, by the discarding node and by another node on top of them) claims it; a node still holding the discarded version records its replacement after each of them | `TestDotsAreNeverReused` |
| REVIEW-2 finding 1, exact: after `a` discards its own `lost`, `next2` (written by `a`, and separately by `b` after syncing `next1`) does not supersede `lost`, and `c`, which held `lost`, records exactly one discard naming it | `TestDiscardGapNeverClaimed` |
| Classification equals an independent oracle: on 3–4 replicas with conditional and blind writes, deletes, full and one-key merges, clock moves both ways and wipes, for every replaced held version and every rejected incoming version, the product's causal/concurrent answer (`Supersedes`) equals the oracle's (built only from the version each write held, never from vectors), and the records the merge emits are exactly the oracle's concurrent ones, in key order, minus pairs already recorded on that replica | `TestPropClassificationMatchesOracle` |
| Every acknowledged version ends current on all replicas, or seen by a later acknowledged write, or named as a loser in some node's discard log, or lost with every disk that ever received it (the one limit). Checked against an independent seen-set oracle (not the product's classifier) at every step and after full exchange, on 2–4 replicas with random conditional/blind writes, deletes, merges, clock moves, **wipes** (a replica restarts empty under a fresh epoch) and **permanent crashes** (a replica leaves for good); the oracle tracks which incarnation received each version from the merges themselves; counters (live plus those on lost disks) equal hook records | `TestPropEveryLossRecorded`, fuzz `FuzzOCCConditionalWrite` (same property) |
| REVIEW-2 finding 2, exact: `n1` (clock 5 s behind) delivers a concurrent `v1` to `n2`, which keeps its later `v2`; `n1` then crashes for good, or is wiped and rejoins, or has its state blob bit-rotted and restarts; after the heal `n2`'s log names `v1` and the oracle (with `n1`'s disk loss declared) finds every write explained | `TestIncomingLoserRecordedWhenWriterIsLost` |
| The one remaining limit is the only unrecorded case: a version no other node received, lost with its writer's wiped disk, has no record anywhere and the oracle flags it (and only it) until the disk loss is declared, then calls it `lost-with-disk`; a version that reached a surviving holder is not allowed by a declared disk loss; a later incarnation that received it voids the allowance again | `TestDiskLossBeforeReachIsTheOnlyUnrecordedLoss` |
| Records survive a crash between the durable merge and the emission: opening the state written by the merge emits the record (line, hook, tail, counter 1) and drains it; opening the drained state emits nothing; the 64-entry tail survives restarts; a malformed blob is refused with `ErrMalformed` | `TestDiscardRecordSurvivesCrashBeforePublish` |
| Node level: the drain write fails, the node crashes and restarts: it emits the record again (same line), keeps counter and tail, drains; a further restart emits nothing | `TestNodeReplaysOutboxAfterRestart` |
| `RecountObjects` never wraps: `MaxUint64` alone is exact, `MaxUint64 + 1` returns `ErrUsageOverflow` and a zero usage; a recount through it writes nothing; code 136 round-trips | `TestRecountObjectsNeverWraps` |
| A recount whose result equals its first read but whose key changed meanwhile retries (attempt 2 writes the right figure); a key that never holds still gives up with `ErrPreconditionFailed` and writes nothing; an already-right quota is a one-call no-op | `TestRecountNoopRechecksCurrentVersion` |
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
| A restart with a drained outbox emits nothing and keeps the epoch, counter and data | `TestNodePersistsAndReloadsItsEpoch`, `TestNodeReplaysOutboxAfterRestart` |
| Wire: malformed requests/datagrams, not-found, precondition failures cross as codes; client get and delete results | `TestNodeWireProtocol` |
| The peer drawn does not depend on the membership's order | `TestPeerChoiceIgnoresMembershipOrder` |
| DST, 6 nodes / 3 regions, skew ±400 ms, drift ±2,000 ppm, 2% drop, 5% duplication, 3 nemesis phases (crash, torn crash, restart, pause, partitions incl. one-way and region, isolate, link loss, clog, read errors), mixed workload (conditional, create, delete, blind, stale `If-Match`, scalars), plus 3 scheduled **disk losses** per run at seeded times on seeded nodes, one of each kind: wipe and rejoin, state-blob bit rot then restart, and crash for good with the disk wiped (the node comes back empty only at the next heal, as a replacement machine). A test wrapper around each node reports every document the node actually processed (delta, push, digest reply) to the oracle, so the oracle knows which incarnation received which version. Every 250 events: every-loss-recorded over live and durable states. After the final heal: converged within `ConvergeBound`, byte-identical documents and equal ETags, every acknowledged key holds one version (unless a version of it was lost with a disk), every-loss-recorded on the final state, counters (live plus lost with disks) = hook records = oracle log | `TestMultiRegionConvergenceDST` |
| DST aimed at REVIEW-2 finding 2: 4 nodes in 2 regions, node 1 with its clock 3 s behind; background workload; 6 rounds in which node 1's inbound links are cut, a rival writes every key, node 1 writes every key (concurrent, lower stamps; its deltas reach the others and are rejected there), then node 1 loses its disk (the three kinds in turn) and the network heals. Every-loss-recorded holds throughout and after convergence, with node 1's lost versions explained by the other nodes' records; every seed has recorded discards | `TestWriterLostAfterDeliveryDST` |
| DST: region c offline 60 s while both sides write conditionally, two forced contests (c's later write on `k0`, the other side's on `k1`); every offline write wins, is superseded by a write that saw it, or is recorded as discarded; both kinds occur on every seed; c's later write on `k0` wins | `TestOfflineRegionDST` |
| DST: two writers with one ETag. On one node exactly one wins, the other gets `ErrPreconditionFailed` over the wire, and no discard is recorded. Across a partition both are acknowledged, after the heal both servers hold the same single version (one of the two), and the other's discard is on a server's record | `TestConditionalWriteRaceDST` |
| DST: quota under partition; a quota-key discard is recorded (the scenario really contested it); recounts driven by those discards settle (no node pending, a further recount writes nothing); every node's stored usage = the recount; overshoot ≤ `MaxOvershoot` | `TestQuotaOvershootUnderPartitionDST` |
| Canary: a discard that is neither counted nor logged fails the deterministic two-writer case and the convergence DST | `TestCanaryDiscardNotRecorded` (switch `canaryUnrecordedDiscard`) |
| Canary: rejecting an incoming loser silently (the round-1 behaviour) fails the two-node scenario for each writer fate and the writer-loss DST | `TestCanaryIncomingLoserSilent`, `TestCanaryIncomingLoserSilentDST` (switch `canaryIncomingLoserSilent`) |
| Canary: a merge that keeps the local version (arrival-order winner) fails the merge laws and the convergence DST | `TestCanaryMergePrefersLocal` (switch `canaryMergePrefersLocal`) |

## Named bounds and measured values

Measured in LWW round 2 with `DST_SEEDS=500` (base 1) in the dev container on the shared
4-core droplet, from the `MEASURE` lines of `go test -v`.

| Constant | Value | Measured |
|---|---|---|
| `ConvergeBound` (from the final heal) | 15 s | multi-region p50 2.25 s, p99 4.32 s, max 4.69 s; offline region p50 0.77 s, p99 1.65 s, max 1.79 s; writer-loss p50 0.81 s, p99 3.02 s, max 3.97 s; quota p50 0.51 s, max 1.42 s |
| `invariantEvery` | 250 events | multi-region runs 826–1,174 events, offline 2,209–2,429, writer-loss 639–879 |
| `diskLosses` (multi-region) | 3 per run, one of each kind | 1,500 over 500 seeds, all applied |
| `writerLossRounds` | 6 | 3,000 disk losses of the writer over 500 seeds |
| `raceConverge` | 10 s | passes on every seed |
| `MaxOvershoot = (quotaWriters-1) × quotaLimit` | 3,000 bytes (4 writers, limit 1,000) | see below |
| `maxRecountRounds` | 6 | always 2 (round 1 writes the recount, round 2 writes nothing) |
| `DiscardTailLen` / `DiscardDedupLen` / `DiscardOutboxLen` | 64 / 256 / 1,024 | no DST run comes near the window (largest per-node counter well under 256 per run) |
| `canarySeeds` | 3 | each canary fails its DST on 3 of 3 seeds |

**Discards per partitioned workload.** "Events" is the sum of the node counters (each node
records each (key, loser) once; on lost disks included). "Distinct" is the number of distinct
losing version IDs named in any record. Events now include rejections of incoming losers,
which round 1 did not record.
- *Multi-region DST* (6 writers, partitions and crashes, blind writes and stale `If-Match`,
  3 disk losses per run). 51,161 acknowledged writes over 500 seeds: 3,959 current at the
  end, 32,452 superseded by a later write that saw them, **13,999 explained by a discard
  record**, **751 lost with a disk**: every node that had received them lost its state while
  holding them (the one limit; 0.5 per disk loss on average; not broken down further). Per seed: discard events
  p50 100, p99 145, max 152 (round 1, without disk losses and without rejections recorded:
  p50 75, max 136); distinct lost versions p50 36, p99 50, max 54 (round 1: p50 36, max 55).
- *Offline-region DST* (conditional writes only, no disk loss). 74,443 acknowledged
  writes: 2,000 current, 61,645 superseded, **10,798 explained by a discard record**, none
  unexplained. Per seed: events p50 60, p99 90, max 100 (round 1: p50 47, max 76); distinct
  p50 24, p99 35, max 39 (round 1: p50 24, max 38). The offline region won 834 keys and had
  3,296 writes recorded as discarded; per seed it won 1–3 keys and lost 2–14 writes.
- *Writer-loss DST* (the finding-2 schedule, 6 writer disk losses per run). 62,175
  acknowledged writes: 3,878 current, 41,007 superseded, **17,287 explained by a discard
  record**, 3 lost with the disk (no surviving node had received them). Events p50 81, max 111; distinct p50 41, max 51.
- *Quota DST*: events p50 16, max 27; distinct p50 8, max 14.
- In every seed of every DST, counter sum (live plus lost disks) = callback records = oracle
  log, and no acknowledged write was gone without a record except as allowed by the one
  limit.

**Cost of retiring the actor epoch** (largest causal context in the final documents, the
same 500 seeds, with the retirement switched off for comparison by a temporary edit, not
committed): multi-region p50 6 → 9 entries, max 9 → 15; offline region p50 4 → 11, max 4 →
16; writer-loss p50 6 → 10, max 8 → 16. With the retirement switched off, all three DSTs
still pass on 500 seeds: the DSTs do not produce the finding-1 shape (it needs a node that
still holds the lost version to receive the *second* write after the discard before any
other replacement). The directed tests and the oracle property catch it (see "LWW round 2").

**State blob size** (largest durable state blob at the end of a run; it now carries the
64-entry tail, the outbox and the window): multi-region p50 4,799 bytes, max 7,896
(4,147 / 6,725 without the retirement, i.e. with smaller contexts); offline p50 4,364, max
7,920; writer-loss p50 7,147, max 10,572. The tail is bounded at 64 discards of two versions
each, so its share is bounded by 128 version encodings whatever the workload; the window is
at most 256 × 16 bytes.

**Quota overshoot** (4 writers, two per side, limit 1,000 bytes, 100-byte objects): p50
1,400, p99 2,100, max 2,300 over 500 seeds (round 1: p50 1,400, max 2,400). At least 1,000
because each side fills the limit on its own. More comes from two nodes on one side updating
from the same read before their deltas cross; those updates settle by LWW, the lower update
is lost, and the side under-counts and admits more objects. After the recounts every node
stores the true usage.

**Merge cost** (`BenchmarkMergeDoc`, 10% of keys written concurrently on both sides, loaded
droplet, 20 iterations). Doc merge (clone + merge) 1e3 records 2.1 ms / 0.21 MB, 1e4 records
18 ms / 2.0 MB. Store merge (observe + LWW + classify + undo log, ~10% discards, now with the
records, the window and two state writes skipped because the bench has no persist hook) 1e3
records 4.1 ms / 0.32 MB, 1e4 records 21 ms / 3.4 MB (round 1: 1.0 ms and 19 ms on a differently loaded droplet; the difference was not
profiled). Encode + strict
decode 1e3 records 8.1 ms, 1e4 records 71 ms.

**Hostile-message work** (`TestHostileMessagesBoundedWork`, counted operations): unchanged.
Many keys (16,777,138 bytes): decode 31.2 M operations (1.86 per byte), merge 1.6 M. Big
contexts (16,776,993 bytes): decode 25.2 M (1.50 per byte), merge 11.2 M. A rejected incoming
version now costs one `supersedes` scan, counted in the same work figure, which the test
already bounds.

**Wall times** (loaded droplet). `go test ./internal/occ` 37–43 s; `-race` 34 s; rapid at
2,000 checks 45 s; `DST_SEEDS=500 go test -v ./internal/occ` (everything) 242 s
(multi-region 45 s, offline 63 s, writer-loss 90 s, quota 12 s, race 1 s).

**Fuzz** (loaded droplet, 30 s each): `FuzzOCCConditionalWrite` 11,740 execs,
`FuzzOCCMergeLaws` 17,659, `FuzzOCCDecode` 263,594. No crasher.

## Uncovered branches

Coverage of `internal/occ`: 98.5% of statements (`store.go` 100%). Uncovered:
- `decodeDoc`: the non-canonical-document failure after a clean parse. Every field is
  checked as it is read, so a reachable input needs a non-minimal varint inside an otherwise
  valid document; the malformed table has one at the document level, not inside a version.
  Kept as the final guard.
- `Node.onDigestReply` reply-kind and decode-failure branches and the not-ready guard; an
  honest peer never sends them and replies to a previous incarnation are cancelled.
- `Node.OnRequest`: one malformed-push branch; `Node.broadcast`: the delta-too-large skip
  and `Send` errors.
- `Node.peers` with nil `Membership`; `Node.EncodeState` for a not-ready node.
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
- Not ignored but not returned: `Store.drain`'s state write. Its failure is handled (the
  emitted records stay in the outbox, capped, and are emitted again at the next open); the
  operation that produced them already succeeded durably, so it does not fail the caller.
  The node counts the failure in `Stats.DiskErrors`.
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
   keys, after the node has synced. Records are emitted at least once across crashes (the
   outbox); a consumer that needs exactly-once de-duplicates on `(node, key, loser ID)`.
4. The independent harness check `partition-two-writer` (`agent_work/adversarial/04-occ/sim.go`)
   asserts one hook in total per lost version, the round-1 rule that LWW round 2 item 2(b)
   replaces. It now reports BROKEN although the behaviour is the one the brief requires (see
   "Gate and commands"). Its author should change `w.hooks == 1` to compare with the number
   of nodes that held or received the loser (here 2), or drop it in favour of
   `len(w.lost) == 1`.

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
- (Round 1 chose not to record an incoming loser on the node that rejects it. REVIEW-2
  finding 2 showed that this loses the only record when the writer's state is lost; LWW
  round 2 records it.)

## LWW round 2 (2026-11-22): REVIEW-2 and the independent test report

Brief: `objstore-p2-04-occ-lww-round2.md`. The owner's rule this round defends: an
acknowledged write may be lost to a concurrent write, but never invisibly. Commits:
`1802130` (tests first, against a compile-only skeleton of the new names; every new test
failed on the round-1 code), `3d9a80b` (implementation), `f4d4471` (DST oracle fix for
rotted state found by the 500-seed sweep, hostile state-blob test, measurements), then these
notes.

| Item | What changed | Proving test(s) |
|---|---|---|
| 1. A clock never claims a dot its writer did not see (REVIEW-2 finding 1) | A node retires its actor epoch (fresh epoch, counter 0, `Rotations`+1) when a merge replaces a held version with a concurrent winner and the loser's clock holds more of the node's current actor's dots than the winner's (`Version.clockAt`). Chosen over an exact, gap-aware context; the reasoning and the cost are under "Dots are never reused" and in the measurements | `TestDiscardGapNeverClaimed` (the reviewer's `next2`, second write by `a` and by `b`); `TestDotsAreNeverReused` (six writes, by both nodes; a fresh replica holding `lost` records its replacement after each); `TestPropClassificationMatchesOracle`. With the retirement switched off (temporary edit): the two directed tests fail every time, and the property fails in 5 of 8 runs at the default 100 checks and always at 2,000 checks (`-rapid.checks=2000`, which the gate runs). The DSTs do not catch it (see measurements) |
| 2. A rejected incoming concurrent loser is recorded (finding 2) | `Doc.mergeVersion` reports rejections; `Store.rejected` records `(key, held winner, incoming loser)` unless the held version descends from the incoming one (old news) or the pair is in the persisted de-duplication window. (a) repeated delivery: window of the last 256 recorded `(key, loser ID)` hashes, persisted, FIFO; a pair offered again after leaving the window is recorded again (over-count only). (b) the same loss is now recorded on several nodes; every record carries `loser=<version ID>`; the counter means discard events observed by this node, not distinct lost versions (see "What the counter counts"). (c) old news records nothing. (d) a failed persist of a rejection leaves no trace, the window included | `TestIncomingConcurrentLoserIsRecorded` (records once, 5 re-sends, old news one and two steps down, failed persist byte-identical then one record); `TestDiscardDedupIsBounded`; `TestIncomingLoserRecordedWhenWriterIsLost` (the reviewer's two-node schedule; writer crashed for good, wiped and rejoined, state blob rotted; after the heal `n2`'s log names the lost version); `TestPropEveryLossRecorded` and `TestMultiRegionConvergenceDST` now with wipes and permanent crashes; `TestWriterLostAfterDeliveryDST`; canaries `TestCanaryIncomingLoserSilent` and `TestCanaryIncomingLoserSilentDST` |
| 2. The one remaining limit, and that it is the only one | Stated under "The one remaining limit". The oracle (`helpers_test.go`) tracks, per version, every node *incarnation* that received it (writes, and every document a node actually processed: merges in the property, a wrapper around delta, push and digest-reply delivery in the DSTs) and every incarnation that lost its disk while holding it; an unexplained version is allowed only if the two sets are equal | `TestDiskLossBeforeReachIsTheOnlyUnrecordedLoss`: the unreached, wiped version has no record anywhere and is flagged (only it) until the loss is declared, then allowed as `lost-with-disk`; a surviving holder voids the allowance; a later incarnation receiving it voids it again. The item-2 canaries prove the allowance does not hide a silent rejection: those runs declare the writer's disk loss too and still fail |
| 3. Records survive a crash between the durable merge and emission (finding 3) | Persisted outbox written with the merge, emitted, then drained by a second write; emitted and drained again on open (`OpenStore`, `Node.Start`). The 64-entry tail is persisted. At-least-once emission across crashes; the counter never double-counts. State blob magic `OCS2` | `TestDiscardRecordSurvivesCrashBeforePublish` (the state written by the merge, i.e. the disk at the instant the sync succeeded and before emission, opened by a new store: the record is emitted, counted, in the tail, then drained); `TestNodeReplaysOutboxAfterRestart` (drain write fails, crash, restart: emitted again, then drained; a further restart emits nothing); `TestOpenStoreRejectsHostileState` |
| 4. C23 `recount-overflow` | `RecountObjects` uses checked addition for bytes and objects and returns `ErrUsageOverflow` (new, code 136); never wraps. Audit of the quota path: `AddUsage` already checked both directions; `RecountObjects` was the only unchecked sum; no other sum over sizes or counts exists in the package | `TestRecountObjectsNeverWraps` |
| 5. C17 `recount-stale-noop` | The no-op path re-reads the key after the hook and returns the no-op only if the version is unchanged; otherwise the attempt is stale and the loop retries within `attempts`, ending in `ErrPreconditionFailed` | `TestRecountNoopRechecksCurrentVersion` (the report's exact sequence: two calls, the second writes 5) |
| Canaries | `canaryIncomingLoserSilent` (test switch) restores the round-1 silent rejection; `TestCanaryDiscardNotRecorded` kept and still fails its DST on 3 of 3 seeds | `TestCanaryIncomingLoserSilent`, `TestCanaryIncomingLoserSilentDST` (3 of 3 seeds), `TestCanaryDiscardNotRecorded`, `TestCanaryMergePrefersLocal` |

Test changes: `TestLWWDiscardCountedAndLogged` now requires the rejecting node to record
(round 1 asserted that it did not); `TestDotsAreNeverReused` moved to `lww2_test.go` and no
longer requires a larger counter (after retirement the next dot is `(new epoch, 1)`), it
requires a dot never issued twice and never claimed; `TestFailedPersistLeavesNoTrace`
compares the full state blob and gains a rejection row; `occNode` unwraps the DST's
delivery-tracking wrapper; the golden `state-v2.golden` is replaced by `state-v3.golden`.

### API changes

Added: `OpenStore(cfg, state) (*Store, error)` (`cfg.Epoch`, `Counter`, `Discards` and `Doc` are
ignored: the blob supplies them); `DiscardDedupLen`, `DiscardOutboxLen`;
`ErrUsageOverflow`, `CodeUsageOverflow` (136). Changed: `Store.Merge`'s bool now means "the
document changed" (a merge that only records a rejected loser returns `false` but persists);
the state blob format (`OCS2`, with tail, outbox and window) and so `Store.EncodeState`'s
bytes; `DiscardTail` is now persisted and returned oldest first from a slice (same
contents); `RecountObjects` can return `ErrUsageOverflow`; `RecountQuota` can return
`ErrPreconditionFailed` where it used to return a stale no-op. `StoreConfig`, `Config`,
`Discard` and its log line are unchanged.

### Chosen not to do, and why

- **An exact (gap-aware) causal context** instead of retiring the epoch: see "Dots are never
  reused". It would change the encoding and the hostile-input bounds for a saving of a few
  vector entries.
- **A durable de-duplication set without a bound.** It would grow with every loss forever.
  The window trades a bounded state for a possible over-count after 256 newer records;
  undercounting is never possible.
- **Exactly-once emission across crashes.** It would need the log sink and the callback to
  take part in the store's transaction. At-least-once with the loser ID in every record lets
  a consumer de-duplicate.
- **Making the property test find finding 1 at the default check count every time.** The
  shape needs about eight specific steps; the generator is biased toward it (two keys, 3–4
  replicas, 80–240 steps) and the directed tests cover it exactly.
- **A cap on the records of one transaction.** A merge can record one discard per key of
  the incoming document, bounded by the message size limit; capping it would drop first
  emissions.

## Gate and commands

All through `docker compose run --rm dev` unless noted. Round 2 runs (on `3d9a80b` and its
test-only follow-up `f4d4471`):

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...`, `go build ./...` | clean |
| `go test -count=1 ./...` | ok (`internal/occ` 40.8 s) |
| `go test -race -count=1 ./...` | ok (`internal/occ` 34.0 s, `internal/sim` 41.6 s) |
| `go test -count=1 -cover ./...` | ok; `internal/occ` 98.5% after `f4d4471` (98.2% before it), `internal/sim` 95.4% |
| `go test -count=1 ./internal/occ -rapid.checks=2000` | ok, 45.3 s |
| `DST_SEEDS=500 go test -count=1 ./internal/occ` | first run (`3d9a80b`): FAIL on 4 seeds of `TestWriterLostAfterDeliveryDST` and 1 of `TestMultiRegionConvergenceDST`, all the same test-oracle error (reading the deliberately bit-rotted state blob of a node between `Restart` and `Start`); fixed in the test in `f4d4471`; the five REPLAY commands then passed, and the full rerun `DST_SEEDS=500 go test -count=1 -v ./internal/occ` passed, 241.8 s |
| `go test ./internal/occ -run '^$' -fuzz '^<T>$' -fuzztime 30s -fuzzminimizetime 5s` | `FuzzOCCConditionalWrite` 11,740 execs, `FuzzOCCMergeLaws` 17,659, `FuzzOCCDecode` 263,594; PASS, no crasher |
| `go test -run '^$' -bench BenchmarkMergeDoc -benchmem -benchtime 20x ./internal/occ` | numbers above |
| retirement switched off by a temporary edit (not committed), `DST_SEEDS=500 go test -v -run 'TestMultiRegionConvergenceDST\|TestWriterLostAfterDeliveryDST\|TestOfflineRegionDST'` | all pass; context sizes above |
| `agent_work/gates/gate.sh 04-occ` (host, `3094597`, clean tree) | `gate: overall=pass counts_for_landing=true`. G0 all pass (comments against baseline `6d44948`); G1 unit 31 s; G2 rapid 2,000 checks (seed 53413714705202), 2 packages, 97 s; G3 fuzz 4 targets × 30 s, 134 s; G4 race 35 s; G5 500 seeds from base 53413714705202, 239 s; G6 pass |
| `agent_work/adversarial/04-occ/run-all.sh` (host, `3094597`, harness not edited) | `SUMMARY held=60 broken=1 setup_errors=0 elapsed_seconds=45`. Both round-1 BROKEN checks now hold: `HELD C17 recount-stale-noop` and `HELD C23 recount-overflow`. The one BROKEN is `C6,C9,C31 partition-two-writer` (`seed=4701 seed=4702 missing/excess discard`), which held in round 1. It asserts `w.lost[loser] && len(w.lost) == 1 && w.hooks == 1`: exactly one record in total for the one lost version. Item 2(b) of this round's brief replaces that rule: the winner's node now records the rejected loser too. A throwaway probe of the same schedule (same seeds, network and period; not committed) showed on all 10 seeds one lost version, recorded once on each node (counters 1 and 1, two hooks naming the same version ID): `len(lost) == 1` and counter = hooks = logs hold, `hooks == 1` cannot. The check needs `hooks` compared with the number of nodes that held or received the loser, or `len(lost) == 1` alone; the harness is not this seat's to edit (see "Requests to the orchestrator"). The harness's committed evidence files were restored after the run so its own record stays as its author left it |

Round 1 for reference: the gate passed on `0879e715` (`overall=pass counts_for_landing=true`).

The result files under `agent_work/04-occ/gates/` are left uncommitted, as the brief allows.
