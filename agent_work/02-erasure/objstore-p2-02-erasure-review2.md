# Brief — Chunk 2 REVIEW 2: Reed-Solomon erasure coding and the chunk store, after a repair round

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch `chunk/02-erasure`).
READ-ONLY: the ONLY file you create is `agent_work/02-erasure/REVIEW-2.md`.

Earlier reviews are `agent_work/02-erasure/REVIEW-*.md`; adversarial results, if any, are
`agent_work/adversarial/02-erasure/RESULTS.md` and `ATTACKS.md`. The repair brief is the newest
`agent_work/02-erasure/objstore-p2-02-erasure-repair*.md`; the repair is `git log 3ae7e3fb25bf94097b899b42f0a090d4e0847965..HEAD`,
diff `git diff 3ae7e3fb25bf94097b899b42f0a090d4e0847965..HEAD -- internal cmd agent_work/gates`; its account is the newest
round section of `agent_work/02-erasure/build-notes.md`. Product Go is comment-free on purpose.

Decide: (1) for each item of the repair brief — FIXED, PARTLY FIXED, NOT FIXED, or
CORRECTLY NARROWED (the claim is restated in the build notes in a form the tests prove);
check code and tests, not notes; does each regression test fail on the old code
(`git show 3ae7e3fb25bf94097b899b42f0a090d4e0847965:<file>`)? (2) any new BLOCKER or MAJOR introduced (wrong behaviour, a
determinism leak, a test weakened to pass, an edit to the frozen simulator packages).
Run tests through Docker only (`docker compose run --rm -T dev go test ./internal/erasure/...`, also
`-race`, `DST_SEEDS=50`, `-rapid.checks=2000` where rapid is imported).

Output `agent_work/02-erasure/REVIEW-2.md`: first line exactly `VERDICT: PASS`,
`VERDICT: NITS` or `VERDICT: FAIL`; the item table with one line of evidence each; new
findings numbered with severity (BLOCKER / MAJOR / NIT / NOTE), file and line,
reproduction, smallest fix; what you ran versus read. FAIL needs a BLOCKER or MAJOR.
