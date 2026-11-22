# Brief — Chunk 4 PROSE, part 2: book chapter "Optimistic Concurrency Control"

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/04-occ`). You write ONE chapter file and its diagram sources:
- `book/chapters/05-optimistic-concurrency-control.md`
- `book/chapters/diagrams/05-*.mmd`
Touch nothing else (another writer is adding comments to the Go files at the same time —
read the code, do not edit it). Do NOT run any git write command. Do not edit
`book/manifest.json`.

## The book and this chapter
The book is the project's learning artifact: a concise study book that teaches each topic
and shows where it lives in this repository, published chapter by chapter as each building
block lands. Read the published chapters under `book/chapters/` first and match their
shape, tone and length. The reader is a working engineer who wants to LEARN the topic and
then find it in the code. Conciseness is the top constraint: about 1,800–2,800 words.

First line of the file, exactly: `# Optimistic Concurrency Control`

## Chapter shape (six parts, your own headings, as in the earlier chapters)
1. The idea, concisely: what it is, why it exists, the core mechanism.
2. How it maps to this project: which packages and files implement it, the design choices
   made here and what each costs, how it is tested on the simulator (earlier chapter).
3. Three to five short code excerpts that carry the idea (each under ~20 lines, copied
   exactly, with the file path). No long listings.
4. One or two diagrams where they genuinely help. Each appears twice: a ```mermaid block
   and, directly under it, a terminal rendering generated from a `.mmd` source saved as
   `book/chapters/diagrams/05-<name>.mmd`: put this marker and an empty fenced
   block in the chapter, then run the renderer, which fills it in:
       <!-- draw-visual: diagrams/05-<name>.mmd -->
       ```text
       ```
       python3 /root/.claude/skills/draw-visual/scripts/render.py --update book/chapters/05-optimistic-concurrency-control.md
       DRAW_VISUAL_MAX_WIDTH=80 python3 /root/.claude/skills/draw-visual/scripts/render.py --check book/chapters/05-optimistic-concurrency-control.md
   Keep sources simple (`graph TD`, or `sequenceDiagram` for message exchanges; at most
   about eight nodes; short labels; no subgraphs) so the text fits 80 columns; look at the
   rendering and simplify if garbled. The tool path is for you only; never in the chapter.
5. "What the tests show, and what they do not": the verified evidence, stated exactly, with
   every narrowed claim in its narrowed form and why. See Evidence.
6. "Go deeper in the repo": exact pointers — files, the named tests, fuzz targets, the
   adversarial harness `agent_work/adversarial/04-occ/`, one or two commands to run
   (through `docker compose run --rm dev ...`; check each against the code). Then "Sources
   and further reading": Kung and Robinson (1981) on optimistic concurrency control; Parker et al. on version vectors; Kulkarni et al. on hybrid logical clocks; RFC 9110 on ETags and conditional requests; the Dynamo paper on siblings and Shapiro et al. on CRDTs as the two roads NOT taken (the design is last-writer-wins with every discard counted and logged — no chapter may describe siblings as the design); Kleppmann on the lost-update cost of last-writer-wins — one line each on what to take from it. Cite only works
   you are sure exist; no invented URLs.

## Evidence (verified; use these and nothing stronger)
`agent_work/04-occ/build-notes.md` (claims table, measurements, every round section),
`agent_work/04-occ/REVIEW-*.md`, `agent_work/adversarial/04-occ/RESULTS.md`. Every number
in the chapter comes from those files. Narrowed claims go in narrowed form. Say what is
proven only on the simulator and what waits for the integrated node. Building blocks not
yet in `book/chapters/` are "coming chapters", without links; published chapters may be
linked relatively (`01-deterministic-simulation.md`).

## Rules
- Plain, concrete English; short sentences; no hype. Teach, do not sell.
- Self-contained: never mention this machine, absolute paths, agents, models, reviewers,
  briefs, chunk numbers, or how the text was produced. Repository-relative paths; relative
  links only to files that exist (from `book/chapters/`: `../../internal/occ/<file>.go`).
- When done run `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src`.
  It will report ONE expected finding, `chapter-unlisted` for this chapter. Fix anything else.

## Binding for this chapter: the design is last-writer-wins, and the trade-off is stated plainly
- The package keeps ONE version per key. When two writers update a key without seeing
  each other, last-writer-wins decides: the higher hybrid-logical-clock stamp wins, node
  ID breaks ties, every replica picks the same winner, and the losing write is discarded.
  Say in plain words, early and without softening: **an acknowledged write can be lost to
  a concurrent write under partition; the system guarantees convergence and a record of
  the loss, not preservation.** Give the measured rate from the build notes with its
  conditions (it is a deliberately contested workload).
- The chapter has a trade-off passage on the two roads not taken: CRDTs (the original
  specification) and Dynamo/Riak-style sibling versions (the first design, rejected
  because surfacing conflicts to readers as co-equal versions was judged a bad design
  for this store). Siblings are described ONLY as a rejected alternative, never as what
  the code does. Ignore every mention of siblings in `REVIEW-1.md` and the first repair
  brief — that code is gone.
- State the record-of-loss guarantee exactly as the final verdict
  (`agent_work/04-occ/ARBITRATION-1.md`) words it, no stronger: every concurrent discard
  is counted and emitted (log line and callback), with replay after a crash; the same
  loss may be recorded on more than one node and, after a bounded de-duplication window,
  more than once on one node (an over-count, never an under-count); the records are
  local observations, not replicated and not a permanent archive; the one loss with no
  record is a version whose every holder lost its disk before passing it on. Version
  vectors only classify a replacement as an ordinary overwrite or a discard; they never
  pick the winner.
- Conditional writes (ETags, If-Match / If-None-Match) are what the "optimistic
  concurrency control" of the title refers to; explain what a precondition guards
  against (writers this node has heard from) and what it cannot (the other side of a
  partition), which is exactly where last-writer-wins takes over.
- Evidence files for this chapter also include `ARBITRATION-1.md` and `REVIEW-2.md`.
