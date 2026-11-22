# Chunk 6 adversarial results (gate G7 of the pipeline: independent testing)

Harness: `run-all.sh` over one nested Go module plus a small Python supplement, written
by Astra in two rounds, run by the orchestrator. Check list, sample sizes and exact
reproductions: `ATTACKS.md`. Raw output: `evidence/`.

| Run | Tree | Held | Broken |
|---|---|---|---|
| 1, first harness, before the repair round (20 seeds per full-node scenario) | c233622 + notes | 284 | 0 |
| 2, harness updated after the repair round and the package moves | 7deacfd + gate commits | 658 | 4 |
| 3, after the entity-tag list fix (final) | 3e17d91 | 662 | 0 |

## What run 1 missed

Run 1 held everything, and the independent code review then found one blocker and eight
majors — among them a migration that could destroy the shards of a write acknowledged
just before the routing flip. The first harness's migration scenarios, like the
builder's, waited for the cluster to settle before starting a migration. Run 1 is kept
in the table as a record that a small clean sample was not evidence of absence.

## Run 2: the harness rebuilt for the repaired tree

The harness was updated for the moved packages (`internal/noderuntime`,
`internal/storage/httpapi`) and extended with the fault classes the review found:
writes arriving on non-coordinator nodes through a whole migration (both outcomes), a
node away during a migration returning with an unseen write, two migrations started at
once, quota usage against an independent recount after every heal, precondition headers
against an independent model, the file disk at crash points, and shutdown after a loop
panic and during a migration.

- **R1 `http-model-*-quoted-comma-list` (C31), four checks — repaired.** A valid
  `If-Match` list whose entity-tag contains a comma inside the quotes was rejected with
  400 on the four mutation routes (the list was split on commas before the quoted strings
  were read). The parser now scans entity-tags left to right; the four checks hold.

## Run 3

All 662 checks held. One expectation changed on purpose between runs 1 and 2 and is
recorded in `ATTACKS.md`: an `If-Match` naming an entity-tag the node does not hold now
returns 412, not 400.

## Limits

Sample sizes are tens of seeds per full-node scenario (see `ATTACKS.md`), not the
500-seed sweeps of the product's own tests; about a tenth of the checks re-run the
product's own named tests under the harness's seeds. The full 78-cell × 500-seed
migration matrix on real components is not run here or in the gate; it belongs to the
hardening chunk.

## Rerun after the loss-declaration fix and the second-eyes round (2026-11-22)

`run-all.sh` on the chunk branch after build-notes sections 14 and 15: 662 held, 0 broken, no set-up
errors, 404 s. The harness has no scenario for loss declaration; that rule is covered by the node
package's own tests, the confirm pass (`06-integration/ARBITRATION-2.md`) and the second independent
pass, whose scenarios are kept in `neon-second-eyes/` and run as `internal/node/secondeyes_*_test.go`.
