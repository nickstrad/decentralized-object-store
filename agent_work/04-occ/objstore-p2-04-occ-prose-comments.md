# Brief — Chunk 4 PROSE, part 1: code comments for optimistic concurrency control for metadata

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/04-occ`). You ADD COMMENTS to Go files. You change NO code. Do NOT run any git write
command: leave your edits in the working tree; they are committed for you under your
byline. Other writers may be working under `book/`, `README.md` and `docs/` at the same
time — do not touch those.

## Files
Every `.go` file under `internal/occ` (sub-packages and test files included), plus any
`cmd/*/help.txt` this chunk created. The code was written with no comments on purpose.
Do not touch files outside these packages (comments elsewhere already exist).

## Source material (read first)
`agent_work/04-occ/build-notes.md` (the builder's account of intent, design choices,
claims, every round section, narrowed claims), `agent_work/04-occ/REVIEW-*.md`,
`agent_work/adversarial/04-occ/RESULTS.md`, and the plan item "Chunk 4" in PLAN.md 4.2 with
the section 7 decisions it cites. Existing commented packages (`internal/sim`,
`internal/env`) show the house style. Where a claim was narrowed, comments state the
narrowed form.

## What to write
- A package comment above the `package` clause of each package's main file: what it is,
  how it fits, the one or two rules a user must know.
- A doc comment on every exported identifier, starting with its name: what it does, what
  the caller may rely on, what it returns on failure. One to four lines is typical.
- Inside functions: only where the code alone does not say WHY — an invariant, an ordering
  that matters, a protocol rule and where it comes from, a subtle boundary.
- Unexported helpers: one line when the name is not enough.
- Test files: one or two lines above each test function on what it proves; for a canary,
  what bug it plants.
Plain, concrete English; short sentences; no hype. Concise above all.

## Hard rules (checked mechanically; a violation discards the whole pass)
1. Comments only. Every line you add or change in a `.go` file is a FULL-LINE `//` comment
   (or a blank line next to one). No end-of-line comments after code, no `/* */`, no edits
   to code, strings, identifiers, imports, build tags or directives; do not move or reflow
   code; no comment starting with `//go:` or `// Code generated`.
2. After editing, run and fix until all pass:
   - `docker compose run --rm -T dev gofmt -l .` prints nothing;
   - `docker compose run --rm -T dev go build ./...` and `go vet ./...` succeed;
   - `git diff -U0 -- '*.go' | grep '^[+-]' | grep -v '^+++\|^---' | grep -vE '^[+-][[:space:]]*(//.*)?$'`
     prints NOTHING;
   - `docker compose run --rm -T dev go test ./internal/occ/...` passes.
3. Self-contained: never mention this machine, absolute paths, agents, models, reviews,
   briefs, chunk numbers or how the text was produced; no file under `agent_work/`.
4. Accuracy over coverage: never state a guarantee the tests do not show.

Design note: the package is last-writer-wins with one version per key; no comment may
describe sibling versions or conflict sets as something the code keeps. Another writer
is working on the chapter under `book/`: do not touch `book/`.
