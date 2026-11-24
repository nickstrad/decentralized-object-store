# Brief — prose cleanup and rebrand to "Object Store" (prose seat, one pass)

You are the prose seat. Work only in the current directory (a git worktree of the
repository, branch `cleanup/object-store`). Do NOT commit, do not create branches, do not
push. Leave your edits in the working tree; the orchestrator commits them.

## Why

The owner's direction (2026-11-23): this repository is a real piece of software — an
object store — with a book that explains its parts. The reader-facing prose must say what
the software is and how it works, and never how it was produced. The project is also
renamed: it is now **Object Store** (repository `nickstrad/object-store`, site
`https://nickstrad.github.io/object-store/`, Go module `github.com/nickstrad/object-store`).
The old name "Decentralized Object Store & Local-First Sync Engine" no longer fits: there
is no client-side or local-first sync and no CRDT layer; concurrent metadata writes settle
by last-writer-wins, and sync runs between nodes only.

The module path, imports, scripts, `book/manifest.json` (title is now "Object Store"), the
C4 model's title and system name, and diagram 1's pictures are already changed. Do not
touch those again except as listed under "model.json" below.

## Files in scope (all of them; read each one end to end)

1. `README.md`
2. `docs/index.md` (the site front page)
3. `book/front/preface.md`, `book/front/topic-map.md`
4. `book/chapters/01-…md` through `08-…md` (all eight)
5. `docs/c4/README.md` and `docs/c4/01-…md` through `09-…md` — the words only
6. `docs/c4/model.json` — string values of description / note fields only (see below)
7. Comments in product Go: every `*.go` under `cmd/`, `internal/`, `test/`
   (not `agent_work/`, not `kb/`), and `cmd/*/help.txt`
8. A replacement GitHub repository description, written to
   `agent_work/09-cleanup/repo-description.txt` (one line, at most 300 characters, plain
   text). The current one wrongly advertises CRDTs and a local-first sync engine:
   "Leaderless multi-region object store & local-first sync engine in Go — SWIM gossip,
   Reed-Solomon, Merkle anti-entropy, CRDTs, Sagas; testing-first with DST, property tests
   and fuzzing". Describe what the software is today.

## The three rules of this pass

### 1. No reference to how the software was built

Remove every mention of the production process, wherever it appears:

- agents, seats, builders, reviewers, arbiters, orchestrators, model or vendor names;
- chunks as work items ("Chunk 4", "when Chunk 6 landed"), waves, phases, landings,
  "landed", "the build added", "added by the build", dated check notes such as
  "checked against the code when Chunk 1 landed, 2026-11-22";
- the owner, rulings, sign-offs, open-question IDs (`OQ-6`, `OQ-15`, …);
- "Source: PLAN.md sections …" lines, "PLAN.md names / does not name it", and links or
  pointers to `PLAN.md` and to `docs/project-source.md` (the plan and the original
  specification stay in the repository as records, but reader-facing prose no longer
  points at them; the plan is no longer a site page);
- design history told as history ("the first design had four CRDT types … was replaced",
  "siblings were built first and then ruled out", "Differences from the target design").
  Where such a passage also states a present-tense fact about the system (for example:
  concurrent versions settle by last-writer-wins and every discard is counted and logged;
  there are no CRDT types and no sibling versions), keep the fact, stated plainly as how
  the system works. A design trade-off stated as a trade-off ("LWW with discard recording
  over CRDTs or siblings: simpler merge, costs lost partitioned writes") is architecture,
  not process — keep it. A bibliography entry is fine; its annotation must not narrate the
  build.
- "gate" used for the build's landing machinery ("landing gates", "enforced by a gate",
  "the gate runs…"). The checks themselves are part of the software's test tooling and
  stay documented: call them checks, the check suite, the fast lane, the full check suite.
  The identifiers the scripts print (`G0`–`G7`, `G5R`) may stay as labels of checks.

Things that are NOT process talk and must stay:

- "chunk" in the storage sense (chunk store, `chunkstore.go`), "orchestrator" as the saga
  orchestrator component, "review"/"owner" in a technical sense (bucket owner, ACL).
- Literal paths, file names, commands, flags, environment variables and identifiers, even
  when they contain words like `agent_work`, `gates`, `GATE_STAGE`, `ci-fast.sh`. Never
  edit a path or a command. Every fenced `sh` block in the README is executed by a check on
  a clean clone; leave every command byte for byte.
- What the tests prove and do not prove, measured results, stated limits.

### 2. No "learning project", "testing project", exercise or study framing

