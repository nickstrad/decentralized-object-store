# Brief — Chunk 1 PROSE, part 3: README and site front page status

Seat: Gemini 4 Neon, prose writer (fresh). Worktree: the current directory. Edit exactly
two files, `README.md` and `docs/index.md`. Do NOT run any git write command.

What changed: SWIM gossip membership has landed in `internal/gossip`, with its book chapter
`book/chapters/02-swim-gossip.md` ("SWIM Gossip Membership"). Read `agent_work/01-gossip/build-notes.md` (first
sections) for what it does, and `book/manifest.json` plus `book/chapters/` for which
chapters exist. Read the plan's chunk list (PLAN.md 4.2 headings) for what is still planned.

Edits, in the style and length already there: in `README.md` the status paragraph and the
status table (this building block becomes Built, with its scope in a few words; mark as
"Planned (next)" nothing unless you can tell from the table which block is not Built and
comes first), and the "Where to read" book bullet (list the published chapters with links
`book/chapters/<file>.md`); in `docs/index.md` the status paragraph and the Book bullet
(links as `../book/chapters/<file>.md`). Do not invent commands; add none unless
`agent_work/01-gossip/build-notes.md` records one a reader can run through
`docker compose run --rm dev ...`. Change nothing else. No mention of this machine, agents,
models or briefs. Then run
`docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` — the only
acceptable finding is `chapter-unlisted` for a chapter not yet in the manifest.

## Note on order
Building blocks land as they pass their gates, not in numeric order. Built so far, with
published chapters: the simulator (chapter 1), erasure coding (chapter 3), sagas
(chapter 6) and now SWIM gossip membership (chapter 2). Merkle anti-entropy and OCC
metadata are still planned and their chapters are not published — do not link them; no
row should say "(next)" unless the table makes it obvious. Another writer is editing
files under `docs/c4/`, `docs/knowledge/`, `agent_work/` and `book/manifest.json` at the
same time: do not touch them.
