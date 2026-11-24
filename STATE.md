# STATE — object-store

RENAMED 2026-11-23 (Nick): the project is "Object Store". Repository nickstrad/object-store
(was nickstrad/decentralized-object-store; GitHub redirects the old repository URL), Go module
github.com/nickstrad/object-store, checkout /root/Software/object-store, site
https://nickstrad.github.io/object-store/ (the old Pages URL no longer serves). History was
not rewritten; records under agent_work/, PLAN.md and docs/project-source.md keep the old name.

Goal: build Nick's object store, specified as "Decentralized Object Store & Local-First Sync
Engine" (spec: docs/project-source.md) — a leaderless, multi-region object store in
Go (SWIM gossip, Reed-Solomon erasure coding, Merkle-tree anti-entropy,
optimistic concurrency control for metadata with last-writer-wins resolution, Sagas),
designed testing-first.
Plan: [PLAN.md](PLAN.md) — signed off by Nick 2026-11-21; his rulings are in
PLAN.md section 9.1 and override the spec where they differ (notably: OCC
replaces the CRDT layer; SIBLINGS ARE OUT — concurrent versions settle by
last-writer-wins on the hybrid logical clock, every discard counted and logged
(LWW ruling, 2026-11-21 16:14 CT, /root/briefs/objstore-lww-ruling-2026-11-21.md,
folded into PLAN 9.1, 7.5 and Chunk 4); Docker is the one environment; the book is the
learning artifact; C4 diagrams are kept current).

Status: PHASE 2 (BUILD) COMPLETE, 2026-11-23, and the CLEANUP PHASE (rename to Object Store,
prose cleanup) COMPLETE, 2026-11-24 — see "Cleanup phase" below. Every chunk is landed on main:
Step 0, 0a, 0b, the five protocol chunks (1–5), Chunk 6 (integration, f64aa11), Chunk 7
(end-to-end suite and hardening, d618607) and Chunk 8 (publishing finish, fbb0e2a). The book is
live at the new URL. Nothing is in flight; done markers: /root/runs/objstore-phase2-done.txt and
/root/runs/objstore-cleanup-done.txt.

Seats (this project only): Fable orchestrates, gates and lands; Opus writes
product Go (logic only, no comments), fresh agent and worktree per chunk, at
most 3 at once; Gemini 4 Neon reviews Opus and writes all reader-facing prose
once, late, per chunk (book chapter, comments, README, site copy, CLI
help); Astra (gpt-6-astra) writes independent test harnesses per chunk and
arbitrates an Opus/Neon standoff. Mission: /root/briefs/objstore-phase2-mission.md.
Chunk 6 only (Nick, 2026-11-22, two rulings): Fable is the builder of the arbitration fix,
and that fix lands only after two independent passes — Astra's confirm and a Neon
second-eyes review-plus-probing pass (see the Chunk 6 row for the landing rule).
Chunk 7's finish only (Nick, 2026-11-22): Neon is the builder, Fable the reviewer, Opus the
adversarial seat; Astra has no Chunk 7 role. Chunk 8 returns to the seats above.

## Chunk board

