# Brief — publishing finish, independent check: follow the README on a clean clone; test the book's claims against the evidence

Seat: Astra (gpt-6-astra), independent tester (fresh). You are in a git worktree (branch
`chunk/08-book`); it is your current directory. You write ONLY under
`agent_work/adversarial/08-book/`. Do not edit anything else, do not commit, no git write
commands in the worktree. Everything of the project runs through Docker
(`docker compose ...`); the machine has 4 cores and 7 GB and is shared — one heavy run at
a time.

## Part 1 — a new reader follows the README

1. Make a clean clone of the worktree's HEAD in a fresh directory under /tmp
   (`git clone --no-hardlinks . /tmp/<dir>`), and work there as a reader who has only
   Docker, git and a shell.
2. Follow `README.md` top to bottom, literally. Run every command in every ```sh block in
   the order given. For each: the command, exit status, wall time, and whether the output
   is what the surrounding text says it will be.
3. Report every step that fails, hangs, needs something the README did not say, prints
   something the text contradicts, or is unclear to a reader who does not know the code
   (say what you had to guess). Commands shown in ```text blocks are long runs: do not run
   them, but check that each names a script and options that exist.
4. Do the same for the commands on `docs/index.md` and for every command quoted in
   `book/front/preface.md` and in each chapter's "Go deeper in the repo" section (each
   chapter: at least the first two commands; skip any the chapter says takes more than
   five minutes).
5. Save your steps as a script, `agent_work/adversarial/08-book/readme-walk.sh`, that
   repeats part 1 from a clean clone and prints one PASS/FAIL line per command.
6. Stop any cluster you started; remove your clone.

## Part 2 — claims the evidence does not support

For each of the eight chapters in `book/chapters/` and for `book/front/topic-map.md`:
1. List every checkable claim: numbers (seeds, node counts, times, counts of checks),
   guarantees ("never", "always", "at most"), named tests, fuzz targets, files, and
   statements about what a test proves.
2. Check each against the evidence in the repository: the code and tests themselves, the
   notes `agent_work/<chunk>/build-notes.md`, the reviews, and
   `agent_work/adversarial/*/RESULTS.md`. A named test must exist and must assert what the
   chapter says it does (read it). An excerpt must match its file exactly.
3. Report, one row each, every claim that is (a) contradicted by the evidence, (b) stronger
   than the evidence (for example "any" where the test covers one case, or a narrowed
   claim stated in its un-narrowed form), (c) unsupported — nothing found, or (d) stale —
   true of an earlier state of the code. Give the chapter, the sentence, the evidence
   path and line, and the smallest rewording that would make it true.
4. Also report contradictions BETWEEN chapters, and any place the book describes siblings
   or CRDTs as this project's design (the design is last-writer-wins with every discarded
   write counted and logged).

## Output

`agent_work/adversarial/08-book/FINDINGS.md`: first line `README: <n> steps, <m> failed or
unclear; BOOK: <p> claims checked, <q> not supported`. Then two tables (part 1, part 2),
each row with a severity: BLOCKER (a reader cannot continue, or a false guarantee),
MAJOR (wrong or misleading), MINOR (unclear, stale name, cosmetic). Only report what you
ran or read; say what you did not get to.
