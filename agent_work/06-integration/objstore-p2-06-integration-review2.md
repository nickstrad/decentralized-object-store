# Brief — Chunk 6 REVIEW 2: integration: the object store and sync engine, after a repair round

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch `chunk/06-integration`).
READ-ONLY: the ONLY file you create is `agent_work/06-integration/REVIEW-2.md`.

Earlier reviews are `agent_work/06-integration/REVIEW-*.md`; adversarial results, if any, are
`agent_work/adversarial/06-integration/RESULTS.md` and `ATTACKS.md`. The repair brief is the newest
`agent_work/06-integration/objstore-p2-06-integration-repair*.md`; the repair is `git log 534c4e7681e8440ef4c6c876a1640a195a5e0394..HEAD`,
diff `git diff 534c4e7681e8440ef4c6c876a1640a195a5e0394..HEAD -- internal cmd agent_work/gates`; its account is the newest
round section of `agent_work/06-integration/build-notes.md`. Product Go is comment-free on purpose.

Decide: (1) for each item of the repair brief — FIXED, PARTLY FIXED, NOT FIXED, or
CORRECTLY NARROWED (the claim is restated in the build notes in a form the tests prove);
check code and tests, not notes; does each regression test fail on the old code
(`git show 534c4e7681e8440ef4c6c876a1640a195a5e0394:<file>`)? (2) any new BLOCKER or MAJOR introduced (wrong behaviour, a
determinism leak, a test weakened to pass, an edit to the frozen simulator packages).
Run tests through Docker only (`docker compose run --rm -T dev go test ./internal/node/...`, also
`-race`, `DST_SEEDS=50`, `-rapid.checks=2000` where rapid is imported).

Output `agent_work/06-integration/REVIEW-2.md`: first line exactly `VERDICT: PASS`,
`VERDICT: NITS` or `VERDICT: FAIL`; the item table with one line of evidence each; new
findings numbered with severity (BLOCKER / MAJOR / NIT / NOTE), file and line,
reproduction, smallest fix; what you ran versus read. FAIL needs a BLOCKER or MAJOR.

## Specific to this re-review — read before you start
Since REVIEW-1 the branch has, in order (`git log --oneline <base>..HEAD`):
(a) the Opus repair round (brief `objstore-p2-06-integration-repair1.md`; account: build
    notes section 10), which also moved the real runtime to `internal/noderuntime`;
(b) ORCHESTRATOR gate-tooling commits (subjects starting `gates:`): a host-side cluster
    check G7 in `agent_work/gates/gate.sh` that `agent_work/orch/land.sh` now requires;
    `internal/node` added to the determinism-lint roots; coverage floors and the repeated
    race run extended to the new packages; the race canary moved to its own package; and
    a single guarded retry in `agent_work/gates/fuzz-short.sh` with a new `fuzz-stall`
    canary;
(c) a small Opus move of the HTTP server to `internal/storage/httpapi` (brief
    `objstore-p2-06-integration-httpsplit.md`) — required because the lint follows the
    protocol core's imports;
(d) an updated independent harness and its results
    (`agent_work/adversarial/06-integration/RESULTS.md`: 662 held, 0 broken);
(e) a small Opus fix of entity-tag list parsing (brief `objstore-p2-06-integration-etagfix.md`).
Binding design ruling, unchanged: last-writer-wins, one version per key, every discard
counted and logged; no siblings.

Decide, in addition to the per-item table for REVIEW-1 findings 1–11:
1. Finding 1 (the blocker). Try hard to break the new rule: before retiring, every live
   peer must agree with the coordinator on route, flip owner and a digest of the
   bucket's manifests; a purge deletes only versions with a recorded death marker; a
   holder refuses to delete a version its own metadata calls live; shard deletes over the
   wire are refused. Is there still any schedule — a peer that is not live at the
   agreement check and returns later, a write racing the agreement itself, a coordinator
   restart between agreement and retire, two migrations — in which a key the metadata
   calls live ends permanently unreadable after heal? The builder states one residual
   (a peer away longer than the one-hour orphan grace can lose its write, recorded and
   declared lost): is that exactly what the code does, is it tested, and is it stated
   plainly in the notes as a limit?
2. The test-only hooks the repair added to product code (`Config.CommitSink`,
   `Config.OCCFanout`, a third canary switch, `startHook`/`dequeueHook` on the runtime):
   can any of them change behaviour in a production configuration, and are they
   unreachable from `cmd/objnode` flags and config?
3. The gate changes in (b): is G7 sound (can a red cluster check still leave a result
   that `land.sh` accepts; can a result count for landing without G7 once `cmd/objnode`
   exists — the in-container direct invocation path is a known, stated gap: say whether
   it matters)? The fuzz retry: does it weaken G3 — could a real defect now pass that
   failed before? It retries ONCE and only when the log has exactly one `--- FAIL`, the
   words `context deadline exceeded`, and no failing-input, panic or hung-worker line;
   the `fuzz-stall` canary shows a target that stalls on every mutated input still
   fails. Is one retry the right call, or should the signature be narrower? Is adding
   `internal/node` to the lint roots complete (does the lint now really cover every file
   the simulator executes in the node, and nothing in `internal/noderuntime`)?
4. The two package moves in (a) and (c): pure moves, no behaviour change, no test lost
   or weakened (compare test names before and after); corpus directories moved with
   their fuzz targets.
5. Known cost, already accepted for this chunk: the node package's 500-seed sweep takes
   about 22–28 minutes (the builder attributes half to the OCC store re-encoding the
   whole document on every merge). Do not fail the review on run time alone; do say
   whether any test was thinned to get there.
A full gate run on this tree is in progress while you review; its result will be in
`agent_work/06-integration/gates/result.json` when it ends. The machine is shared: run
package-level tests, not the whole module at 500 seeds.
