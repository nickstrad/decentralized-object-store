# Brief — Chunk 3 REVIEW 2: Merkle-tree anti-entropy, after a repair round

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch `chunk/03-merkle`).
READ-ONLY: the ONLY file you create is `agent_work/03-merkle/REVIEW-2.md`.

Earlier reviews are `agent_work/03-merkle/REVIEW-*.md`; adversarial results, if any, are
`agent_work/adversarial/03-merkle/RESULTS.md` and `ATTACKS.md`. The repair brief is the newest
`agent_work/03-merkle/objstore-p2-03-merkle-repair*.md`; the repair is `git log 579aa87c1dbf931b5c88c962cf676f24d2e505ba..HEAD`,
diff `git diff 579aa87c1dbf931b5c88c962cf676f24d2e505ba..HEAD -- internal cmd agent_work/gates`; its account is the newest
round section of `agent_work/03-merkle/build-notes.md`. Product Go is comment-free on purpose.

Decide: (1) for each item of the repair brief — FIXED, PARTLY FIXED, NOT FIXED, or
CORRECTLY NARROWED (the claim is restated in the build notes in a form the tests prove);
check code and tests, not notes; does each regression test fail on the old code
(`git show 579aa87c1dbf931b5c88c962cf676f24d2e505ba:<file>`)? (2) any new BLOCKER or MAJOR introduced (wrong behaviour, a
determinism leak, a test weakened to pass, an edit to the frozen simulator packages).
Run tests through Docker only (`docker compose run --rm -T dev go test ./internal/merkle/...`, also
`-race`, `DST_SEEDS=50`, `-rapid.checks=2000` where rapid is imported).

Output `agent_work/03-merkle/REVIEW-2.md`: first line exactly `VERDICT: PASS`,
`VERDICT: NITS` or `VERDICT: FAIL`; the item table with one line of evidence each; new
findings numbered with severity (BLOCKER / MAJOR / NIT / NOTE), file and line,
reproduction, smallest fix; what you ran versus read. FAIL needs a BLOCKER or MAJOR.

## One extra point
`agent_work/adversarial/03-merkle/RESULTS.md` items N1 and N2: two independent checks are
still scored BROKEN because they pin the first, inexact cost bounds; the repair restated
the bounds (build notes, "Repair round" item 3) and PLAN.md 2.2 and 7.4 now carry the
restated form. Confirm or reject, from the code and the property tests, that the
restated bounds are exactly right and tightly asserted (not merely loosened until the
tests pass).
