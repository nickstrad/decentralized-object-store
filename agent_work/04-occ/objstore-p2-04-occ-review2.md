# Brief — Chunk 4 REVIEW 2: optimistic concurrency control for metadata, after a repair round

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch `chunk/04-occ`).
READ-ONLY: the ONLY file you create is `agent_work/04-occ/REVIEW-2.md`.

Earlier reviews are `agent_work/04-occ/REVIEW-*.md`; adversarial results, if any, are
`agent_work/adversarial/04-occ/RESULTS.md` and `ATTACKS.md`. The repair brief is the newest
`agent_work/04-occ/objstore-p2-04-occ-repair*.md`; the repair is `git log 8ad382e1f84766701edc7a19f0262a1818a01a9b..HEAD`,
diff `git diff 8ad382e1f84766701edc7a19f0262a1818a01a9b..HEAD -- internal cmd agent_work/gates`; its account is the newest
round section of `agent_work/04-occ/build-notes.md`. Product Go is comment-free on purpose.

Decide: (1) for each item of the repair brief — FIXED, PARTLY FIXED, NOT FIXED, or
CORRECTLY NARROWED (the claim is restated in the build notes in a form the tests prove);
check code and tests, not notes; does each regression test fail on the old code
(`git show 8ad382e1f84766701edc7a19f0262a1818a01a9b:<file>`)? (2) any new BLOCKER or MAJOR introduced (wrong behaviour, a
determinism leak, a test weakened to pass, an edit to the frozen simulator packages).
Run tests through Docker only (`docker compose run --rm -T dev go test ./internal/occ/...`, also
`-race`, `DST_SEEDS=50`, `-rapid.checks=2000` where rapid is imported).

Output `agent_work/04-occ/REVIEW-2.md`: first line exactly `VERDICT: PASS`,
`VERDICT: NITS` or `VERDICT: FAIL`; the item table with one line of evidence each; new
findings numbered with severity (BLOCKER / MAJOR / NIT / NOTE), file and line,
reproduction, smallest fix; what you ran versus read. FAIL needs a BLOCKER or MAJOR.

## This repair also carried an owner design ruling — review it as such
Part A of the repair brief is binding design, not a suggestion: siblings are OUT; each key
holds one version; the winner is last-writer-wins by hybrid logical clock stamp, then
node ID (then version ID on forged ties) — a pure function of the two versions; every
concurrent write that loses is counted and logged. PLAN.md 4.2 "Chunk 4", 7.5 and the LWW
ruling in 9.1 say the same. Do NOT report the absence of siblings or the loss of an
acknowledged concurrent write as a defect. DO check, with reproductions where you can:
(a) is the winner really independent of arrival order and of which side is local, on
honest and on arbitrary decoded states; (b) is EVERY discard recorded — look hard at the
builder's choice to record a loss only on the node that held the losing version, not on a
node that received and rejected it: is there any schedule where an acknowledged version
disappears from every node with no causal successor and no discard record anywhere
(crash between merge and log, a failed persist, a loser that only ever lived on a node
that then crashed after acknowledging); (c) does `TestPropEveryLossRecorded` use an
oracle independent of the product's own classifier; (d) the far-future-stamp decision
(a local write takes a stamp just above the held version without moving the node clock):
can an acknowledged local write ever lose to the version it replaced, and what can one
forged stamp do to a key; (e) no reader-facing identifier, error string or log message
still speaks of siblings or conflicts as the design.
