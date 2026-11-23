# Brief — Chunk 7 PROSE, part 2: book chapter "End-to-End Tests and Hardening"

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/07-e2e`). You write ONE chapter file and its diagram sources:
- `book/chapters/08-end-to-end-and-hardening.md`
- `book/chapters/diagrams/08-*.mmd`
Touch nothing else (another writer is adding comments to the Go files at the same time —
read the code, do not edit it). Do NOT run any git write command. Do not edit
`book/manifest.json`.

## The book and this chapter
The book is the project's learning artifact: a concise study book that teaches each topic
and shows where it lives in this repository, published chapter by chapter as each building
block lands. Read the published chapters under `book/chapters/` first and match their
shape, tone and length. The reader is a working engineer who wants to LEARN the topic and
then find it in the code. Conciseness is the top constraint: about 1,800–2,800 words.

First line of the file, exactly: `# End-to-End Tests and Hardening`

## Chapter shape (six parts, your own headings, as in the earlier chapters)
1. The idea, concisely: what it is, why it exists, the core mechanism.
2. How it maps to this project: which packages and files implement it, the design choices
   made here and what each costs, how it is tested on the simulator (earlier chapter).
3. Three to five short code excerpts that carry the idea (each under ~20 lines, copied
   exactly, with the file path). No long listings.
4. One or two diagrams where they genuinely help. Each appears twice: a ```mermaid block
   and, directly under it, a terminal rendering generated from a `.mmd` source saved as
   `book/chapters/diagrams/08-<name>.mmd`: put this marker and an empty fenced
   block in the chapter, then run the renderer, which fills it in:
       <!-- draw-visual: diagrams/08-<name>.mmd -->
       ```text
       ```
       python3 /root/.claude/skills/draw-visual/scripts/render.py --update book/chapters/08-end-to-end-and-hardening.md
       DRAW_VISUAL_MAX_WIDTH=80 python3 /root/.claude/skills/draw-visual/scripts/render.py --check book/chapters/08-end-to-end-and-hardening.md
   Keep sources simple (`graph TD`, or `sequenceDiagram` for message exchanges; at most
   about eight nodes; short labels; no subgraphs) so the text fits 80 columns; look at the
   rendering and simplify if garbled. The tool path is for you only; never in the chapter.
5. "What the tests show, and what they do not": the verified evidence, stated exactly, with
   every narrowed claim in its narrowed form and why. See Evidence.
6. "Go deeper in the repo": exact pointers — files, the named tests, fuzz targets, the
   adversarial harness `agent_work/adversarial/07-e2e/`, one or two commands to run
   (through `docker compose run --rm dev ...`; check each against the code). Then "Sources
   and further reading": Kingsbury's Jepsen analyses (nemesis schedules, what a passing run does and does not show); the FoundationDB simulation-testing talk and paper; TigerBeetle's VOPR write-ups; Yuan et al., Simple Testing Can Prevent Most Critical Failures; the Go fuzzing documentation — one line each on what to take from it. Cite only works
   you are sure exist; no invented URLs.

## Evidence (verified; use these and nothing stronger)
`agent_work/07-e2e/build-notes.md` (claims table, measurements, every round section),
`agent_work/07-e2e/REVIEW-*.md`, `agent_work/adversarial/07-e2e/RESULTS.md`. Every number
in the chapter comes from those files. Narrowed claims go in narrowed form. Say what is
proven only on the simulator and what waits for the integrated node. Building blocks not
yet in `book/chapters/` are "coming chapters", without links; published chapters may be
linked relatively (`01-deterministic-simulation.md`).

## Rules
- Plain, concrete English; short sentences; no hype. Teach, do not sell.
- Self-contained: never mention this machine, absolute paths, agents, models, reviewers,
  briefs, chunk numbers, or how the text was produced. Repository-relative paths; relative
  links only to files that exist (from `book/chapters/`: `../../test/e2e/<file>.go`).
- When done run `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src`.
  It will report ONE expected finding, `chapter-unlisted` for this chapter. Fix anything else.

## Chunk-specific points the chapter must get right (orchestrator's addendum)

These come from the reviews and the adversarial pass; each is backed by the notes named.

1. **The suite found two product defects, and the chapter says so plainly** (the first build
   reported none). (a) A read of a live object that could not gather k shards answered "not
   found" (HTTP 404) instead of "unavailable" (503): build-notes section 12, fix `f70e2c2`.
   (b) A bucket migration whose coordinator was frozen for longer than the orphan grace
   period right after copying completed and lost acknowledged objects: found by the
   adversarial pass, `agent_work/adversarial/07-e2e-opus/ATTACKS.md` F1, fix `eaa0556`,
   build-notes section 15. Tell (b) as the worked example of why an end-to-end suite needs
   an outside attacker: every existing fault lasted seconds, the bug needed one process
   stalled for a minute.
2. **The migration guarantee, in the exact wording of build-notes section 15** ("The
   guarantee, in one sentence"), with how it is achieved: the holder that confirms a shard
   is, by the same request, made to keep it. Use the stated threshold. Do not claim more.
3. **Limits, stated as limits** (build-notes sections 8 and 15; `docs/knowledge/chunk-07-e2e.md`
   "Stated limits"): metadata acknowledged with one copy on the writer's disk; a
   coordinator that never returns; whole-bucket migration messages bounded by the frame
   size; real-process write concurrency of one outside E9; static clock skew only; no
   read-your-writes promise during a partition.
4. **What a passing run does and does not show**, per scenario, from the claim tables of
   build-notes section 3 — and the three coverage holes the first review found in the suite
   itself (a fuzzed migration that always moved nothing; a timed-out start treated as
   nothing started; a partition test that required nothing to succeed), with the lesson:
   every repaired check is mutation-tested and keeps a canary.
5. **Replay.** Every failure prints a REPLAY command; the chapter shows one. It may mention
   that the adversarial pass found replay commands that did not replay (seeds were grouped
   by position, not value) and that a test now re-executes every printed command.
6. **How the long runs are run.** Push-triggered CI runs only a fast lane (build, static
   checks, unit tests, pinned seeds plus three fixed seeds; `go test -short` trims the
   78-cell migration failure matrix to a smoke). The full gate, the fuzz budgets
   (`agent_work/07-e2e/hardening/run-fuzz.sh`), the large seed sweep (`run-sweep.sh`) and
   the real-process tier are on-demand runs through Docker; give the commands a reader can
   run. Numbers (run times, seed counts) only from the notes.
7. Measurement names changed during the finish: use `dest_*` and `moved_*` as defined in
   build-notes section 13, never "migrated shards" for shards merely present afterwards.
