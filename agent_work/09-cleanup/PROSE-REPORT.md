# Prose Cleanup Report

## 1. Per-file summary of changes

### Top-level & site front pages
- **`README.md`**: Rebranded H1 to `# Object Store` and updated the GitHub Pages link to `nickstrad.github.io/object-store`. Removed "learning project" framing, replaced landing-gate terminology with check-suite terminology (`enforced by a check`, `check suite`, `full check suite`, `check tools`), removed "as-built" / "Finished" build-progress wording, and removed the bullets linking to `PLAN.md` and `docs/project-source.md`. All fenced `sh` blocks and commands left byte for byte.
- **`docs/index.md`**: Rebranded H1 to `# Object Store`, removed "Built testing-first as a learning project," framing, removed "as-built" and "build status" wording, and removed the bullet linking to `../PLAN.md`.

### Book front matter and chapters
- **`book/front/preface.md`**: Removed "It is a learning project built testing-first:" framing, replaced "study companion" / "teach each distributed-systems topic" with "explains each distributed-systems mechanism in plain terms and shows where it lives in the repository", and replaced "for deeper study" with "for further reading".
- **`book/front/topic-map.md`**: Read end to end; no edits needed.
- **`book/chapters/01-deterministic-simulation.md`**: Replaced `gated run` with `check run`.
- **`book/chapters/02-swim-gossip.md`**: Read end to end; no edits needed.
- **`book/chapters/03-reed-solomon-erasure-coding.md`**: Read end to end; no edits needed.
- **`book/chapters/04-merkle-anti-entropy.md`**: Read end to end; no edits needed.
- **`book/chapters/05-optimistic-concurrency-control.md`**: Replaced `gated 239 s` with `239 s in the check suite`.
- **`book/chapters/06-sagas.md`**: Replaced `gated 500-seed sweep` with `check-suite 500-seed sweep`.
- **`book/chapters/07-object-store-and-sync-engine.md`**: Replaced `30 s gates` with `30 s checks`. Left chapter title, filename, and slug unchanged per brief.
- **`book/chapters/08-end-to-end-and-hardening.md`**: Replaced `full gates` with `the full check suite` (twice) and `Fuzz, sweeps, and gates.` with `Fuzz, sweeps, and checks.`.

### C4 documentation (`docs/c4/*.md`)
All Mermaid blocks, `<!-- draw-visual: … -->` markers, fenced `text` renderings, element IDs, table rows, and relationship numbers were left byte for byte.
- **`docs/c4/README.md`**: Removed "As built", chunk/landing history, `PLAN.md` / `OQ-7` references, dated check header (`Checked on 2026-11-22`), Chunk 6 mermaid re-parse note, owner/buy-in/Chunk 0a "Open items on this set" section (moving the 80-column terminal-check note into `## Model notes`), and the C4 ruling / "c4-ask" chat history under `## Tool: c4-sdk`. Rebranded diagram 1 summary to "Object Store".
- **`docs/c4/01-system-context.md`**: Removed "As built" / Chunk 6 dated check paragraph, `Source: PLAN.md …`, and `(OQ-6)` from relationship 1's technology cell.
- **`docs/c4/02-container.md`**: Removed "As built" / Chunk 6 dated check paragraph and `Source: PLAN.md …`. Converted `Differences from the target design:` into present-tense `Notes:`, retaining all architectural facts.
- **`docs/c4/03-container-runtime.md`**: Removed "As built" / Chunk 6 dated check paragraph, "target design" contrast, and `Source: PLAN.md …`. Converted `Differences from the target design:` into present-tense `Notes:`.
- **`docs/c4/04-component-gossip.md`**: Removed "As built" / Chunk 1 & 6 dated checks, `Source: PLAN.md …`, `Added by the build` / `PLAN.md does not name it` tags in element descriptions, and renamed `As built` column header to `Description`. Converted `Differences from the target design:` into present-tense `Notes:`.
- **`docs/c4/05-component-erasure.md`**: Removed "As built" / Chunk 2 dated check line, `Source: PLAN.md …`, and renamed `As built` column header to `Description`. Converted `Differences from the target design:` into present-tense `Notes:`.
- **`docs/c4/06-component-merkle.md`**: Removed "As built" / Chunk 3 & 6 dated checks, `Source: PLAN.md …`, `added by the build; PLAN.md does not name it` tags, and renamed `As built` column header to `Description`. Converted `Differences from the target design:` into present-tense `Notes:`.
- **`docs/c4/07-component-occ.md`**: Removed "As built" / Chunk 4 & 6 dated checks, `Source: PLAN.md … and the LWW ruling in 9.1`, `PLAN.md names the guarantee, the build added the parts`, `Added in Chunk 6:`, and renamed `As built` column header to `Description`. Replaced the CRDT/sibling build-history bullets under `Differences from the target design:` with present-tense `Notes:` stating that metadata uses OCC with one version per key, no CRDT types, no siblings, and LWW with discard recording.
- **`docs/c4/08-component-saga.md`**: Removed "As built" / Chunk 5 & 6 dated checks, `Source: PLAN.md …`, `added by the build; PLAN.md does not name it`, and renamed `As built` column header to `Description`. Converted `Differences from the target design:` into present-tense `Notes:`.
- **`docs/c4/09-component-storage.md`**: Removed "As built" / Chunk 2 & 6 dated checks, `Source: PLAN.md …`, `Added by the build; PLAN.md does not name it`, and renamed `As built` column header to `Description`. Converted `Differences from the target design:` into present-tense `Notes:`.