Delete that framing wherever it appears (known places: README line 3, `docs/index.md`
line 3, the preface's first paragraph; look for others). Present the software as what it
is: an object store; the book explains its parts. "Built testing-first" may stay only as a
statement about the software's test coverage if the sentence still reads as a description
of the software; do not replace one framing with another. The verb "exercise" in a
technical sense ("the test exercises the repair path") is fine.

### 3. Rebrand to "Object Store"

- Titles and headings: `README.md` and `docs/index.md` H1 become `# Object Store`.
- Replace "Decentralized Object Store", "Decentralized Object Store & Local-First Sync
  Engine", "… and Sync Engine" as the project's or system's NAME everywhere.
- The README's site link becomes
  `[nickstrad.github.io/object-store](https://nickstrad.github.io/object-store/)`.
- Factual architecture language stays: leaderless, multi-region, gossip-based membership,
  erasure coding, anti-entropy. It describes the system; it is not branding.
- Do NOT rename chapter 7. Its title "The Object Store and Sync Engine", its file name and
  its slug stay exactly as they are (the manifest, the links and the published URL depend
  on them; whether to retitle it is the owner's decision).
- Do not change any chapter title, file name, heading anchor that another page links to,
  or the manifest.

## Claims discipline — unchanged, and the hard limit of this pass

This is a verbiage pass. Do not change any number, bound, count, duration, seed count,
guarantee, limit or technical claim. Do not narrow or widen what the text asserts about
behavior. Do not add new technical claims. When a sentence mixes process talk with a
technical fact, remove the process words and keep the fact with the same strength. When
you delete a whole sentence, it must carry no technical claim that exists nowhere else on
that page; if it does, keep the claim in a reworded sentence.

- Code excerpts in chapters are checked against the source: leave every fenced code block
  byte for byte, with one exception — if you change a Go comment that a chapter excerpt
  quotes, change the excerpt identically.
- In `docs/c4/*.md`: leave every Mermaid block, every `<!-- draw-visual: … -->` marker and
  every fenced `text` rendering byte for byte. Keep every element ID, every table row and
  every relationship number; edit only the words in descriptions and notes. Section
  headings may be renamed or a history-only section removed when nothing links to its
  anchor (check with grep first).
- `docs/c4/model.json`: edit only string VALUES that are descriptions or notes and that
  contain process talk (for example "Added by the build; PLAN.md does not name it.",
  "PLAN.md OQ-15", the reference entry titled "PLAN.md — testing-first plan, Phase 1" may
  have its title reworded but its object stays). Do not add, remove, rename or reorder any
  key, element, relationship, diagram or ID; do not change `name`, `id`, `status`, `url` or
  `technology` values. Keep the file's formatting (2-space indent, `—` escapes as
  they are) so the diff shows only the strings you changed. If a note is nothing but
  process talk, replace its text with the plain fact it implies or with an empty-of-process
  equivalent; do not delete the array entry if that changes the structure — a removed
  string from a notes array is acceptable, a removed key is not.
- Go files: comments only. The code, directives (`//go:…`), build tags and string
  literals must not change by one byte; `gofmt -l .` must stay empty. Change only comments
  that contain process talk, study framing or the old name; leave every other comment
  alone. A comment you edit must still say the same thing about the code.

## Style

Follow `agent_work/08-book/objstore-p2-08-book-style.md` (the book's style sheet) for
vocabulary and tone. Plain, exact, present tense. Do not pad: where process talk is
removed, the text simply gets shorter. Do not restructure sections that have no finding.

## How to work

1. Read `agent_work/08-book/objstore-p2-08-book-style.md`.
2. Sweep every file in scope with a search first, then READ each file end to end — the
   search misses rephrased process talk. A starting pattern (case-insensitive):
   `learning project|testing project|agent|seat|orchestrat|chunk [0-9]|gate|landing|landed|owner|ruling|PLAN\.md|project-source|the build|OQ-[0-9]|wave|phase [0-9]|arbitrat|review|adversarial|exercise|study|decentralized|local-first|sync engine|CRDT|kickoff|brief`
3. Edit.
4. Check your own work:
   - `docker compose run --rm -T dev bash -c 'gofmt -l . ; go build ./... && go vet ./...'`
     (no output from gofmt, build and vet clean);
   - `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py .` ends with
     `site-lint: ok (0 findings)`;
   - `python3 -c "import json;json.load(open('docs/c4/model.json'))"`;
   - `git diff --stat` touches only files in scope plus your two output files;
   - rerun the search pattern and read every remaining hit: each must be one of the
     allowed technical uses above.
5. Write `agent_work/09-cleanup/PROSE-REPORT.md`: per file, what kind of text was removed
   or reworded; a list of every sentence you deleted outright (file, the sentence) with a
   note on where its technical content, if any, still stands; every remaining hit of the
   search pattern that you judged allowed, grouped by reason; anything you were unsure
   about and left for the orchestrator. Do not paste whole files.

Do not edit anything else: not `PLAN.md`, `STATE.md`, `docs/project-source.md`,
`docs/knowledge/`, `book/manifest.json`, `agent_work/` (except your two output files),
`kb/`, scripts, workflows, Go code.
