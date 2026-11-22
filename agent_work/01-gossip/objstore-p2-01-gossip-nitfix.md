# Brief — Chunk 1, nit fix after review 2 (SWIM gossip membership)

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/01-gossip`).
Binding rules: `agent_work/01-gossip/objstore-p2-builder-rules.md` (no comments in Go, Docker
only, test first, commit here, never push). Another agent is writing files under `book/`
in this worktree at the same time: do not touch or `git add` anything under `book/`;
stage only the files you change, by explicit path (no `git add -A`, no `git commit -a`).

Read `agent_work/01-gossip/REVIEW-2.md`, "New findings" 1, 2 and 3. Do exactly this:
1. Finding 1 — the `FuzzViewMergeLaws` seed corpus must exercise non-empty views under
   plain `go test`: replace seeds so that at least two draw five or more updates, and add
   a cheap test that fails if every corpus seed draws an empty update list.
2. Finding 2 — qualify the "cannot cycle" sentence in
   `agent_work/01-gossip/build-notes.md` with the tombstone bound, as the reviewer says.
3. Finding 3 (NOTE) — make `View.Apply` after `View.Remove` unable to accumulate duplicate
   IDs for any caller (a test first that alternates Apply/Remove without an ordered read
   and checks the internal growth bound through an exported observation or `export_test`
   hook); keep the change minimal and the merge laws untouched.
4. Through Docker: `gofmt -l .` empty, `go vet ./...`, `go test ./internal/gossip/...`,
   the same with `-race`, and with `-rapid.checks=2000`. Commit tests and fixes (by path),
   append a short "REVIEW-2 nits" section to the build notes and commit that.
Final message: commit list and one line. Do not run the full gate; the orchestrator does.