### C4 model (`docs/c4/model.json`)
Edited only description, `plannedNotes`, legend description, and `sources[0]` title/notes string values; kept all keys, IDs, `name`, `status`, `url`, `technology`, and formatting intact:
- Removed leading `"As built, "` from `plannedNotes` on `c-gossip`, `c-merkle`, `c-occ`, and `c-saga`.
- Reworded `c-occ.plannedNotes[1]` from owner-ruling/CRDT-replacement history to a present-tense statement of OCC + LWW with discard recording (no CRDT types, no siblings).
- Removed process-only strings (`"Added by the build; PLAN.md does not name it."` / `"Retention followed by bounded tombstones was added by the build; PLAN.md names only the view and its events."`) from `plannedNotes` arrays of `g-view`, `g-wire`, `m-wire`, `m-membership`, `s-sim`, and `t-proto`.
- Removed `"Added in Chunk 6: "` from `o-node.description`.
- Reworded `t-paths.plannedNotes[0]` to drop `PLAN.md OQ-15` while keeping `k=4, m=2` and the `k+1` write threshold.
- Replaced `"A part of the package that PLAN.md names, or one the build added"` and `"A part of the package that PLAN.md names"` in all six component-diagram legends with `"A component inside the package"`.
- Reworded `sources[0].title` (`"Object Store architecture notes"`) and `sources[0].notes` (`"Architecture reference for the system, container, and component views."`), keeping `id` and `url` unchanged.

### Product Go comments & CLI help
- **`cmd/objsim/help.txt`**, **`cmd/objnode/help.txt`**: Read end to end; clean, no edits needed.
- **`test/e2e/e2e.go`**: Reworded package comment to drop `and metadata sync engine` (`// Package e2e hosts the end-to-end test suite for Object Store.`).
- **`test/e2e/harness_test.go`**: Changed `gate budgets` to `check-suite budgets` in the `seedsFor` comment.
- **`test/e2e/nemesis_test.go`**: Removed `local-first` from the `metadataElsewhere` comment (`// Because metadata writes acknowledge after a single local fsync,`).
- All other Go files under `cmd/`, `internal/`, and `test/`: audited; no other comments contained process talk, study framing, or the old project name.

### Output files created
- **`agent_work/09-cleanup/repo-description.txt`**: 1 line, 258 characters (plain text), describing Object Store accurately without CRDT or local-first sync engine claims.
- **`agent_work/09-cleanup/PROSE-REPORT.md`**: This report.

---

## 2. Every sentence deleted outright

