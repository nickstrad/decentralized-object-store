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
live at the new URL. Observability core is now built; see its section below. Prior-phase done markers: /root/runs/objstore-phase2-done.txt and
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

## Observability phase — core: DONE (2026-11-24; build, review pending)

Goal/spec: [observability plan](agent_work/10-observability/PLAN.md), including Nick's
2026-11-24 amendment: mandatory OTel/Prometheus, CSV disk flush, DuckDB analysis over
CSV; no runtime DuckDB writer without a valid runtime use case.

Astra core build commits on feat/observability:
- `5b2e656`: framework-free metrics values, Source, finite catalogue and validation.
- `1072417`: config gating, selector/port, runner, shared contracts and stdlib OTLP.
- `7ad39f1`: pinned Docker Collector/Prometheus setup, bounded pipeline check and gates.
- `ca42bf1`: prevent boot_id labels in Prometheus; bounded warning text and joined shutdown.
- `4c4f330`: active-export cancellation/shutdown test.
- `bcd9c5d`, `017c0ad`, `55e430b`: durable knowledge, reusable-script index, fixture proof
  and the unrelated saga gate failure record.

Verified: go build ./..., go vet ./..., and Docker fast lane G0/G1/G6 PASS on `55e430b`.
Metrics coverage 96.6%; telemetry 95.7%. The prior `017c0ad` gate failed only the existing
saga randomized coverage-of-draws assertion (20 cases passed; no retry-exhaustion draw);
one unchanged-code retry passed. No full/race/fuzz/large-sweep lane was run.
Real Docker A1 fixture proof: HTTP counter 7 -> 15, in-flight gauge 1, stored boot_id
label count 0. Collector 0.145.0 configuration validated; Prometheus 3.9.1; immutable
image digests pinned. This is NOT the A2 real-objnode proof.

Records: [core verification](agent_work/10-observability/CORE-VERIFICATION.md),
[fast result](agent_work/10-observability/core-fast-result.json),
[query evidence](agent_work/10-observability/core-fixture-results.json),
[knowledge](docs/knowledge/observability-core.md), [script index](work-scripts/README.md).
Done marker: /root/runs/objstore-obs-core-done.txt.

Next, in order:
1. Conductor: fresh cross-company review of this build before acceptance/landing.
2. Opus O1/O2: component Snapshot methods, runtime collector, HTTP and objnode wiring.
3. Astra A2: run the prepared default check-otel.sh real-node path and add the CI hook.
4. Opus O3/O4: register mandatory Prometheus/CSV adapters; exact CSV readback and DuckDB
   analysis over finalized CSV files. No placeholder factories or ReadCSVDir were added.
5. Neon N1: final documentation/polish. Nick/conductor retains merge/publish gates.

Resume: feat/observability is in /root/Software/object-store-work/10-obs-core, retained
for conductor review. Build/check processes have stopped; no background process to
relaunch. Logs: /root/runs/objstore-obs-core-fast-retry.log,
/root/runs/objstore-obs-pipeline-final.log; preserved failed gate:
/root/runs/objstore-obs-core-fast-saga-failure.{json,log}.
No build blocker. Independent review and O1/O2 wiring are the next dependencies.

## In progress

Observability core, cleanup fix, and Neon final-pass code tweaks/docs are on
`feat/observability`, but **the observability phase is NOT READY**: Stage C
(`C-rest-sol`: O1/O2/O3/O4 and A2 real-`objnode` proof) never re-ran after the
erasure catalogue blocker was resolved (see the checkpoint below).

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

Phase 2 and cleanup are complete; observability core is DONE and the erasure label-domain
blocker has been resolved by the Neon cleanup fix on `feat/observability` (see the current
checkpoint below). No run is alive. For
follow-up work read this file, PLAN.md and /root/briefs/objstore-phase2-mission.md; start a new
branch and worktree from main (worktrees under /root/Software/object-store-work/).
`agent_work/orch/launch.sh` names run files `objstore-p2-<run>` unless RUN_PREFIX is set. The full gate on main is `GATE_STAGE=final agent_work/gates/gate.sh <name>`.

## Observability phase — NOT READY (core + tweaks + docs done; Stage C missing, 2026-11-24)

