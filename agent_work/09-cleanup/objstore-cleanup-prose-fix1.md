# Brief — prose cleanup, fix round 1 (prose seat)

Work only in the current directory (git worktree, branch `cleanup/object-store`). The
working tree already holds the uncommitted edits of the prose cleanup pass; build on them.
Do NOT commit, do not revert anything, do not push.

Read first: `agent_work/09-cleanup/objstore-cleanup-prose.md` (the pass's rules, all still
in force — verbiage only, no number or claim changed, fenced blocks byte for byte) and
`agent_work/09-cleanup/REVIEW-1.md` (the review; verdict NITS). Close its four nits and
nothing else:

1. `README.md`, section "What is built": present the table as what the repository
   contains. Remove the constant "Status" column (every cell says "Built"). Give the
   section a heading that is not about build progress. Keep every row and every word of
   the "Scope" cells; you may rename the first column's header and a row label only where
   the label is itself build-progress wording. If any other heading or link in the
   repository points at the old heading's anchor, update it (grep for
   `#what-is-built`).
2. `docs/c4/README.md`, the first table: remove the constant "State" column (every cell
   says "built"). Keep every row, the "Part" text and the "Diagrams" cells.
3. `docs/c4/model.json`, `sources[0]` (id `plan`): set `url` to
   `https://nickstrad.github.io/object-store/book/` and write a `title` and `notes` that
   describe the book as the reference for these views. Do not change the `id`.
4. `docs/c4/model.json`: remove the trailing " — as built" (written `—` in the file)
   from the top-level `title` and from the nine diagram `title` values. Leave `revision`
   and everything else alone. Keep the file's formatting so the diff shows only these
   strings.

Then check: `python3 -c "import json;json.load(open('docs/c4/model.json'))"`;
`docker compose run --rm -T dev python3 agent_work/gates/site_lint.py .` ends with
`site-lint: ok (0 findings)`; `agent_work/09-cleanup/check-prose.py HEAD` prints no line
containing FINDING other than, for model.json, the `url` of `sources[0]` and the title
values you were told to change. Append a short "Fix round 1" section to
`agent_work/09-cleanup/PROSE-REPORT.md` saying what changed for each nit.