| File | Deleted sentence | Where its technical content still stands |
| --- | --- | --- |
| `README.md` | `- **Plan:** [PLAN.md](PLAN.md) (build plan, testing strategy, and landing gates).` | No unique technical claim; link to `PLAN.md` removed per Rule 1. |
| `README.md` | `- **Specification:** [docs/project-source.md](docs/project-source.md) (original specification).` | No unique technical claim; link to `docs/project-source.md` removed per Rule 1. |
| `docs/index.md` | `- **[The Plan](../PLAN.md)** — testing strategy, building blocks, and landing gates.` | No unique technical claim; plan is no longer a site page (Rule 1). |
| `docs/c4/README.md` | `Every part of the system has landed, and every diagram was checked against the code when its part landed.` | Build-process history only; diagram inventory table directly below lists every part. |
| `docs/c4/README.md` | `Every element and relationship in the set is built.` | Redundant with `status: existing` in `## Format` and the inventory table. |
| `docs/c4/README.md` | `These diagrams started as the design that [PLAN.md](../../PLAN.md) defines.` | Pure build history / `PLAN.md` pointer. |
| `docs/c4/README.md` | `As each part landed, its diagrams were checked against the code and updated in the same landing.` | Pure build history. |
| `docs/c4/README.md` | `Where the build differs from the plan, the diagram shows the build and a "Differences from the target design" section says what changed.` | Pure build history. |
| `docs/c4/README.md` | `- Component diagrams contain the parts that PLAN.md names. A part that the build added is marked as such in the element table.` | Pure build history / `PLAN.md` reference. |
| `docs/c4/README.md` | `Checked on 2026-11-22:` | Dated check note; the validation table remains directly underneath. |
| `docs/c4/README.md` | `The nine Mermaid blocks were last run through mermaid.parse (Mermaid 11) at the previous refresh of this set. The blocks changed for Chunk 6 use the same statements as before and were not re-parsed.` | Chunk 6 refresh history; edge-by-edge Mermaid validation remains in the `## Validation` table. |
| `docs/c4/README.md` | `This is a workaround for a limit in the tool. Nick decides whether to keep it or to change c4-sdk.` | Owner decision note; the neighbour-element mechanism itself stays explained in the preceding sentences of note 1. |
| `docs/c4/README.md` | `1. **Buy-in.** The set waits for Nick's buy-in together with the plan sign-off.` | Pure sign-off / owner process note. |
| `docs/c4/README.md` | `2. **Publishing.** The Pages C4 section must load model.json with c4-sdk and render the Mermaid blocks; that belongs to the Pages scaffold in Chunk 0a.` | Pure Chunk 0a planning note (Pages publishing is already built). |
| `docs/c4/README.md` | `The C4 ruling of 2026-11-21 names c4-sdk as the tool for these diagrams.` | Owner ruling / process history; `c4-sdk` usage is stated in the following paragraph. |
| `docs/c4/README.md` | `Nick first called it "c4-ask" in chat and clarified on 2026-11-21 that he meant c4-sdk; there is no separate tool of that other name.` | Chat / process history. |
| `docs/c4/README.md` | `An earlier design run searched for the other name and recorded it as not found. That finding is closed.` | Pure agent-run history. |
| `docs/c4/01-system-context.md` | `Source: PLAN.md sections 1, 2.1, 2.7, 7.1.` | `PLAN.md` pointer removed per Rule 1. |
| `docs/c4/02-container.md` | `See "Differences from the target design" below.` | Target-design framing removed; `Notes:` remains at the bottom of the page. |
| `docs/c4/02-container.md` | `The eleven target relationships between the Object API or a protocol package and another protocol package are replaced by relationships 3 to 9 and 11 to 14.` | Target-design diff history; current relationships 3–9 and 11–14 are in the diagram and table. |
| `docs/c4/03-container-runtime.md` | `The Node runtime of the target design is two containers, the Node core and the Node runtime. Relationships 3 (implements the environment), 9 (peers) and 10 (disk) of the target design now start at the Node runtime (6 to 8 here); the wiring of the five protocol packages starts at the Node core (10 to 14).` | Target-design diff history; the two-package split is stated in the opening paragraph and relationships 6–8 and 10–14 are in the table. |
| `docs/c4/03-container-runtime.md` | `Relationships 3 and 4 are new: cmd/objnode starts the HTTP server, and the server calls the runtime through an interface. The target design drew the Object API only in the domain view.` | Target-design diff history; `cmd/objnode` starting the Object API and its `Service` call are stated in the opening paragraph and relationships 3–4. |
| `docs/c4/04-component-gossip.md` | `**As built.** The seven components PLAN.md names and the wire codec exist and match this diagram (checked against internal/gossip when Chunk 1 landed, 2026-11-22). Relationships 10 and 11 are as built since Chunk 6 (checked against internal/node, 2026-11-22).` | Dated chunk landing check notes; all eight components and relationships 10–11 are documented in the tables. |
| `docs/c4/04-component-gossip.md` | `Relationship 5 now shows what the indirect probe takes from the view.` | Target-design diff history; relationship 5 is in the table. |
| `docs/c4/04-component-gossip.md` | `Relationships 12 to 26 and the Wire codec are new.` | Target-design diff history; relationships 12–26 and `g-wire` are in the tables. |
| `docs/c4/05-component-erasure.md` | `**As built.** Checked against internal/erasure when Chunk 2 landed (2026-11-21).` | Pure dated landing check note. |
| `docs/c4/06-component-merkle.md` | `**As built.** The tree builder, the diff protocol, the anti-entropy worker, the Repairer interface, the wire codec and the Membership interface exist and match this diagram (checked against internal/merkle when Chunk 3 landed, 2026-11-22). Relationships 4, 5, 8, 11 and 12 are as built since Chunk 6 (checked against internal/node, 2026-11-22).` | Dated chunk landing check notes; all six components and relationships are in the tables. |
| `docs/c4/06-component-merkle.md` | `Relationships 11 and 12 are new.` | Target-design diff history; relationships 11–12 are in the table. |
| `docs/c4/07-component-occ.md` | `**As built.** The eleven components inside the package exist and match this diagram (checked against internal/occ when Chunk 4 landed, 2026-11-22). Relationships 5 and 20 to 22 are as built since Chunk 6 (checked against internal/node, 2026-11-22; relationship 20 again for the migration's shard check, 2026-11-23).` | Dated chunk landing check notes; all eleven components and relationships are in the tables. |
| `docs/c4/07-component-occ.md` | `Chunk 6 added one method inside the package, Node.Merge.` | Process framing; `Node.Merge` in `node_merge.go` is documented in the `o-node` table row and the `Code in …` list. |
| `docs/c4/07-component-occ.md` | `The Store, the Discard record, Version and record, Version vectors and the Membership interface are new components. The hybrid logical clock, the canonical encoding, the typed metadata document and the replicator (now the Replicating node) keep their places from the first design.` | Pure first-design comparison; all eleven components are listed in the element table. |
| `docs/c4/07-component-occ.md` | `Relationship 2 named Clock, Rand and Transport. As built, the node also uses Disk, for one state blob.` | Target-design diff history; relationship 2 in the table states `(Disk)` for `occ/state`. |
| `docs/c4/08-component-saga.md` | `**As built.** The orchestrator, the saga log, recovery, the bucket-migration workflow, the step interfaces and the model cluster exist and match this diagram (checked against internal/saga and internal/saga/sagasim when Chunk 5 landed, 2026-11-22). Relationships 1 and 9 to 11 are as built since Chunk 6 (checked against internal/node, 2026-11-22; relationship 9 again for the migration's shard check, 2026-11-23).` | Dated chunk landing check notes; all components and relationships are in the tables. |
| `docs/c4/08-component-saga.md` | `The Model cluster and relationships 13 to 17 are new.` | Target-design diff history; `s-sim` and relationships 13–17 are in the tables. |
| `docs/c4/09-component-storage.md` | `**As built.** The shard format, chunk store, striping, manifest, placement, shard request protocol and the node's put and get paths exist and match this diagram (checked against internal/storage when Chunk 2 landed, 2026-11-21). Relationships 1, 5, 11, 12, 16 and 17 are as built since Chunk 6 (checked against internal/node, 2026-11-22; relationship 12 again for the migration's shard check, 2026-11-23).` | Dated chunk landing check notes; all components and relationships are in the tables. |
| `docs/c4/09-component-storage.md` | `Relationships 16 and 17 are new.` | Target-design diff history; relationships 16–17 are in the table. |
| `docs/c4/model.json` | `"Retention followed by bounded tombstones was added by the build; PLAN.md names only the view and its events."` (`g-view.plannedNotes`) | Pure build/plan comparison; retention and bounded tombstones (`MaxTombstones = 4,096`) are stated verbatim in `g-view.description`. |
| `docs/c4/model.json` | `"Added by the build; PLAN.md does not name it."` (`g-wire`, `m-wire`, `m-membership`, `t-proto` `plannedNotes`) | Pure build/plan comparison; each component's `description` carries its full technical specification. |