| Chunk | What it is | State |
|---|---|---|
| Step 0 | Sign-off rulings applied to PLAN.md | LANDED |
| 0a | Foundation: go.mod, Docker environment, gate scripts and canaries, detlint, orchestration scripts, vendored kb, book site on Pages, CI | LANDED 8205edf + c5117c5 |
| 0b | Deterministic simulator (internal/env, internal/sim, simtest, cmd/objsim) | LANDED ceb5438, tag `freeze-0b` (API frozen); chapter 1 |
| 1 | SWIM gossip membership (internal/gossip) | LANDED 3074279; chapter 2; review FAIL → repair → NITS closed; harness 72 held, 1 pins removed behaviour |
| 2 | Reed-Solomon erasure coding and chunk store (internal/erasure, internal/storage) | LANDED c39eb79; chapter 3; review FAIL → repair → NITS closed; harness 41 held, 1 narrowed |
| 3 | Merkle anti-entropy (internal/merkle) | LANDED 2a86f4c; chapter 4; review NITS → repair → PASS; harness 72 held, 2 restated (cost bounds; PLAN 2.2/7.4 updated) |
| 4 | Optimistic concurrency control for metadata, last-writer-wins (internal/occ) | LANDED 9acc7ea; chapter 5; review FAIL → LWW rework → FAIL → round 2 → Astra arbitration NITS closed; harness 60 held, 1 expects exactly one record per loss |
| 5 | Saga engine and bucket migration (internal/saga) | LANDED 4892ab9; chapter 6; review FAIL → repair → NITS closed; harness 125 held |
| 6 | Integration: node (`internal/node`), real runtime (`internal/noderuntime`), HTTP object API (`internal/storage/httpapi`), cmd/objnode | LANDED f64aa11; chapter 7; REVIEW-1 FAIL → repair → REVIEW-2 FAIL → fix → ARBITRATION-1 FAIL (one major) → fix finished by Fable (owner ruling) → ARBITRATION-2 NITS closed → owner-ordered Neon second-eyes pass FAIL (four majors, two nits) → fixed by Fable → focused Neon confirm NITS closed (its one nit was fixed AFTER the confirm; covered by tests and the final gate only); harness 662 held, 0 broken; final gate G0–G7 green at 500 seeds. Records: agent_work/06-integration/ (build-notes sections 14 and 15, NEON-SECOND-EYES.md, NEON-CONFIRM.md) |
| 7 | E2E suite, nemesis cycles, fuzz hardening (`test/e2e`, tier S on the simulator and tier R with real `objnode` processes; `internal/nodetest`) | LANDED d618607; chapter 8 live. Opus build → Neon REVIEW-1 FAIL (three coverage holes in the suite) → owner seat swap for the finish (Neon builds, Fable reviews, Opus adversarial) → Fable review rounds 1 FAIL, 2 PASS, 3 NITS, 4 PASS (FABLE-REVIEW.md; landing verdict REVIEW-2.md NITS CLOSED). Two product defects found and fixed with tests (a read short of k shards answered 404 instead of 503, f70e2c2; a migration behind a long-frozen coordinator lost acknowledged objects, eaa0556). Adversarial (agent_work/adversarial/07-e2e-opus): 80 held, 1 broken with no claim behind it. Final gate on d35dcfe: G0–G7 and G5R all green (race 54 min, 500-seed sweep 73 min, base 232399873226549). OPEN, not blocking: the real-process step failed only when run straight after the hour-long sweep, cause not established; the gate runs it first (build-notes sections 16–17). |
| 8 | Publishing finish: the assembled book (assembly and polish only) | LANDED fbb0e2a (2026-11-23), published AHEAD of its final gate on the owner's ruling (/root/briefs/objstore-publish-book-now-ruling-2026-11-23.md; land.sh bypassed, merged by hand); the gate then finished ALL GREEN on b72277f (G0–G7 and G5R: 13 of 13 real-process tests, unit, 2,000-check properties, 15 fuzz targets, race 50 min, 500-seed sweep 69 min from base 201358669618850, Docker, cluster; agent_work/08-book/gates/result.json). Live and verified: front page presents the book; book index lists preface, topic map and chapters 1–8; all 24 site URLs HTTP 200. Content: eight chapters edited into one shape (two Neon editors); preface, topic map, final README, site front page (Neon); final C4 pass; README command check; Astra's independent check (36 commands pass; 3 README findings and 13 of 381 claims not supported, all closed in one Neon round; agent_work/adversarial/08-book/RESULTS.md); acceptance check REVIEW-1.md PASS. NOT CLEAN: `prose-check.sh 98c6edc 1639665` exits 1 with four findings of one kind — see Open items. |

## Cleanup phase (2026-11-23/24) — DONE

Mission: /root/briefs/objstore-cleanup-mission-2026-11-23.md. Records: agent_work/09-cleanup/
(briefs, PROSE-REPORT.md, REVIEW-1.md NITS CLOSED, check-prose.py, sweep.sh);
docs/knowledge/rename-to-object-store.md. Commits on main, e50e41b..637c162:

