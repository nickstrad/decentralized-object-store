# Brief — Chunk 0b PROSE, part 1: code comments and CLI help for the simulator

Seat: Gemini 4 Neon, prose writer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0b-simulator (branch `chunk/0b-simulator`).
You ADD COMMENTS to Go files and write one help file. You change NO code. Do NOT run any
git write command (no add, no commit): leave your edits in the working tree; they are
committed for you under your byline. Another writer is working on the book chapter in
`book/` at the same time — do not touch `book/`.

## Files
Every `.go` file under `internal/env/`, `internal/sim/` (including its sub-packages and
`simtest/`) and `cmd/objsim/`, test files included; plus `cmd/objsim/help.txt`.
The code was written with no comments on purpose; every comment in it will be yours.

## Source material (read first)
- `agent_work/0b-simulator/build-notes.md` — the builder's account of intent: what each
  part does and why, the contract later builders rely on ("Semantics Wave B builders must
  know"), the repair rounds, and narrowed claims. This is your main source for WHY.
- `agent_work/0b-simulator/REVIEW-1.md`, `REVIEW-2.md`, `REVIEW-3.md` and
  `agent_work/adversarial/0b-simulator/RESULTS.md` — what was found, fixed and narrowed.
  Where a claim was narrowed, the comment states the narrowed form.
- `PLAN.md` section 2.1 (what the simulator is for) and 7.1.

## What to write
- A package comment for each package (in a `doc.go` you may NOT create — put it above the
  `package` clause of the package's main file): what the package is, how it fits, the one
  or two rules a user must know.
- A doc comment on every exported type, function, method, constant, variable and sentinel
  error: what it does, what the caller may rely on, what it returns on failure. Start with
  the identifier's name, as Go convention asks. One to four lines is typical.
- Inside functions: a comment only where the code alone does not say WHY — an invariant,
  an ordering that matters for determinism, a fault's exact semantics, a subtle boundary.
  Do not narrate what the next line obviously does.
- Unexported helpers: one line when the name is not enough.
- Test files: one or two lines above each test function saying what it proves and, for a
  canary, what bug it plants. Table rows need no comments.
- `cmd/objsim/help.txt`: the command's help text. Keep every flag name that is in the file
  now, each on its own line starting with the flag exactly as written there, followed by a
  short description; add a two-line summary at the top and one usage example at the bottom
  (`objsim -scenario toy-chaos-100 -seed 3`). Check the flags against `cmd/objsim/main.go`.
Plain, concrete English. Short sentences. No hype, no "simply", no "robust". Concise above
all: a reader should learn the design from the comments without wading.

## Hard rules (checked mechanically; a violation discards the whole pass)
1. Comments only. Every line you add or change in a `.go` file is a FULL-LINE `//` comment
   (or a blank line next to one). No end-of-line comments after code, no `/* */` blocks, no
   edits to code, strings, identifiers, imports, build tags or directives. Do not move or
   reflow code. Do not add a comment that starts with `//go:` or `// Code generated`.
2. After editing, run and fix until all pass:
   - `docker compose run --rm -T dev gofmt -l .` prints nothing;
   - `docker compose run --rm -T dev go build ./...` and `go vet ./...` succeed;
   - `git diff -U0 -- '*.go' | grep '^[+-]' | grep -v '^+++\|^---' | grep -vE '^[+-][[:space:]]*(//.*)?$'`
     prints NOTHING (every changed line is a comment or blank);
   - `docker compose run --rm -T dev go test ./internal/... ./cmd/...` passes (a comment
     placed where Go reads it as an example's `// Output:` or a directive would break it).
3. Self-contained: never mention this machine, absolute paths, agents, models, reviews,
   briefs, chunk numbers or how the text was produced. Refer to no file under
   `agent_work/`. Say "the simulator", "the gate", "the plan" only as a reader of the
   repository would understand them.
4. Accuracy over coverage: if you are not sure what something guarantees, read the code
   and its tests; never state a guarantee the tests do not show.