---

## 3. Remaining hits of the search pattern judged allowed (grouped by reason)

### Chapter 7 title / slug / links (explicitly frozen by Rule 3)
- `README.md:18`, `docs/index.md:18`, `book/manifest.json:49`, `book/front/preface.md:9`, `book/front/topic-map.md:13`, `book/chapters/01-deterministic-simulation.md:55,223`, `book/chapters/02-swim-gossip.md:253`, `book/chapters/03-reed-solomon-erasure-coding.md:135,245,250`, `book/chapters/04-merkle-anti-entropy.md:131,261`, `book/chapters/05-optimistic-concurrency-control.md:134,236`, `book/chapters/06-sagas.md:81,151,253,258`, `book/chapters/07-object-store-and-sync-engine.md:1`, `book/chapters/08-end-to-end-and-hardening.md:3,222,224,229` — `[The Object Store and Sync Engine](...)`.

### Literal paths, commands, flags, URLs, filenames, identifiers, and string literals in Go code
- `README.md:48,54`: `agent_work/gates/ci-fast.sh`, `GATE_STAGE=final agent_work/gates/gate.sh demo` (executable command lines; must remain byte for byte).
- `book/chapters/06-sagas.md:264`: `sagasim/review2_test.go` (literal repo filename).
- `docs/c4/model.json:4230`: `"url": "https://github.com/nickstrad/object-store/blob/main/PLAN.md"` (brief prohibits editing `url` values in `model.json`).
- `internal/storage/httpapi/http.go:111-112`, `internal/storage/httpapi/http_test.go:464`, `test/e2e/realcluster_test.go:561`: `http.StatusGatewayTimeout` (substring `gate`).
- `internal/gossip/dst_test.go:43,668,673`: `pauseAt` (substring `seat` in `pau`**`seAt`** — Go identifier).
- `internal/occ/encoding_test.go:160,202,204,220,223,224,230,235,243`: `reviewerShape`, `TestReviewerHostileInputIsRejectedEarly`, fuzz corpus filename `reviewer-28k-multi-version`, and test string literals (Go code, string literals, and doc comments naming their own Go identifiers).
- `internal/merkle/replysize_test.go:103`: `t.Run("the reviewer's 32 KiB inner reply is rejected before allocation", ...)` (Go string literal; code must not change).
- `internal/node/secondeyes_arbiter_test.go:12,16`: `TestArbitrationOrphansPlusAnUnwrittenShardFixed` (Go function identifier and doc comment naming it).
- `internal/node/dst_migstall_test.go:1290-1444`: `verifyWaveNet`, `onWave`, `waveActive` (network request wave identifiers and comment).
- `test/e2e/scenario_e6_test.go:47,69,70,84,86,93,98,124,134`, `test/e2e/tiers_test.go:122`, `test/e2e/tierr_test.go:127`, `test/e2e/canary_test.go:297`, `internal/node/secondeyes_confirm_test.go:39,48`: `perWave` / `wave` (write-traffic waves in E6 and confirmation tests).
- `test/e2e/scenario_e3_test.go:192,235,252`: `"phase 1 ..."` / `"phase 2 ..."` (test fault-injection phases in E3).
- `test/e2e/scenario_e6_test.go:610-611`: `"review-a"`, `"review-b"` (synthetic object tag values in E8).
- `internal/node/secondeyes_late_shards_test.go:55,58,82,379`, `internal/node/secondeyes_orphan_test.go:53,61`, `internal/node/secondeyes_arbiter_test.go:54,57`, `internal/node/gc_test.go:183,191,192`, `internal/node/lost_test.go:137,153,154`: `landed` (local test variable / failure message for shards arriving on disk).

