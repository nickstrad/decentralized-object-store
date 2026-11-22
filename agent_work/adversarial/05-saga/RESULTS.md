# Chunk 5 adversarial results (gate G7)

Harness: `run-all.sh` (four drivers over one nested Go module: codec, engine, migration,
supplement), written by Astra in two runs, run by the orchestrator. Check list and
reproductions: `ATTACKS.md`. Raw output: `evidence/`.

| Run | Tree | Held | Broken |
|---|---|---|---|
| 0, Astra's first, interrupted run (harness half-built) | de16c0b + notes | 33 | 85 |
| 1, Astra's finished harness, before the repair round | 3bf50ed | 125 | 0 |
| 2, after the repair round (final) | b517771 | 125 | 0 |

## What run 0 really was

The interrupted run's 85 BROKEN lines were read at the time as a harness set-up mistake,
and the finished harness "corrected" them by building buckets of 1, 8 and 32 objects
instead of 33. That reading was half wrong. The failures were real: the model store
could not reload or copy a bucket of more than 32 objects (two fields per object against
a 64-field decoder cap), which the independent review found separately as its second
MAJOR. The repair round replaced the object-list codec and moved the nemesis scenario
off the limit; the builder's regression tests cover buckets above the old limit. The
harness itself still uses at most 32 objects, so it does not exercise that fix — the
product's own tests do (`agent_work/05-saga/build-notes.md`, "Repair round").

## Run 2

All 125 check groups held on the repaired tree: the log codec under malformed input, the
engine state machine and resume, the 78-cell failure matrix on fresh seeds, the nemesis
and dueling-migration scenarios, the skipped-compensation canary, and the wire-code and
fence checks. No claim was narrowed.
