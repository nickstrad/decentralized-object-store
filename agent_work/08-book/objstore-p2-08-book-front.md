# Brief — publishing finish, PROSE: front matter, final README and site front page

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/08-book`); it is your current directory; work only here. You create or edit EXACTLY:
- `book/front/preface.md` (new; first line exactly `# Preface`)
- `book/front/topic-map.md` (new; first line exactly `# Topic Map`)
- `README.md`
- `docs/index.md`
Two other writers are editing the chapters under `book/chapters/` at the same time: read
them, do not edit them. Do NOT run any git write command. Do not edit `book/manifest.json`
(the orchestrator lists your two files there) or any `.go` file.

Read first: `agent_work/08-book/objstore-p2-08-book-style.md` (vocabulary and rules bind
you), all eight chapters, the current `README.md` and `docs/index.md`, `compose.yaml`,
`agent_work/gates/gate.sh` (header), `agent_work/gates/ci-fast.sh` (header),
`cmd/objnode/help.txt`, `cmd/objsim`, and `agent_work/07-e2e/build-notes.md` (replay
commands, the hardening scripts under `agent_work/07-e2e/hardening/`).

## Docker safety (read before running any command)

`compose.yaml` names the compose project, so every checkout on this machine shares ONE
project: a `docker compose down` here would stop other people's containers (a test gate may
be running). Before you run anything, in every shell: `export COMPOSE_PROJECT_NAME=objstore-front`.
With that set, run the README's commands exactly as a reader would type them (the README
itself must NOT mention this variable). Never pass `--volumes` or `-v` to `down`, and never
run `docker system prune`, `docker volume rm` or `docker rm` on containers you did not start.

## 1. Preface (300–500 words)

What the project is (a leaderless, multi-region object store in Go on the standard library,
built testing-first as a learning project); what the book is for (learn each topic, then
find it in the code); how to read it next to the repository (each chapter: the idea, how
it maps to the code, a few excerpts, what the tests show and do not, where to go deeper);
the order of the chapters and why the simulator comes first; what the project is NOT (not
production software; the stated limits are in each chapter). No history of how it was built.

## 2. Topic map (a table and almost nothing else)

One row per topic, columns: Topic | Chapter (relative link `../chapters/NN-<slug>.md`) |
Package(s) | Main tests (two to four exact test or fuzz-target names per row, each checked
with `grep -rn "func <Name>"`) . Rows, in book order: deterministic simulation; SWIM gossip
membership; Reed-Solomon erasure coding and the chunk store; Merkle-tree anti-entropy;
optimistic concurrency control and last-writer-wins; sagas and bucket migration; the
integrated node, real runtime and object API; end-to-end scenarios, nemesis cycles and
fuzz hardening. One sentence above the table saying how to use it.

## 3. README.md — final form (aim for 600–900 words; it is shorter than today's)

The README's job now: tell a visitor what this is in one paragraph and send them to the
book as THE way to learn the project. Sections, in this order:
1. Title and the one-paragraph description (keep today's facts: standard library only,
   rapid test-only, last-writer-wins with every discard counted and logged).
2. `## Read the book` — the live site URL
   `https://nickstrad.github.io/decentralized-object-store/`, one sentence on what the book
   is, and the chapter list (eight chapters, relative links `book/chapters/<file>.md`),
   plus the preface and topic map.
3. `## What is built` — the status table with every row Built (nothing is "Planned" any
   more; the "Finished book" row becomes built; no "next"). One short line per row.
4. `## Run the tests through Docker` — build the image; the whole test suite; the fast lane
   (`agent_work/gates/ci-fast.sh`) and the full gate (`GATE_STAGE=final
   agent_work/gates/gate.sh <name>`) with a one-line statement of how long each takes, taken
   from the notes; the gate list G0–G7 including the real-process step, as the gate script
   has it TODAY (read the script; today's README says G0–G6, which is stale).
5. `## Replay a seed` — how a failing simulation prints a REPLAY command and one real,
   working example for a single seed (take it from the end-to-end chapter or the notes, run
   it, confirm it passes); the `objsim` example from today's README stays.
6. `## Run a local cluster` — the three-node Docker cluster profile from `compose.yaml`:
   start it, PUT an object through one node, GET it from another, stop it. Work the exact
   commands out from `compose.yaml`, `cmd/objnode/help.txt` and the object API routes, RUN
   them, and paste only what worked.
7. `## Where things are` — book, C4 diagrams (`docs/c4/README.md`; they are as-built now),
   plan, specification. Four bullets.

EVERY command in a ```sh block of the README will be executed by a script on a clean
clone, top to bottom, in one shell at the repository root. So: run each one yourself
first; no placeholders such as `<name>` inside ```sh blocks (use a real value, e.g. `demo`);
anything that takes longer than about five minutes (the full gate, the canary suite, fuzz
budgets, the large sweep) goes in a block fenced as ```text instead, so it is shown but
not executed; the cluster section must stop the cluster in its last command.

## 4. docs/index.md — the site front page (250–400 words)

The site presents the book as its main content. Front page: the one-paragraph description;
"Start here" pointing to the preface and the topic map (links `../book/front/preface.md`,
`../book/front/topic-map.md`) and listing the eight chapters (`../book/chapters/<file>.md`);
one line each for the C4 diagrams (`c4/README.md`, as built), the plan and the README;
the three Docker commands already there. Nothing "planned" remains.

## Check

`docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` — the only
acceptable findings are `chapter-unlisted`-style findings for your two new front files
until the orchestrator lists them. `git status --short` must show only your four files.
Write `agent_work/08-book/FRONT-REPORT.md` (the one extra file you create): every README
command with the result and time of your own run; every test name in the topic map with
the file it is in; anything you could not verify.

## Addendum (2026-11-23, after the chapter edit)

The chapter editors have finished: the eight chapters are final and committed. No test
gate is running now, but keep `COMPOSE_PROJECT_NAME=objstore-front` set all the same.
The end-to-end work has landed on main and this branch is rebased on it.

One extra edit is yours, and only this one, in `book/chapters/01-deterministic-simulation.md`:
the sentence that says `detlint` checks "... and future protocol packages — 15 files in 3
packages today" is stale now that every package is built. Run the lint as the gate script
runs it (see `agent_work/gates/gate.sh`, step `detlint`), read the file and package counts
it prints, and rewrite that one sentence with today's counts and no "future" or "today".
Keep the chapter within 2,600 words. `git status --short` will then show five files plus
your report. Record the lint output line in FRONT-REPORT.md.
