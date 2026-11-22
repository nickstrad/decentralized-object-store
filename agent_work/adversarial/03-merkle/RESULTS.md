# Chunk 3 adversarial results (gate G7)

Harness: `run-all.sh` over one nested Go module, written by Astra, run by the
orchestrator. Check list and exact reproductions: `ATTACKS.md`. Raw output: `evidence/`.

| Run | Tree | Held | Broken |
|---|---|---|---|
| 1, during review 1, before the repair round | 3c0a41e + notes | 70 | 4 |
| 2, after the repair round (final) |  | 72 | 2 |

## The four checks that broke in run 1

- **R1 `single-leaf-reply-size` (C8) — repaired.** A single leaf with 1,024-byte keys was
  answered with 1,049,541 bytes, over the 1 MiB reply cap. Leaf replies are now split so
  every reply obeys one stated size bound; inner replies obey it too. HELD.
- **R2 `batched-round-trip-bound` (C12) — repaired by restating the bound and making the
  check's case hold.** HELD.
- **N1 `sim-prefix-reply-tight-bound` (C21) — claim restated in writing, still scored
  BROKEN.** The check pins "round trips ≤ Depth + 1". A peer that answers one leaf per
  reply needs one round trip per leaf, so three round trips is correct there. The bound
  is now stated exactly: one round trip per tree level, plus one for each extra reply a
  leaf's entries need under the reply cap; the property tests assert that form tightly.
  Data converged correctly in the check.
- **N2 `clustered-hash-cost` (C31) — claim restated in writing, still scored BROKEN.** The
  check pins the plan's first wording, 16·d·(Depth+1) hashes; 46 keys in one leaf cost 49
  hashes against 48. The exact bound is 1 + 16·d·Depth inner hashes plus the entries of
  the mismatched leaves; PLAN.md sections 2.2 and 7.4 carry the restated bound.

## Everything else

The other 70 checks held in both runs: completeness of the diff against an independent
set comparison, domain separation of leaf and inner hashes, malformed and truncated wire
input, a peer that lies in the protocol, mutation during a diff, rot in the stored tree,
and repair convergence under loss.
