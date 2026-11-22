# Chunk 4 — optimistic concurrency control and last-writer-wins: findings, limits, pitfalls

What was built, the encodings, the claims-to-tests table and every measurement are in
`agent_work/04-occ/build-notes.md` (the sections "Repair round" and "LWW round 2" hold what
each round changed); the reviews are `REVIEW-2.md` and `ARBITRATION-1.md` beside it; the
outside harness is in `agent_work/adversarial/04-occ/RESULTS.md`. This entry holds what a
later builder must know and would not guess.

## The ruling and the exact guarantee

- Owner ruling (PLAN.md 9.1, "LWW ruling"): the first build kept concurrent versions of a
  key as siblings; that was ruled out. Each key holds exactly one version. Two concurrent
  versions settle by last-writer-wins, and the loser is discarded on the record. The system
  guarantees convergence and a record of the loss, not preservation of both writes.
- Winner order (`Version.Compare`): stamp wall time, then logical counter, then node ID,
  then version ID bytes. Only forged input ties as far as the version ID. Nothing else
  decides: not arrival order, not which side is local, not version vectors. Vectors only
  classify a replacement as a causal overwrite (nothing recorded) or a concurrent discard.
- The guarantee, in the wording that held up in arbitration: every concurrent discard is
  counted and emitted (log line and `OnDiscard`), with replay after a crash. The records
  are local to a node. They are not replicated and are not a permanent archive: the readable
  tail is 64 entries, and keeping the emitted records is the job of the configured sinks.
- One lost version can be recorded on several nodes, and after 256 newer records on one
  node (the de-duplication window) more than once on that node. This is an over-count,
  never an under-count. `LWWDiscards` counts discard events seen by this node; to count
  distinct losses, join the log lines on `loser=<version ID>`.
- The one unrecorded loss: a version whose every holder, its writer included, lost its disk
  before passing it on. No node ever saw it lose.
- Emission is at least once; for exactly once, de-duplicate on `(node, key, loser ID)`.

## Why the rules are what they are

- A node retires its actor epoch when a discard leaves a gap in its own dots (review 2,
  finding 1). A vector entry `a:N` claims all of `a`'s dots `1..N`. After `a`'s dot 2 is
  discarded, `a`'s second later write would carry `a:3` in its context and so claim the
  discarded dot; a third node still holding that version would then drop it as a causal
  overwrite and record nothing. With a fresh epoch and a counter restarted at 0, no later
  context can reach the gap. The cost is one more vector entry per such discard: the largest
  context in the final documents grew from p50 6 to 9 entries (max 9 to 15) in the
  multi-region sweep.
- A node that rejects an incoming concurrent version records it, as well as a node that
  replaces a held one (finding 2). Otherwise the record depends on the writer's node
  surviving until it learns the winner.
- A far-future stamp pins only its own key. `Observe` refuses a stamp more than 1 hour
  ahead, and the node clock does not move. A local write over that version takes the stamp
  just above it, so the key stays writable; concurrent writes to that key from other nodes
  lose, on the record, until real time passes the stamp. At the end of the stamp space the
  write is refused with `ErrClockExhausted`.
- A failed persist leaves no trace in the document, the ETags, the actor, the counters,
  the clock, the discard state or the log; the same call works once the disk recovers.

## Using the package (integration chunk)

- All writes go through `*Node` (`Put`, `Delete`, `SetScalar`, `UpdateQuota`,
  `RecountQuota`, `PutTag`, `PutACL`, `PutObjectMeta`, `PutManifest`, `SetRoute`,
  `SetContentType`): they persist, then broadcast. `Node.Reader()` is read-only.
- `Membership` is declared here: `Peers() []env.NodeID`. The node sorts the list and drops
  duplicates and its own ID. Only test code supplies it so far.
- The node does not schedule quota recounts. It reports quota-key discards through
  `OnDiscard` and offers `RecountQuota`, which writes nothing when the stored usage is
  already right. The host decides when to recount, and must wait until the node has synced:
  a recount made before the other side's objects arrive writes a wrong figure.
- State blob (`occ/state`): `OCS2`, SHA-256 of the payload, then epoch, counter, discard
  count, tail, outbox, window and the document. A blob in the older format (`OCCS`), like any
  unreadable blob, is treated as corrupt: the node starts empty under a fresh epoch.
- Wire codes: occ owns block 128–143 and uses 128–130 and 132–136; 131 is unassigned on
  purpose. Project blocks: gossip 80–95, storage 96–111, merkle 112–127, occ 128–143, saga
  144–159.

## Measurements

Virtual time, 500 seeds, in the `dev` container.

- Contested multi-region sweep (6 writers in 3 regions, partitions, crashes, blind writes,
  stale `If-Match`, 3 disk losses per run): of 51,161 acknowledged writes, 13,999 (27.4%)
  ended as recorded discards, 32,452 were overwritten by a write that saw them, 3,959 were
  current at the end and 751 were lost with a disk.
- Convergence after the final heal, bound 15 s: p50 2.25 s, max 4.69 s in that sweep.
- Quota overshoot under partition (4 writers, limit 1,000 bytes, 100-byte objects): p50
  1,400, p99 2,100, max 2,300 bytes above the limit; bound 3,000. Recounts settle in 2
  rounds on every seed.

## Pitfalls

- The simulation sweeps alone do not catch finding 1: with the epoch retirement switched
  off they still pass on 500 seeds. `TestDiscardGapNeverClaimed`, `TestDotsAreNeverReused`
  and the oracle property `TestPropClassificationMatchesOracle` catch it.
- Do not assert one discard record per lost version. A node records only a pair it compares,
  so one loss gives one record or two in the two-writer case, depending on delivery
  (`RESULTS.md` N1). Assert on the distinct losing version IDs.
