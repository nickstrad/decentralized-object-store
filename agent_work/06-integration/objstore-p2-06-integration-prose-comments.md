# Brief — Chunk 6 PROSE, part 1: code comments for integration: the object store and sync engine

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/06-integration`). You ADD COMMENTS to Go files. You change NO code. Do NOT run any git write
command: leave your edits in the working tree; they are committed for you under your
byline. Other writers may be working under `book/`, `README.md` and `docs/` at the same
time — do not touch those.

## Files
The product and test Go files that have no comments yet, in these packages:
`internal/node` (every file), `internal/noderuntime` (every file),
`internal/storage/httpapi` (every file), `internal/storage/api.go`,
`internal/testutil/leakcheck/leakcheck.go`, `internal/occ/node_merge.go` (only the code
this branch added: `git diff main..HEAD -- internal/occ/node_merge.go`), and
`cmd/objnode/main.go` with its tests. Also `cmd/objnode/help.txt`: it holds only bare flag
names, one per line; KEEP one flag per line, in the same order, and turn each line into
`<flag>  <one-line description>` (read `cmd/objnode/main.go` for what each flag does and
how the file is printed; run `docker compose run --rm -T dev go test ./cmd/objnode` after).
Do not touch files outside this list (comments elsewhere already exist).

## Source material (read first)
`agent_work/06-integration/build-notes.md` (the builder's account of intent, design choices,
claims, every round section, narrowed claims), `agent_work/06-integration/REVIEW-*.md`, `agent_work/06-integration/ARBITRATION-*.md`,
`agent_work/adversarial/06-integration/RESULTS.md`, and the plan item "Chunk 6" in PLAN.md 4.2 with
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
   - `docker compose run --rm -T dev go test ./internal/node/... ./internal/noderuntime/... ./internal/storage/... ./cmd/...` passes.
3. Self-contained: never mention this machine, absolute paths, agents, models, reviews,
   briefs, chunk numbers or how the text was produced; no file under `agent_work/`.
4. Accuracy over coverage: never state a guarantee the tests do not show.

## Points the comments must get right
- When a version is declared lost (`internal/node/inventory.go`; build notes section 14):
  state the rule exactly as section 14 does — absence records, written only after every
  other holder of the stripe answered; more than m of them for one stripe; an unreachable
  holder blocks the decision; the loss record is written before the tombstone — and its
  "Residual limit" in its stated form. Nothing stronger.
- Last-writer-wins: concurrent versions are never kept side by side; every discard is
  counted and logged.
- Another process (a test run) is reading this tree while you work: never leave a file
  in a state that does not compile.