### Substring matches in normal technical words
- `README.md:71`, `book/chapters/08-end-to-end-and-hardening.md:145`: `aggregate` (matches `gate`).
- `internal/merkle/tree.go:8`, `internal/occ/node.go:418`, `internal/sim/simtest/simtest_test.go:181`: `propagate` / `propagates` (matches `gate`).
- `internal/gossip/dst_test.go:696`: `briefly` (matches `brief`).
- `test/e2e/oracle_self_test.go:287,294,305,315,324`: `unknownErr` (substring `owner` in `unkn`**`ownEr`**`r`).

### Saga orchestrator component (explicitly allowed by Rule 1)
- `docs/c4/README.md:14,32`, `docs/c4/08-component-saga.md:11,48,65,90,109,127,136,137,138,140,143,147,149,156,157`, `docs/c4/model.json:254,783,799,2285`, `docs/c4/diagrams/08-component-saga-{a,b,c,d}.mmd`, `internal/saga/engine.go:1`.

### Technical `owner` / `ownership` (saga flip owner, bucket owner, buffer ownership, object tag key `"owner"`)
- `book/chapters/07-object-store-and-sync-engine.md:118`, `docs/c4/08-component-saga.md:144`, `internal/node/migrate.go:851-1019`, `internal/node/export_test.go:59-60`, `internal/node/flip_test.go:116-118`, `internal/node/node_unit_test.go:450-454,492` (migration flip owner).
- `book/chapters/06-sagas.md:142`, `internal/saga/migration.go:118`, `internal/saga/sagasim/ops.go:23,56`, `internal/saga/sagasim/cluster.go:414`, `internal/saga/sagasim/codec.go:187,194,230`, `internal/saga/sagasim/store.go:91,415-529` (staging/bucket ownership).
- `internal/gossip/node.go:345,772` (buffer slice ownership).
- `internal/node/dst_partition_test.go:168-169`, `internal/noderuntime/conformance_test.go:131`, `test/e2e/oracle_self_test.go:217-218`, `test/e2e/scenario_e6_test.go:279,378-381` (synthetic object tag key `"owner"`).

