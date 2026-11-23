# Brief — publishing finish, PROSE FIX ROUND: close the independent check's findings

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/08-book`); it is your current directory; work only here. Do NOT run any git write
command. Do not edit any `.go` file, `book/manifest.json`, or anything under `agent_work/`
except the one report named at the end.

Read first: `agent_work/08-book/objstore-p2-08-book-style.md` (vocabulary and rules bind
you) and `agent_work/adversarial/08-book/FINDINGS.md` (the findings; evidence files are
beside it).

## Docker safety

In every shell, before anything else: `export COMPOSE_PROJECT_NAME=objstore-fix`. Never pass
`--volumes` or `-v` to `down`; never run `docker system prune`, `docker volume rm`, or
`docker rm`/`docker kill` on containers you did not start.

## What to do

For EACH of B01–B13: open the cited source line yourself and confirm the finding. If it is
right, make the smallest rewording that makes the sentence true (the finding's last column
is a suggestion; keep the chapter's voice and the style sheet). If you think a finding is
wrong, do not change the sentence; say why in the report, with file and line. Rules:
- No number may be invented. B04 and B05: drop the execution counts that no kept record
  supports. B13: state what the fuzz target covers and where multi-megabyte payloads are
  covered instead.
- B06 and B08 are false guarantees and matter most. B08: state the guarantee the tests
  actually support (damage within the budget after writes have settled on all shards) and
  say plainly that a PUT acknowledged at quorum may have fewer than k+m shards written, with
  a link to the chapter 7 passage that already says so.
- B12 also applies to `README.md` (the sentence in "Replay a seed"): make both true.
- Every chapter stays within 1,800–2,600 words (`wc -w`); cut elsewhere in the same
  chapter if a correction adds words. Code excerpts stay exact.

README (part 1 of the findings):
- State the host tools the README's own commands need besides Docker with Compose v2
  (`git`, `curl`, a POSIX shell); say the full gate also needs `jq` on the host, next to
  its `text` block.
- Bound both `until curl ...` loops in the cluster section (for example at most 60 tries
  one second apart, then print the node's health output or `docker compose ... logs` tail
  and exit nonzero). Run the whole section afterwards and make sure it still ends by
  stopping the cluster.
- Add ONE sentence to the cluster section: `compose.yaml` fixes the project name, so a
  second checkout on the same machine shares containers with the first; a reader who has
  two sets `COMPOSE_PROJECT_NAME` to a different value in one of them. Do not put the
  variable into any command block.
- README stays within 600–950 words. Every ```sh block must still pass when run top to
  bottom in one shell on a clean clone.

## Check

- `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` → 0 findings.
- Run every ```sh block of README.md yourself, top to bottom.
- `git status --short` shows only: the chapters you changed, `README.md`, and your report.
- Write `agent_work/08-book/FIX1-REPORT.md`: one row per finding (B01–B13 and the three
  README findings): confirmed or disputed, the evidence line you read, the old sentence,
  the new sentence; then `wc -w` per changed file.
