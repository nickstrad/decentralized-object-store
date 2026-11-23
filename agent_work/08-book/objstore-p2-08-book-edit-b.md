# Brief — publishing finish, PROSE: consistency edit of book chapters 5 to 8

Seat: Gemini 4 Neon, prose writer and editor (fresh). You are in a git worktree (branch
`chunk/08-book`); it is your current directory; work only here. You edit EXACTLY these
files and nothing else (other writers are editing the other chapters and the front matter
at the same time):
- `book/chapters/05-optimistic-concurrency-control.md`
- `book/chapters/06-sagas.md`
- `book/chapters/07-object-store-and-sync-engine.md`
- `book/chapters/08-end-to-end-and-hardening.md`
and, only if a diagram source must change, the matching `book/chapters/diagrams/NN-*.mmd`.
Do NOT run any git write command. Do not edit `book/manifest.json` or any `.go` file.

## Task

Read `agent_work/08-book/objstore-p2-08-book-style.md` first: it is the style sheet for the
whole book and it binds you. Then read ALL eight chapters under `book/chapters/` once, so
you know what the reader already knows when your chapters begin. Then edit your chapters
into the one shape, one vocabulary and the length range of the style sheet.

Work one chapter at a time, in order. For each chapter:
1. Check every code excerpt against the file it names (`grep`/read the file); correct an
   excerpt that drifted so it is an exact copy again, and keep it under about 20 lines.
2. Check every repository path, test name, fuzz target and command in the chapter exists
   (`ls`, `grep -rn "func TestName"`). Fix or remove what does not; list each in the report.
3. Do the edit. Keep every number, guarantee, narrowed claim and limit exactly as it is.
4. If a diagram source changed:
   `python3 /root/.claude/skills/draw-visual/scripts/render.py --update <chapter file>` and
   `DRAW_VISUAL_MAX_WIDTH=80 python3 /root/.claude/skills/draw-visual/scripts/render.py --check <chapter file>`
   (the tool path is for you only; never in a chapter).
5. `wc -w` before and after.

## Check

`docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` must report
nothing for your files. `git diff --stat` must show only your files.

## Report (write it to `agent_work/08-book/EDIT-REPORT-b.md`, the one extra file you create)

Per chapter: words before and after; headings renamed; excerpts corrected (file, what
drifted); paths or names fixed or removed; any fact on which two chapters disagree (quote
both, change neither); anything you could not verify.
