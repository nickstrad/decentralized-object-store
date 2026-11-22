# Brief — Chunk 4 PROSE, part 3: README and site front page status

Seat: Gemini 4 Neon, prose writer (fresh). Worktree: the current directory. Edit exactly
two files, `README.md` and `docs/index.md`. Do NOT run any git write command.

What changed: optimistic concurrency control for metadata has landed in `internal/occ`, with its book chapter
`book/chapters/05-optimistic-concurrency-control.md` ("Optimistic Concurrency Control"). Read `agent_work/04-occ/build-notes.md` (first
sections) for what it does, and `book/manifest.json` plus `book/chapters/` for which
chapters exist. Read the plan's chunk list (PLAN.md 4.2 headings) for what is still planned.

Edits, in the style and length already there: in `README.md` the status paragraph and the
status table (this building block becomes Built, with its scope in a few words; mark as
"Planned (next)" nothing unless you can tell from the table which block is not Built and
comes first), and the "Where to read" book bullet (list the published chapters with links
`book/chapters/<file>.md`); in `docs/index.md` the status paragraph and the Book bullet
(links as `../book/chapters/<file>.md`). Do not invent commands; add none unless
`agent_work/04-occ/build-notes.md` records one a reader can run through
`docker compose run --rm dev ...`. Change nothing else. No mention of this machine, agents,
models or briefs. Then run
`docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` — the only
acceptable finding is `chapter-unlisted` for a chapter not yet in the manifest.

## Notes
- With this landing every protocol building block is built: the simulator (chapter 1),
  SWIM gossip (2), erasure coding (3), Merkle anti-entropy (4), OCC metadata (5, this
  one) and sagas (6). What remains planned: full-cluster integration (the node, the HTTP
  object API), the end-to-end suite, and the finished book.
- Describe this building block as it is: conditional writes (ETags, If-Match /
  If-None-Match), version vectors to detect concurrency, and last-writer-wins for
  concurrent writes with every discarded write counted and logged. The word "sibling"
  must not appear. Keep the README's existing opening sentence about last-writer-wins
  true and unchanged unless it is now wrong.
- If either file links to `docs/c4/07-component-crdt.md`, the file is being renamed to
  `docs/c4/07-component-occ.md` by another writer: update the link text and target.
- Another writer is editing files under `docs/c4/`, `docs/knowledge/`, `agent_work/` and
  `book/manifest.json` at the same time: do not touch them.
