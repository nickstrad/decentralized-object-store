# Brief — Chunk 2, two-nit fix after review 2 (erasure coding and chunk store)

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/02-erasure`).
Binding rules: `agent_work/02-erasure/objstore-p2-builder-rules.md` (no comments in Go,
Docker only, test first, commit here, never push). Stage only the files you change, by
path.

Read `agent_work/02-erasure/REVIEW-2.md`, "New findings" 1, 2 and 3. Do exactly this:
1. Finding 1 — a regression test FIRST that fails on the current code: a PUT whose first
   stripe cannot be launched (too few members) leaves `Stats().PutAwaitingPeak == 0`;
   then the smallest fix (launch before counting, or equivalent you can prove). Check the
   GET side for the same pattern and fix it the same way if it is there.
2. Finding 2 — correct the byte approximation in the C30 passage of
   `agent_work/02-erasure/build-notes.md` to match the exact formula.
3. Finding 3 — in the build notes' repair-round narrative, replace commit SHAs with
   commit subjects (SHAs change when the branch is rebased; subjects do not).
4. Through Docker: `gofmt -l .` empty, `go vet ./...`, `go test ./internal/erasure/...
   ./internal/storage/...`, the same with `-rapid.checks=2000`. Commit test and fix (one
   or two commits), append a short "REVIEW-2 nits" section to the build notes (what,
   test name, fix) and commit that.
Final message: commit list and one line. Do not run the full gate; the orchestrator does.