### Architectural trade-offs & present-tense CRDT statements (explicitly allowed by Rule 1)
- `book/chapters/05-optimistic-concurrency-control.md:7,131,255`: CRDTs and siblings stated as roads not taken / trade-off and bibliography entry.
- `docs/c4/07-component-occ.md:210`, `docs/c4/model.json:239`: Present-tense statement that there are no CRDT types and no sibling versions.

### Technical verb `exercise` / `exercised` (explicitly allowed by Rule 2) and packet `landing`
- `book/front/preface.md:7`, `book/chapters/01-deterministic-simulation.md:207`, `book/chapters/07-object-store-and-sync-engine.md:238`, `internal/testutil/leakcheck/leakcheck.go:1`, `internal/gossip/node_test.go:365`, `internal/gossip/dst_test.go:705`, `internal/occ/lww2_dst_test.go:84`, `internal/node/dst_crash_test.go:231`, `internal/node/node_unit_test.go:18`, `internal/node/prop_test.go:179`, `internal/node/secondeyes_late_shards_test.go:270`, `internal/noderuntime/runtime_more_test.go:17`, `internal/noderuntime/runtime_ops_test.go:76`, `test/e2e/absence_test.go:27`, `test/e2e/e7_test.go:218`.
- `book/chapters/06-sagas.md:142`: "late replies landing after compensation" (network reply arrival).

### Adversarial testing & measured test results (technical testing terminology & measurements)
- `book/chapters/01-deterministic-simulation.md:208`, `book/chapters/02-swim-gossip.md:238,242`, `book/chapters/03-reed-solomon-erasure-coding.md:241`, `book/chapters/04-merkle-anti-entropy.md:251`, `book/chapters/05-optimistic-concurrency-control.md:225`, `book/chapters/06-sagas.md:245`, `book/chapters/07-object-store-and-sync-engine.md:227`, `book/chapters/08-end-to-end-and-hardening.md:5,69,71,232`, `internal/merkle/store_test.go:202` (`liar is an adversarial simulated peer`): standard software-testing terminology describing independent fault-attack test suites and measured check counts; none mention agents, seats, reviewers, or the build workflow.
- `book/chapters/08-end-to-end-and-hardening.md:232`: `30-wave soak` (workload wave count in soak test).
- `book/chapters/07-object-store-and-sync-engine.md:231`: `briefly` (matches `brief` substring in "leaving `k` shards briefly").

---

## 4. Items left for the orchestrator

