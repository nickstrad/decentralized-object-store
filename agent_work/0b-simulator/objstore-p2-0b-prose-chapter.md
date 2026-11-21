# Brief — Chunk 0b PROSE, part 2: book chapter 1, deterministic simulation testing

Seat: Gemini 4 Neon, prose writer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0b-simulator (branch `chunk/0b-simulator`).
You write ONE chapter file and its diagram source(s):
- `book/chapters/01-deterministic-simulation.md`
- `book/chapters/diagrams/01-*.mmd` (Mermaid sources for the terminal renderings)
Touch nothing else (another writer is adding comments to the Go files at the same time —
read the code, do not edit it). Do NOT run any git write command: leave the files in the
working tree; they are committed for you under your byline. Do not edit
`book/manifest.json` (the chapter is listed there for you afterwards).

## The book and this chapter
The book is the project's learning artifact: a concise, Leanpub-style study book that
teaches each topic and shows where it lives in this repository. One chapter per building
block, PUBLISHED on the project site as each lands. This is the first chapter. The reader
is a working engineer who wants to LEARN deterministic simulation testing (DST) and then
find it in the code. Conciseness is the top constraint: about 1,800–2,800 words. Cut
anything that does not help the reader learn the topic or locate it in the project. Never
repeat in prose what a snippet or a pointer already says.

First line of the file, exactly: `# Deterministic Simulation Testing`

## Chapter shape (use these six parts, with your own headings)
1. The idea, concisely: what DST is, why distributed systems need it (bugs that depend on
   timing and failure order cannot be reproduced otherwise), the core mechanism — one
   seed, a virtual clock, a single-threaded event loop, simulated network and disk, so the
   same seed replays the same run byte for byte.
2. How it maps to this project: `internal/env` (the four interfaces protocol code sees —
   Clock, Rand, Transport, Disk), `internal/sim` (named PRNG streams from one seed, the
   event queue ordered by time then sequence, the fault-injecting network and disk, the
   nemesis that alternates chaos and healing inside a fault budget, the event log and the
   two digests), `internal/sim/simtest` (seeds, sweeps, the determinism check, the REPLAY
   line), `cmd/objsim`. The design choices made here and what each costs: protocol code as
   a single-threaded state machine; integers not floats; errors crossing the network as
   status codes; two delivery classes (datagram vs request/response); written vs durable
   disk layers. How the project keeps itself honest: a lint that bans clocks, goroutines
   and unordered map iteration from simulated packages; replay tests across processes;
   canary tests that plant a bug and require the harness to catch it.
3. A few short code excerpts that carry the idea (three to five, each under ~20 lines,
   copied exactly from the repository, with the file path): for example the stream
   derivation, the event ordering, what a DST test looks like through `simtest`, and the
   failure report with its REPLAY line. No long listings.
4. One or two diagrams where they genuinely help (for example: seed → named streams → loop
   → network/disk → digests; or the life of one message through drop/delay/deliver). Each
   diagram appears twice: a ```mermaid block, and directly under it a terminal rendering
   generated from a `.mmd` source: write the source to
   `book/chapters/diagrams/01-<name>.mmd`, put this marker and an empty fenced block in the
   chapter, then run the renderer, which fills the block in:
       <!-- draw-visual: diagrams/01-<name>.mmd -->
       ```text
       ```
       python3 /root/.claude/skills/draw-visual/scripts/render.py --update book/chapters/01-deterministic-simulation.md
       DRAW_VISUAL_MAX_WIDTH=80 python3 /root/.claude/skills/draw-visual/scripts/render.py --check book/chapters/01-deterministic-simulation.md
   (run `bash /root/.claude/skills/draw-visual/scripts/setup.sh` first if the renderer is
   missing). Keep sources simple — `graph TD` or `graph LR`, at most about eight nodes,
   short labels, no subgraphs — so the text rendering fits 80 columns and reads cleanly.
   Look at the rendered text; if it is garbled, simplify the source. See
   `docs/c4/01-system-context.md` and `docs/c4/diagrams/` for the pattern. The tool path
   above is for you only; it must not appear in the chapter.
5. "What the tests show, and what they do not": the verified evidence, stated exactly —
   see "Evidence" below. Include the narrowed claims in their narrowed form and say why
   they were narrowed. This honesty section is what makes the chapter worth trusting.
6. "Go deeper in the repo": exact pointers — packages, key files, the named tests (replay
   meta-tests, canaries, properties, the fuzz target), `agent_work/adversarial/0b-simulator/`
   (the attack harness and its results), and how to replay a seed
   (`docker compose run --rm dev go test ./internal/sim -run '^TestReplaySameProcess$' -seed=3 -count=1`
   — check the exact flag spelling against `internal/sim/simtest`), and
   `docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3`.
   Then "Sources and further reading": the FoundationDB simulation testing talk (Will
   Wilson, Strange Loop 2014), TigerBeetle's VOPR write-ups, the Jepsen analyses, Antithesis'
   writing on deterministic hypervisors, and Go's `testing/synctest` — one line each on
   what to take from it. Cite only sources you are sure exist; no invented URLs (name the
   work and author; add a URL only if you are certain of it).

## Evidence (verified; use these and nothing stronger)
Read `agent_work/0b-simulator/build-notes.md` (claims table, measurements, all round
sections), `agent_work/0b-simulator/REVIEW-1.md`, `REVIEW-2.md`, `REVIEW-3.md`, and
`agent_work/adversarial/0b-simulator/RESULTS.md` (attacks held, broken, repaired,
narrowed). Rules:
- A number in the chapter must come from those files (event rates, seed counts, coverage,
  attack counts). Give counts as they are recorded there.
- Narrowed claims go in narrowed form. In particular: the nemesis's schedule — when it
  acts, on which node, with which kind of fault — depends only on the seed and the
  nemesis configuration; which blob a disk fault hits depends on what the protocol has
  written. And: the determinism lint covers product code, not test files.
- The simulator proves properties of protocol code under the faults it models; it does
  not prove anything about real sockets, real disks or the Go scheduler. Say so.
- Other building blocks (gossip, erasure coding, Merkle repair, OCC metadata, sagas) are
  not built yet: refer to them as coming chapters, without links.

## Rules
- Plain, concrete English; short sentences; no hype. Teach, do not sell.
- Self-contained: never mention this machine, absolute paths, agents, models, reviews by
  name of reviewer, briefs, chunk numbers, or how the text was produced. Paths are
  repository-relative. Links: only relative links to files that exist in the repository
  (from `book/chapters/`, e.g. `../../internal/sim/stream.go`), or none.
- When done, run `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src`.
  It will report ONE expected finding, `chapter-unlisted` for this chapter (the manifest
  entry is added afterwards). Fix anything else it reports.
