# Brief — Chunk 5, two-nit fix after review 2 (sagas)

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/05-saga`).
Binding rules: `agent_work/05-saga/objstore-p2-builder-rules.md` (no comments in Go, Docker
only, test first, commit here, never push). Another agent is writing files under `book/`
in this worktree at the same time: do not touch or `git add` anything under `book/`;
stage only the files you change, by explicit path (no `git add -A`, no `git commit -a`).

Read `agent_work/05-saga/REVIEW-2.md`, "New findings" 1 and 2. Do exactly this:
1. Finding 1 — a regression test FIRST that fails on the current code (a store seeded
   with more than 1,000 objects: overwriting an existing key replaces it, never
   duplicates it, and the bound check is exact); then the fix. Make the sortedness the
   store relies on something the store itself guarantees (sort or validate on load and on
   every path that builds the list), not a property of one key-formatting helper; fix the
   helper too. Correct the build-notes sentence the reviewer cites.
2. Finding 2 — a per-invocation local report in `NemesisRun` and `DuelRun`; a test that
   runs one returned closure from several goroutines under `-race` (test files may use
   goroutines) or, if the determinism lint forbids that in this package's tests, a test
   that two sequential invocations with a nil report do not share state — say which.
3. Through Docker: `gofmt -l .` empty, `go vet ./...`, `go test ./internal/saga/...`, the
   same with `-race`, and with `-rapid.checks=2000`. Commit test and fix (by path), append
   a short "REVIEW-2 nits" section to `agent_work/05-saga/build-notes.md` (what, test
   name, fix) and commit that.
Final message: commit list and one line. Do not run the full gate; the orchestrator does.
