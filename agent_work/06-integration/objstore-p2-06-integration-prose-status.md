# Brief — Chunk 6 PROSE, part 3: README and site front page status

Seat: Gemini 4 Neon, prose writer (fresh). Worktree: the current directory. Edit exactly
two files, `README.md` and `docs/index.md`. Do NOT run any git write command.

What changed: integration: the object store and sync engine has landed in `internal/node`, the HTTP object API in `internal/storage`, and `cmd/objnode`, with its book chapter
`book/chapters/07-object-store-and-sync-engine.md` ("The Object Store and Sync Engine"). Read `agent_work/06-integration/build-notes.md` (first
sections) for what it does, and `book/manifest.json` plus `book/chapters/` for which
chapters exist. Read the plan's chunk list (PLAN.md 4.2 headings) for what is still planned.

Edits, in the style and length already there: in `README.md` the status paragraph and the
status table (this building block becomes Built, with its scope in a few words; mark as
"Planned (next)" nothing unless you can tell from the table which block is not Built and
comes first), and the "Where to read" book bullet (list the published chapters with links
`book/chapters/<file>.md`); in `docs/index.md` the status paragraph and the Book bullet
(links as `../book/chapters/<file>.md`). Do not invent commands; add none unless
`agent_work/06-integration/build-notes.md` records one a reader can run through
`docker compose run --rm dev ...`. Change nothing else. No mention of this machine, agents,
models or briefs. Then run
`docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` — the only
acceptable finding is `chapter-unlisted` for a chapter not yet in the manifest.
