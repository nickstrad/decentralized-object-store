# Brief — Chunk 0a PROSE, one correction to the README

Seat: Gemini 4 Neon, prose writer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0a-foundation (branch `chunk/0a-foundation`).
Edit ONE file, `README.md`, and commit it alone (`git add README.md`) with the subject
`docs: say which gate stage runs where in the README` and, after a blank line, the LAST
line exactly `Prose-By: Gemini 4 Neon`. Do not push. Touch nothing else.

## What changed in the tooling (verified)
`agent_work/gates/gate.sh <name>` has two stages, chosen with the `GATE_STAGE` variable:
- the default, `build`, is for a chunk branch before its comments are written. It requires
  product Go to have no comments beyond those in the commit the branch started from. It
  compares the branch with that commit, so it refuses to run on `main` itself (exit 2, with
  a message) — a commit cannot be checked against itself.
- `GATE_STAGE=final` runs the same gates G0–G6 without that comment comparison. This is the
  form to use on `main` or on any finished tree:
  `GATE_STAGE=final agent_work/gates/gate.sh <name>`.
`<name>` is only a label: results are written to `agent_work/<name>/gates/result.json`.
The script is run on the host; it builds the image and re-executes itself inside the
container.

## The edit
In the section "Landing gates and canaries" of `README.md`, the text currently says to run
`agent_work/gates/gate.sh <name>` "inside the container". Correct it, briefly: the command
is run from the repository root on the host and re-runs itself inside the container; show
`GATE_STAGE=final agent_work/gates/gate.sh <name>` as the command for a checkout of `main`,
and say in one or two sentences what the default `build` stage is for and why it stops on
`main`. Keep everything else in the README as it is. Plain, short sentences. No mention of
this machine, agents, models or briefs.

Then run `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` (it
must report no findings) and commit as described.
