# Brief — Chunk 1 REVIEW 2: SWIM gossip membership, after a repair round

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch `chunk/01-gossip`).
READ-ONLY: the ONLY file you create is `agent_work/01-gossip/REVIEW-2.md`.

Earlier reviews are `agent_work/01-gossip/REVIEW-*.md`; adversarial results, if any, are
`agent_work/adversarial/01-gossip/RESULTS.md` and `ATTACKS.md`. The repair brief is the newest
`agent_work/01-gossip/objstore-p2-01-gossip-repair*.md`; the repair is `git log 8f3dc3d2cce61f1213c5bf4709c5579d7e85cd53..HEAD`,
diff `git diff 8f3dc3d2cce61f1213c5bf4709c5579d7e85cd53..HEAD -- internal cmd agent_work/gates`; its account is the newest
round section of `agent_work/01-gossip/build-notes.md`. Product Go is comment-free on purpose.

Decide: (1) for each item of the repair brief — FIXED, PARTLY FIXED, NOT FIXED, or
CORRECTLY NARROWED (the claim is restated in the build notes in a form the tests prove);
check code and tests, not notes; does each regression test fail on the old code
(`git show 8f3dc3d2cce61f1213c5bf4709c5579d7e85cd53:<file>`)? (2) any new BLOCKER or MAJOR introduced (wrong behaviour, a
determinism leak, a test weakened to pass, an edit to the frozen simulator packages).
Run tests through Docker only (`docker compose run --rm -T dev go test ./internal/gossip/...`, also
`-race`, `DST_SEEDS=50`, `-rapid.checks=2000` where rapid is imported).

Output `agent_work/01-gossip/REVIEW-2.md`: first line exactly `VERDICT: PASS`,
`VERDICT: NITS` or `VERDICT: FAIL`; the item table with one line of evidence each; new
findings numbered with severity (BLOCKER / MAJOR / NIT / NOTE), file and line,
reproduction, smallest fix; what you ran versus read. FAIL needs a BLOCKER or MAJOR.

## Two extra points for this re-review
- `agent_work/adversarial/01-gossip/RESULTS.md`, item N1: one independent check
  (`relay-once-original-sequence`) is still scored BROKEN after the repair, and the
  orchestrator reads it as pinning behaviour that review 1's finding 2 asked to remove.
  Confirm or reject that reading from the code and the check's source
  (`agent_work/adversarial/01-gossip/`).
- The repair introduced tombstones for expired dead entries (bounded at 4,096) and a
  `MaxMembers` cap, and it runs the N=100 convergence seeds in four shards. Check: what
  happens at each bound (is the eviction rule safe — can evicting a tombstone reopen the
  resurrection hole, and is that stated); that the sharded sweep still runs every seed
  with every check (nothing dropped, results independent of shard scheduling, replay
  command still exact).
