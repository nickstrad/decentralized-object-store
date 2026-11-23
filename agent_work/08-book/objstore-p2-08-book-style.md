# Book style sheet — the consistency edit (shared by every writer of the publishing finish)

The book is a concise study book: it teaches each topic and shows where it lives in this
repository. The reader is a working engineer with no knowledge of this code base. The
chapters were written one at a time as each building block landed; this pass makes them one
book. CONCISENESS IS THE TOP CONSTRAINT.

## One chapter shape (same `##` headings, in this order, in every chapter)

1. (no heading) Opening: the idea in two to four short paragraphs — what it is, why it
   exists, the core mechanism. Every term is explained in plain words the first time it
   appears in the BOOK (later chapters may rely on earlier ones and link back).
2. `## How <topic> maps to this repository` — packages and files, then the design choices
   and what each costs (`### Design choices and what each costs`), then how it is tested on
   the simulator (`### Testing on the simulator`). Other `###` headings name the idea, with
   the file names in the text, not in the heading.
3. `## Key pieces in code` — three to five excerpts, each under about 20 lines, copied
   EXACTLY from the file named above it (check each against the file; fix any that drifted).
4. Diagrams sit where they help, inside 2. Each keeps its ```mermaid block and, directly
   under it, the terminal rendering with its `<!-- draw-visual: ... -->` marker. Do not
   hand-edit a rendering; if you change a `.mmd` source, re-run the renderer named in your
   brief.
5. `## What the tests show, and what they do not` with `### Narrowed claims` and
   `### What the tests do not prove` (both always present; say "None." if empty).
6. `## Go deeper in the repo`
7. `## Sources and further reading`

## What the edit may and may not change

- MAY: cut repetition, split long sentences, replace a compressed spec-like sentence by a
  plain one, define a term, move text to the section it belongs to, rename headings to the
  shape above, add one or two connecting sentences at a chapter's start ("the previous
  chapter built X; this one...") and end, fix a wrong path or an excerpt that no longer
  matches the code, make names consistent (below).
- MAY NOT: change, strengthen or drop any number, measurement, guarantee, narrowed claim or
  stated limit; add a claim the chapter did not make; add a command you did not check
  against the code; remove a narrowed claim or a "does not prove" item to save words. If
  two chapters disagree on a fact, do not pick one: list it in your report.
- Length: each chapter ends between 1,800 and 2,600 words (count with `wc -w`); a chapter
  already inside the range gets shorter only where the cut loses nothing.

## One vocabulary

- "the simulator" (not "DST harness" in running text; "deterministic simulation testing" is
  spelled out once per chapter), "seed", "sweep" (a run over many seeds), "replay".
- "shard" (one erasure-coded piece), "stripe" (k+m shards of one data block), "manifest",
  "data shards k, parity shards m" — defaults 4 and 2 written "4+2".
- "last-writer-wins" (hyphenated, lower case; "LWW" only after it has been spelled out in
  that chapter), "hybrid logical clock", "version vector", "discarded write".
- "node" for a running store process, "peer" for another node, "region", "bucket".
- "the object API" for the HTTP interface; `objnode`, `objsim` in code font.
- Conflicts are settled by last-writer-wins with every discard counted and logged. No
  chapter describes siblings or CRDTs as this project's design (they are roads not taken).
- Chapters refer to each other by title with a relative link (`03-reed-solomon-erasure-coding.md`),
  never by number alone and never as "coming" — all eight chapters now exist.

## Rules for everything reader-facing

- Plain, concrete English; short sentences; no hype; teach, do not sell.
- Self-contained: never mention this machine, absolute paths, agents, models, reviewers,
  briefs, chunk numbers, or how the text was produced. Repository-relative paths only;
  relative links only to files that exist.
- Cite only works you are sure exist; no invented URLs.