1. **Pre-existing dirty file:** `agent_work/orch/launch.sh` was already modified in the worktree before this pass began; it was not touched.
2. **`docs/c4/model.json` top-level `title`, `revision`, and diagram `title` fields:** Lines 4 (`"Object Store \u2014 architecture as built"`), 5 (`"as-built-final-2026-11-23"`), and the nine diagram titles (`"… \u2014 as built"`) were left untouched because the brief states the model title was already changed and instructs editing only description/note string values (and `sources[0].title`). If you want `" — as built"` trimmed from those titles or `revision` updated, that is a one-line follow-up.
3. **Test filenames and identifiers containing process-era words:** `internal/node/secondeyes_*_test.go` (and `TestArbitrationOrphansPlusAnUnwrittenShardFixed`), `internal/saga/sagasim/review2_test.go`, `internal/occ/encoding_test.go` (`reviewerShape`, `TestReviewerHostileInputIsRejectedEarly`, `testdata/fuzz/FuzzOCCDecode/reviewer-28k-multi-version`), and `internal/merkle/replysize_test.go:103` (`"the reviewer's 32 KiB inner reply is rejected before allocation"`) were left untouched per the brief's rule against modifying Go code, string literals, filenames, or identifiers.
4. **Adversarial testing bullets in chapters 1–8:** These bullets report measured check counts from independent fault/attack test suites (e.g. 58/60, 72/73, 41/42, 72/74, 60/61, 125/125, 662/662) and do not mention agents, seats, reviewers, or build machinery. They were preserved in full to uphold claims discipline (no dropped numbers or test results). Note that chapter 7 line 227 references the literal filename pattern `internal/node/secondeyes_*_test.go` alongside its 20-test / 3-defect count.
5. **Chapter 7 title:** Kept as `The Object Store and Sync Engine` everywhere per Rule 3.

---

## 5. Verification results

- `docker compose run --rm -T dev bash -c 'gofmt -l . ; go build ./... && go vet ./...'`: clean (empty `gofmt -l .`, build and vet exited 0).
- `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py .`: `site-lint: ok (0 findings)` (22 pages built).
- `python3 -c "import json;json.load(open('docs/c4/model.json'))"`: valid JSON; structural check confirmed only allowed string values changed and no keys, IDs, `name`, `status`, `url`, or `technology` values changed.
- Diagram & code-block integrity check: all Mermaid blocks, `<!-- draw-visual: … -->` markers, and fenced `text` blocks in `docs/c4/*.md`, all fenced `sh` blocks in `README.md`, and all fenced `go` blocks in `book/chapters/*.md` are byte-for-byte identical to `HEAD`.
- `agent_work/09-cleanup/repo-description.txt`: 1 line, 258 characters (within the 300-character limit).
- `git diff --stat`: touches only files in scope (plus the pre-existing `agent_work/orch/launch.sh`) and the two output files under `agent_work/09-cleanup/`.

---

## Fix round 1

Closed all four nits from `REVIEW-1.md`:

1. **`README.md`**: Renamed `## What is built` to `## What is in the repository`, removed the constant `Status` column, renamed the first column header from `Building block` to `Part`, and renamed the two build-stage row labels (`Foundation` → `Environment and checks`, `Integration` → `Node, runtime, and API`). Kept all 10 rows and every word of every `Scope` cell unchanged. Verified no links pointed to `#what-is-built`.
2. **`docs/c4/README.md`**: Removed the constant `State` column from the first table while keeping every row, `Part` cell, and `Diagrams` cell unchanged.
3. **`docs/c4/model.json` (`sources[0]`)**: Kept `"id": "plan"`, set `url` to `https://nickstrad.github.io/object-store/book/`, set `title` to `"Object Store book"`, and updated `notes` to describe the book as the architecture and package reference for the views.
4. **`docs/c4/model.json` (titles)**: Trimmed `" as built"` from the top-level `title` (`"Object Store \u2014 architecture"`) and trimmed `" \u2014 as built"` from all nine diagram `title` values, leaving `revision` and formatting untouched.

Verification after fix round 1:
- `python3 -c "import json;json.load(open('docs/c4/model.json'))"`: passed.
- `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py .`: `site-lint: ok (0 findings)`.
- `agent_work/09-cleanup/check-prose.py HEAD`: only `FINDING` is the instructed `sources[0].url` change; no fenced blocks changed and no structural findings.