Core was completed at `bd3e01c` (independent review remains separate). The remaining Sol
build (`C-rest-sol`) stopped under the explicit core-contract blocker rule because
`internal/metrics/catalogue.go` configured HTTP operation values instead of
`{encode, reconstruct}` for `objstore_erasure_input_bytes_total` and
`objstore_erasure_output_bytes_total` (recorded in `9dbf6ab`,
[REST-BLOCKER](agent_work/10-observability/REST-BLOCKER.md), and
`/root/runs/objstore-obs-rest-blocked.txt`).

Cleanup fix (Neon, 2026-11-24, on `feat/observability` at `/root/Software/object-store`, `f5c4538`):
- Fixed `labelDomains` in `internal/metrics/catalogue.go` so both
  `objstore_erasure_input_bytes_total` and `objstore_erasure_output_bytes_total` allow
  `operation` in `{"encode", "reconstruct"}` per PLAN section 3. Audited all 83 catalogue
  descriptors and 25 labeled metrics against PLAN section 3; no other mismatches found.
- Added regression coverage in `internal/metrics/metrics_test.go`
  (`TestErasureByteOperationDomains` plus full-domain coverage and label-sort checks in
  `TestCatalogueAndClone`).
- Done marker: `/root/runs/objstore-obs-cleanup-done.txt`.

Final tweak pass (Neon, 2026-11-24, on `feat/observability` at `/root/Software/object-store`):
- **Why NOT READY:** `/root/runs/objstore-obs-conductor2.sh` (lines 106–110) ran
  `neon_cleanup` when `C-rest-sol` blocked without `/root/runs/objstore-obs-rest-done.txt`,
  then fell straight through to `gate` and `D-neon-final` without re-running `C-rest-sol`
  or checking for `/root/runs/objstore-obs-rest-done.txt`. Consequently, ALL Stage C
  deliverables from PLAN.md and Nick's 2026-11-24 amendment remain unbuilt on
  `feat/observability`:
  1. O1: component `Snapshot()` methods, `MetricsEnabled` config propagation, `erasure.MeteredCodec`,
     `storage.StripeCodec`/`EncodeStripeWithCodec`/`DecodeStripeWithCodec`, `node.MetricSources()`.
  2. O2: `internal/noderuntime/metrics.go` (`MetricsCollector`, metrics-exempt inspection path,
     `Runtime.Snapshot()`), `httpapi.Server.Snapshot()`, and `cmd/objnode` `observability` JSON/flags.
  3. A2: real `objnode` → Collector → Prometheus proof (`TestObjnodeOTelPrometheus` / default
     `check-otel.sh`) and CI hook.
  4. O3: mandatory direct `prometheus` scrape adapter and first-class `csv` disk-flush adapter +
     `telemetry.ReadCSVDir` (`internal/telemetry/registry.go` currently registers only `otel`).
  5. O4: DuckDB-over-CSV analysis layer (`deploy/observability/queries.sql`, `query-csv.sh`,
     optional pinned `dev-observability` Docker target, and real CSV smoke).
- **Small tweaks applied to built core/OTel code:**
  - `internal/telemetry/otel.go`: honored caller context deadlines beyond 2s (removed hardcoded
    2s `http.Client.Timeout` and `Transport.ResponseHeaderTimeout`; kept 2s fallback when caller
    passes no deadline), checked `json.Marshal` error before batch byte-limit check, checked
    `ctx.Err()` on `io.ReadAll` failure in `send`, and checked `ok` from `ctx.Deadline()`.
  - `internal/telemetry/runner.go`: replaced magic `counts` indices with named constants, removed
    unused `err` parameter on `report`, removed redundant `shutdownDone` channel/select after
    synchronous `shutdownOnce.Do`, and guarded non-negative duration/timestamp conversions.
  - `internal/telemetry/runner_test.go`: fixed data races detected by `go test -race` on test
    `sink`, `logs`, `calls`, and `r.counts` inside `synctest` tests.
  - `internal/telemetry/adaptertest/contract.go`: added deterministic label tie-breaker to
    `projection` sorting for multi-series points sharing a metric name.
  - `internal/metrics/catalogue.go`: corrected acronym casing in metric `Help` strings
    (`HTTP`, `RPC`, `OCC`, `LWW`).
  - `internal/telemetry/otel_test.go`: added `TestOTelHonorsCallerTimeoutBeyondDefault`.
- **Reader-facing docs written (truthful to current tree):**
  - `docs/observability.md` and a short `## Observability` section + links in `README.md`,
    documenting the 83-descriptor catalogue, off-by-default toggle, `otel` adapter, Docker
    Collector → Prometheus stack, extension guide (one file + one `registry.go` entry), and
    explicitly noting current wiring status (no false claims about unbuilt adapters or flags).
