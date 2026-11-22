# Chunk 4 adversarial results (gate G7)

Harness: `run-all.sh` over one nested Go module, written by Astra against the
last-writer-wins design, run by the orchestrator. Check list and exact reproductions:
`ATTACKS.md`. Raw output: `evidence/`. (An earlier Astra run against the first,
sibling-keeping build was cut off by its provider and produced nothing usable; that
design was then ruled out by the owner, so no results are recorded for it.)

| Run | Tree | Held | Broken |
|---|---|---|---|
| 1, after the LWW rework, before round 2 | 9eb2804 + notes | 59 | 2 |
| 2, after LWW round 2 (final) | 1e02c71 | 60 | 1 |

## The two checks that broke in run 1 — both repaired

- **R1 `recount-overflow` (C23), seed 4701.** Two objects of sizes MaxUint64 and 1
  recounted to zero bytes with no error. Totals no longer wrap: the recount returns a
  typed overflow error. HELD.
- **R2 `recount-stale-noop` (C17), seed 4701.** A recount whose result equalled the value
  read first skipped the staleness check while the stored value changed underneath. The
  shortcut now re-checks the stored version and retries. HELD.

## The one check scored BROKEN in run 2

- **N1 `partition-two-writer` — the check pins behaviour that round 2 changed on
  purpose.** The check requires exactly ONE discard record for one lost version. The
  independent review then found that recording a loss only on the node that held the
  losing version lets the record disappear with that node; round 2 makes a node that
  rejects an incoming concurrent version record the loss too, so the same lost version
  is now recorded once on each node that saw it (each record carries the losing version
  ID, so records can be joined; the counter means discard events seen by this node).
  The lost version is still recorded — on more nodes than the check expects. The
  arbiter is asked to confirm this reading.

## Everything else

The other 60 checks held in run 2: no acknowledged write vanished without a causal
successor or a discard record, replicas holding the same versions were byte-identical,
the winner did not depend on delivery order, causally ordered overwrites counted no
discard, malformed and hand-built states broke no merge law, and the wiped-node epoch
guarantee held.
