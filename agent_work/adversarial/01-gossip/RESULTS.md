# Chunk 1 adversarial results (gate G7)

Harness: `run-all.sh` (four drivers over one nested Go module: core, scenarios, scaling,
canary), written by Astra, run by the orchestrator. Astra's run was cut off by its
provider before it wrote `ATTACKS.md`; the claims are numbered as in the claims table of
`agent_work/01-gossip/build-notes.md` and each check line in `evidence/run-all.txt` names
its claim, set-up and seed, which is the reproduction.

| Run | Tree | Held | Broken |
|---|---|---|---|
| 1, during review 1, before the repair round | 6bccb38 | 69 | 4 |
| 2, after the repair round (final) | 7e8a72f | 72 | 1 |

## The four checks that broke in run 1 — all repaired

- **R1 `retention-monotonicity` (C4) and `stale-replay-after-purge` (C23), seed 901.** A
  purged dead member could be brought back by a replayed `alive` at a lower incarnation.
  Each node now keeps a bounded tombstone (at most 4,096, oldest dropped first) for an
  expired dead or left entry; old news about it is dropped or answered with the recorded
  verdict. Both HELD.
- **R2 `partition-heal-retained-false` (seed 1401) and `default-retention-long-partition`
  (seed 1405), C19.** After a partition longer than the retention period, halves that
  had joined through `Join(seeds)` never re-merged. Seeds given to `Join` and the
  tombstones are now used by the heal path. Both HELD.

## The one check scored BROKEN in run 2

- **N1 `relay-once-original-sequence` (C7), seed 901 — the check pins behaviour the review
  asked to remove.** The check sends a ping-req whose target is an ID that does not
  exist, so the relayed ping cannot be sent, and then expects an ack for that ping to be
  forwarded. Review 1 (finding 2, unbounded relay state) required that no relay entry be
  kept for a ping that was not sent; the repair does that. Relaying through a reachable
  member, once and with the original sequence number, is covered by the product's own
  tests. The re-review is asked to confirm this reading.

## Everything else

The other 72 checks held in run 2, including detection and convergence times at N = 20,
50, 100 and 200, loss and duplication up to 10%, one-way link loss, pause-and-refute,
wipe-and-rejoin with the same ID, flapping, and the indirect-probe canary.
