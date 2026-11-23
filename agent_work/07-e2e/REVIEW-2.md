VERDICT: NITS

This chunk's finish was reviewed by Fable under the owner's seat-swap ruling (builder:
Gemini 4 Neon; reviewer: Fable; adversarial pass: Opus). The full record of all four
rounds is `agent_work/07-e2e/FABLE-REVIEW.md`; this file is the verdict the landing script
reads, and it supersedes `REVIEW-1.md` (FAIL, Neon reviewing the first Opus build).

- Round 1: FAIL (one major, two minors, three nits) → the builder's one fix round.
- Round 2: PASS (all six closed).
- Round 3, after the adversarial fix round: NITS (findings F1–F5 closed, the product fix
  accepted; two missing tests, R3-1 and R3-2).
- Round 4: PASS (R3-1 and R3-2 closed by tests that kill both surviving mutants; the
  loopback port race the full gate found, R3-3, closed).

NITS CLOSED (2026-11-23): R3-1 and R3-2 closed; see round 4 of FABLE-REVIEW.md.