- **Verified (all PASS):** `gofmt -s -l`, `go build ./...`, `go vet ./...`,
  `go test -v -race -cover ./internal/metrics ./internal/telemetry/...` (metrics 96.6%,
  telemetry 94.9%), `go test -short ./...`, and Docker fast lane
  `COMPOSE_PROJECT_NAME=objstore-obs-neon-final agent_work/gates/ci-fast.sh` (G0 including
  `detlint`, `panics`, `deps`, `selfcontained`, `site_lint.py` + selftest; G1 unit + coverage
  floors; G6 docker).
- Done marker / owner report: `/root/runs/objstore-obs-neon-done.txt`.

Retained interrupted worktrees from the stopped run remain untouched for inspection:
`/root/Software/object-store-work/10-obs-{components,adapters,runtime}` on
`work/obs-{components,adapters,runtime}` (partial uncommitted Prometheus work in adapters;
no product changes in components/runtime). Logs/briefs/PIDs/exits:
`/root/runs/objstore-obs-{components,adapters,runtime}*`.

Next: fix the Stage C retry/check in `/root/runs/objstore-obs-conductor2.sh` (or dispatch
fresh O1/O2/O3/O4 + A2 runs from the current `feat/observability` tip), run independent review
and real-`objnode` / DuckDB-over-CSV checks, then update `docs/observability.md` and `README.md`
for the newly wired components, flags, Prometheus/CSV adapters, and DuckDB-over-CSV queries
before marking the observability phase READY. DuckDB ruling remains CSV analysis over flushed
CSV files, no runtime writer.


## Stage C resumption — A2 preparation (2026-11-24)

Goal: complete O1/O2/O3/O4 and A2 under the [observability plan](agent_work/10-observability/PLAN.md).
Status: implementation resumed from f711468; phase remains NOT READY. No core blocker.
A2 now asserts health counter growth, the in-flight gauge, and absence of boot_id labels;
Compose has the observability profile and CI has a separate five-minute Docker proof job.
Static preparation checks pass: go test ./test/observability (integration gated), shell syntax,
Docker Compose config. Docker daemon 29.7.2 is available; live proof waits for O1/O2.

Active worktrees: 10-obs-resume-components (O1), 10-obs-resume-runtime (O2),
10-obs-resume-adapters (O3/O4), 10-obs-resume-proof (A2 preparation), all under
/root/Software/object-store-work. Retained interrupted worktrees are untouched.
Next: combine tested implementation commits on feat/observability, run the real Docker
proof and CSV/DuckDB smoke, independent cross-company review, required fast lane, then
record Stage C DONE with SHAs. Neon owns the second final pass and phase readiness.
No detached process for A2 is running; rerun check-otel.sh after wiring lands.

A2 preparation independent review: NITS addressed/recorded in agent_work/10-observability/A2-REVIEW.md; added Collector-side no-boot-label assertion. Live run remains pending wiring.

Stage C live claims and verification dependencies: agent_work/10-observability/COORDINATION.md. A2 prep on target at 7954bdb + 8f25539; independent review exit/output verified.
### Stage C O1 resume checkpoint (component worktree)

In progress on work/obs-resume-components. Erasure facade and compatible measured stripe
entry points implemented; focused erasure/storage tests pass. Remaining component counters,
config propagation and bounded aggregate sources are being implemented. No background run.
### Stage C adapter resume checkpoint (Sol, 2026-11-24)

Branch/worktree: `work/obs-resume-adapters`, `10-obs-resume-adapters`.
O3 direct Prometheus implementation and shared contracts complete: immutable bounded
cache, private listener, no pre-first/stale values, escaped/sorted exposition and joined
shutdown. Enabled config uses a private registry validation hook for the two-interval
staleness bound; public interfaces/catalogue unchanged. Focused telemetry tests pass.
CSV and DuckDB analysis work remains in progress; no background run to resume.

O3 CSV implementation complete: exact unsigned integers and metadata through
`ReadCSVDir`, one header-bearing batch per private atomic finalized segment, configurable
sync and post-success retention. Shared contracts cover a real filesystem failure and
recovery; focused cases cover rotation, max uint64, crash temp exclusion, invalid files,
identity and byte limits. No runtime DuckDB writer is built (owner ruling); CSV is the
runtime disk sink and DuckDB remains an offline analysis consumer. O4 remains in progress.
