# Brief — Chunk 5 PROSE, part 2: book chapter "Sagas"

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/05-saga`). You write ONE chapter file and its diagram sources:
- `book/chapters/06-sagas.md`
- `book/chapters/diagrams/06-*.mmd`
Touch nothing else (another writer is adding comments to the Go files at the same time —
read the code, do not edit it). Do NOT run any git write command. Do not edit
`book/manifest.json`.

## The book and this chapter
The book is the project's learning artifact: a concise study book that teaches each topic
and shows where it lives in this repository, published chapter by chapter as each building
block lands. Read the published chapters under `book/chapters/` first and match their
shape, tone and length. The reader is a working engineer who wants to LEARN the topic and
then find it in the code. Conciseness is the top constraint: about 1,800–2,800 words.

First line of the file, exactly: `# Sagas`

## Chapter shape (six parts, your own headings, as in the earlier chapters)
1. The idea, concisely: what it is, why it exists, the core mechanism.
2. How it maps to this project: which packages and files implement it, the design choices
   made here and what each costs, how it is tested on the simulator (earlier chapter).
3. Three to five short code excerpts that carry the idea (each under ~20 lines, copied
   exactly, with the file path). No long listings.
4. One or two diagrams where they genuinely help. Each appears twice: a ```mermaid block
   and, directly under it, a terminal rendering generated from a `.mmd` source saved as
   `book/chapters/diagrams/06-<name>.mmd`: put this marker and an empty fenced
   block in the chapter, then run the renderer, which fills it in:
       <!-- draw-visual: diagrams/06-<name>.mmd -->
       ```text
       ```
       python3 /root/.claude/skills/draw-visual/scripts/render.py --update book/chapters/06-sagas.md
       DRAW_VISUAL_MAX_WIDTH=80 python3 /root/.claude/skills/draw-visual/scripts/render.py --check book/chapters/06-sagas.md
   Keep sources simple (`graph TD`, or `sequenceDiagram` for message exchanges; at most
   about eight nodes; short labels; no subgraphs) so the text fits 80 columns; look at the
   rendering and simplify if garbled. The tool path is for you only; never in the chapter.
5. "What the tests show, and what they do not": the verified evidence, stated exactly, with
   every narrowed claim in its narrowed form and why. See Evidence.
6. "Go deeper in the repo": exact pointers — files, the named tests, fuzz targets, the
   adversarial harness `agent_work/adversarial/05-saga/`, one or two commands to run
   (through `docker compose run --rm dev ...`; check each against the code). Then "Sources
   and further reading": Garcia-Molina and Salem (1987), Sagas; Gray on compensating transactions; Helland, Life Beyond Distributed Transactions; write-ups on idempotency keys and durable workflow engines — one line each on what to take from it. Cite only works
   you are sure exist; no invented URLs.

## Evidence (verified; use these and nothing stronger)
`agent_work/05-saga/build-notes.md` (claims table, measurements, every round section),
`agent_work/05-saga/REVIEW-*.md`, `agent_work/adversarial/05-saga/RESULTS.md`. Every number
in the chapter comes from those files. Narrowed claims go in narrowed form. Say what is
proven only on the simulator and what waits for the integrated node. Building blocks not
yet in `book/chapters/` are "coming chapters", without links; published chapters may be
linked relatively (`01-deterministic-simulation.md`).

## Rules
- Plain, concrete English; short sentences; no hype. Teach, do not sell.
- Self-contained: never mention this machine, absolute paths, agents, models, reviewers,
  briefs, chunk numbers, or how the text was produced. Repository-relative paths; relative
  links only to files that exist (from `book/chapters/`: `../../internal/saga/<file>.go`).
- When done run `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src`.
  It will report ONE expected finding, `chapter-unlisted` for this chapter. Fix anything else.