- eea974e — book manifest title, C4 model title and system name, diagram 1: "Object Store".
- 6243645 — site builder: repository URL; THE PLAN PAGE IS NO LONGER PUBLISHED (plan.html and
  its navigation entry removed; PLAN.md stays in the repository as the build's record).
- 4e17bb5 — module path in go.mod, every import, the nested check modules, scripts. No logic
  change (the one non-import edit in product Go is the module-path string in
  internal/testutil/leakcheck).
- c7e5ab5 — C4 model strings: build-process notes removed, source entry points at the book.
- eb5e723 — this phase's records, scripts, INDEX rows, knowledge entry.
- 637c162 — the prose commit (Neon): README, front page, preface, chapters 1 and 5–8, C4
  pages, three Go comments. Verbiage only.

Verified: prose provenance clean over the range; fast lane green locally and in CI; README
command check on a clean clone; site lint clean (22 pages); sweep part 1 empty; live site at
the new URL, all pages HTTP 200. No full gate was run in this phase (by the mission).
The GitHub description is Neon's draft (agent_work/09-cleanup/repo-description.txt).

## Observability phase — PLANNED (2026-11-24)

Goal: framework-free component snapshots and config-selected OTel, Prometheus, CSV and
DuckDB adapters. Implementation spec: [agent_work/10-observability/PLAN.md](agent_work/10-observability/PLAN.md).
Planning baseline: main `379e622`; plan and this status are committed together on
`feat/observability` (resolve the plan checkpoint with
`git log -1 --format=%H feat/observability -- agent_work/10-observability/PLAN.md`).

Done: architecture, exact ports, metric catalogue, toggle/cost model, adapter contracts,
Docker Collector-to-Prometheus path, dependency rulings, chunk acceptance and seat split.
No product code was changed or product tests run for this planning-only checkpoint.
Status: PLANNED; implementation not started; no background build/review run is alive.

Next, in order: conductor briefs A0/A1 for Astra (core/selector/OTel first); Opus builds
O1/O2 (component/runtime/objnode wiring); Astra completes A2 (real objnode metric queried
in Prometheus); Opus builds O3/O4 (Prometheus, CSV, DuckDB); Neon performs N1 cleanup,
docs and final polish. See the plan for dependencies, acceptance and independent review.
Open implementation choices: Astra pins and validates Collector/Prometheus image digests
in A1; Opus pins the DuckDB CLI/checksum and measures its cost in O4. No planning blocker.
Publication/merge remains a Nick/conductor gate, not authorized by the planning task.

Resume: main ref remains at `379e622`; the primary checkout is switched to
`feat/observability` after completion. The temporary
planning worktree `/root/Software/object-store-work/10-observability` is removed after the
planning commit and done marker are verified; create a fresh worktree from the branch for
implementation. Marker: `/root/runs/objstore-obs-plan-done.txt` (branch, exact plan SHA,
five key rulings). There are no detached processes to relaunch. Verify branch/git before
starting A0; do not rerun a build inferred only from the old Phase 2 resume section.

## In progress

Observability is planned; no implementation run is active.

## Per-chunk pipeline (as run for chunks 1–5; worked examples in each agent_work/<slug>/)

1. Build exits 0 → commit `agent_work/<slug>/gates/`, rebase on main. The builder's own
   build-stage gate (`counts_for_landing=true`) is accepted; the final gate is re-run.
2. In parallel: Neon review (`mkstage.py <slug> review base=main n=1`) and Astra harness
   (`mkstage.py <slug> adversarial`, THEN `agent_work/orch/soften.sh <brief>` — Astra's
   provider filter cuts off briefs that speak of attacks; softened briefs run normally).
   Commit the harness when Astra stops.
3. One Opus repair round takes the review findings and the broken checks together
   (brief written by hand; examples `objstore-p2-*-repair1.md`). Then: commit gates, rebase,
   orchestrator re-runs `agent_work/adversarial/<slug>/run-all.sh`, writes RESULTS.md (the
   harness rewrites its evidence files — commit them with RESULTS.md BEFORE any rebase),
   Neon re-review (`mkstage.py <slug> rereview base=<sha of the review-1 record commit> n=2`;
   check the generated brief names a real base). NITS → small Opus nit-fix brief → append a
   `NITS CLOSED` line. Second FAIL → Astra arbitration (ARBITRATION-1.md; land.sh takes it
   as the verdict; example agent_work/04-occ/objstore-p2-04-occ-arbitration.md).
4. Prose (Neon, no commits; orchestrator commits by path with trailer `Prose-By: Gemini 4
   Neon`): chapter and comments in parallel once code is final (comments only after the
   last code change); then, after rebasing on current main, status prose (README +
   docs/index.md) while an orchestrator subagent writes the landing docs as four commits
   staged by path: C4 refresh CHECKED AGAINST THE CODE, docs/knowledge entry, INDEX row,
   manifest entry (worked example: the four commits before each landing merge).
5. `GATE_STAGE=final agent_work/gates/gate.sh <slug>` and `agent_work/gates/prose-check.sh
   main HEAD` on the final rebased tree; commit result.json; write the tour message;
   `agent_work/orch/land.sh <slug> chunk/<slug> <message-file>`; verify the chapter URL
   returns 200; remove the worktree (KEEP the branch); STATE; progress line.
- LESSON: do not commit to main between a chunk's final gate and its landing.
- LESSON: a 30 s fuzz run of `FuzzSimSchedule` timed out once in a final gate under
  machine load (`context deadline exceeded`, no failing input); the rerun on the same
  commit passed. Record such a rerun in the result commit message; investigate if it repeats.
- Helpers: `agent_work/orch/launch.sh <run> <opus|neon|astra> <worktree> <ABSOLUTE brief>`,
  `waitany.sh <secs> <run>...`, `mkstage.py` (run inside the worktree), `soften.sh`.

## Open items

- From the cleanup phase, for Nick to accept or direct otherwise (none blocks anything):
  (1) the plan page was taken off the site (the mission did not name it; it was the one
  published page wholly about how the software was built — restoring it is a revert of
  6243645's three builder lines); (2) chapter 7 is still titled "The Object Store and Sync
  Engine" with slug `object-store-and-sync-engine` (retitling changes a published URL);
  (3) the chapters' "Adversarial testing" bullets still tell what independent test harnesses
  found run by run — they name no agent or seat, and rewording them would touch numbers;
  (4) test file names and identifiers with process-era words (`secondeyes_*_test.go`,
  `TestArbitration…`, `reviewerShape`, `review2_test.go`) are code and were not renamed;
  (5) the repository's GitHub "website" field is empty.

- Prose provenance check on Chunk 8 is RED on a bookkeeping point (owner: Nick, to accept
  or direct otherwise): the three prose commits 9b10463, b9fbcdf and b72277f also carry the
  prose seat's own work reports under agent_work/08-book/, which the rule does not allow in
  a prose commit. No finding touches a reader-facing file or Go. The check ran only after
  the publish, the history is pushed, and it has not been rewritten.
- ANSWERED (Nick, 2026-11-23, /root/briefs/objstore-durability-question-closed-2026-11-23.md):
  a metadata write acknowledged by a node cut off from the cluster is lost without a record
  if that node's disk is wiped before the write replicates. Ruling: "Don't worry about that
  edge case." It is an accepted, stated limitation of the design; no fix, no further work.

- Neon seat: UP again on the direct route (probed LIVE 2026-11-23 ~15:16Z at Nick's request;
  the monthly spending cap that took it down at ~11:20Z no longer blocks it). OpenRouter
  fallback is still out of credits until 2026-12-01. No seat change was made and none is
  pending; Neon keeps its roles (Chunk 7: builder of the two missing tests, then prose;
  Chunk 8: normal seats). Probe again before each dispatch: `agent_work/orch/seat-probe.sh neon`.

- CI — OWNER RULING (Nick, 2026-11-22, /root/briefs/objstore-ci-fast-ruling-2026-11-22.md):
  push-triggered CI runs the FAST LANE ONLY. ci.yml runs `agent_work/gates/ci-fast.sh`
  (gates G0, G1, G6 in the dev container: build, static checks, unit tests with coverage
  floors, pinned seeds plus the default three-seed sweep) and prose provenance; about 1.5
  minutes locally on main. A red fast lane is a real blocker. Everything long is on demand
  and NOT deleted (commands in the ci-fast.sh header): the full gate, fuzz budgets, seed
  sweeps, and the gate canaries (canaries.yml is now started by hand only; no push, PR or
  weekly trigger). The full local gate on vm-1 is still the landing authority, unchanged.
  The Chunk 7 branch adds a short mode (`GATE_SHORT=1`, `go test -short`) so the
  end-to-end suite does not push the fast lane past a few minutes; it arrives with the
  landing. Check the landing push's CI time.
  Known and left alone: the full gate cannot finish on a GitHub runner (its host-side
  cluster check cannot write into the container-owned result directory — the cause of the
  last red CI runs on main); it runs on vm-1 as root.
- Book chapters are accurate but dense (close to compressed specifications). Style
  verdict is Nick's; Chunk 8 is the consistency edit.
- OCC's LWW round 2 was run as the owner-directed amendment round (ruling's process
  note) before arbitration rather than after; recorded in the progress log.
- c4-sdk neighbour elements — owner: Nick, not blocking (docs/c4/README.md, "Model notes").
- CUTOFF: the Claude sign-in on this VM ends between 2026-11-25 and 2026-11-28. Rule
  (OQ-14): stop at the last landed chunk, leave any in-flight chunk unmerged in its
  worktree unless it passes every gate cleanly, make this file an exact resume document,
  write the done marker with STOPPED-AT-CUTOFF. Chunks do not move to other seats.

## Facts a resuming agent needs

- Rulings in force: OCC replaces CRDTs; SIBLINGS ARE OUT — last-writer-wins with every
  discard counted and logged (/root/briefs/objstore-lww-ruling-2026-11-21.md, PLAN 9.1);
  every chunk's chapter goes live at its landing, Chunk 8 is assembly only
  (/root/briefs/objstore-publish-ruling-2026-11-21.md); simulator kernel extraction is
  later, not now (docs/knowledge/sim-kernel-extraction-note.md).
- Wire-code blocks: gossip 80–95, storage 96–111, merkle 112–127, occ 128–143 (131
  unassigned), saga 144–159, node and object API 160–191.
- Briefs: /root/briefs/objstore-p2-<topic>.md. Run logs, pid and exit files:
  /root/runs/objstore-p2-*. Progress log: /root/runs/objstore-phase2-progress.txt.
  Done marker (only at completion or cutoff): /root/runs/objstore-phase2-done.txt.
- Worktrees: /root/Software/object-store-work/NN-<slug>, branch
  chunk/NN-<slug>. Live: none. Branches of landed chunks are kept.
- Gate stages: `agent_work/gates/gate.sh <chunk>` (build stage, on a chunk branch, before
  prose) and `GATE_STAGE=final ...` (after prose, and the only form that runs on main).
- Site: https://nickstrad.github.io/object-store/ — built by
  .github/scripts/build_site.py through .github/workflows/pages.yml on every push to
  main; the book index lists the preface, the topic map and chapters 1–8. Pages: front page,
  book index, 10 book pages, C4 index and 9 diagram pages, model.json (no plan page).
- C4 set: docs/c4/ (c4-sdk bundle model.json, nine diagram files; 07 is
  07-component-occ.md). Every protocol package is drawn as built; redrawn as built at the Chunk 6 landing (a node core implements every package's
  interfaces; no protocol package calls another).
- Neon routes: direct (google/gemini-4-neon via opencode) is primary;
  OpenRouter fallback is out of credits until 2026-12-01. Probe with
  agent_work/orch/seat-probe.sh before each chunk's first Neon stage. Neon (opencode) cannot
  read files outside its worktree: copy each brief into agent_work/<chunk>/ and launch with
  that path. Run
  opencode and codex headless with stdin closed (`</dev/null`).

## Resume

Phase 2 and cleanup are complete; observability is PLANNED (see its section above). No run
is alive. For
follow-up work read this file, PLAN.md and /root/briefs/objstore-phase2-mission.md; start a new
branch and worktree from main (worktrees under /root/Software/object-store-work/).
`agent_work/orch/launch.sh` names run files `objstore-p2-<run>` unless RUN_PREFIX is set. The full gate on main is `GATE_STAGE=final agent_work/gates/gate.sh <name>`.
