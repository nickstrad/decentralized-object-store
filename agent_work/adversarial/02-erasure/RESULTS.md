# Chunk 2 adversarial results (gate G7)

Harness: `run-all.sh` (five drivers over one nested Go module with an independent
polynomial / Cauchy / elimination oracle), written by Astra, run by the orchestrator.
Check list and exact reproductions: `ATTACKS.md`. Raw output: `evidence/`.

| Run | Tree | Held | Broken |
|---|---|---|---|
| 1, after review 1, before the repair round | 265dc9c | 41 | 1 |
| 2, after the repair round (final) | 91e0727 | 41 | 1 |

## The one check that is scored BROKEN

- **N1 `window-pending-stripes` (C30) — claim narrowed in writing, behaviour kept.** With
  `Window=1` and one paused holder, 2, 8 and 64 stripes still have one outstanding shard
  request each when the PUT is acknowledged (seed 220211, 5 bytes at stripe size 4). The
  repair round decided that `Window` bounds the stripes that are launched and not yet at
  their write quorum, not the requests still outstanding to slow holders: capping those
  would let one paused or dead holder throttle every PUT to `Window` stripes per request
  timeout. What stays outstanding after the acknowledgement is stated in the build notes
  ("Repair round", C30): at most one shard request per stripe per slow holder, for at most
  the request timeout, and the coordinator no longer holds the caller's object while they
  wait. `TestNodePutWindowBoundsStripesAwaitingQuorum` pins the restated bound on Astra's
  own set-up. The check measures outstanding requests, so it keeps reporting BROKEN; that
  is the narrowed claim, not a regression.

## Everything else

41 checks over claims C1–C29 and the streaming half of C30 held in both runs, including
the exhaustive loss-mask sweep, the differential encoder oracle, the self-consistent
forged-shard checks and the fault-matrix seeds. The review-1 defects Astra deliberately
skipped (unsynced Put acknowledgement, callback re-entrancy, overflow on hostile k and m)
were repaired in the same round and are covered by the builder's regression tests.
