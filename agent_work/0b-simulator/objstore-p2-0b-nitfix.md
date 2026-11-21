# Brief — Chunk 0b, one-line nit fix: `scaled(k, -1)` in the simulator

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/0b-simulator
(branch `chunk/0b-simulator`). Binding rules: `agent_work/0b-simulator/objstore-p2-builder-rules.md`
(no comments in Go, Docker only, test first, commit here, never push). Another agent is
writing files under `book/` in this worktree at the same time: do not touch or `git add`
anything under `book/`; stage only the files you change, by path.

Read `agent_work/0b-simulator/REVIEW-3.md`, "New findings" 1: in `internal/sim/clock.go`
the overflow guard of `scaled(k, d)` computes `math.MinInt64 / int64(d)`, which itself
overflows for `d == -1`, so `scaled(k, -1)` returns `math.MinInt64` instead of `-k`
(reachable through `ScheduleFromBytes` with a step of -1).

Do exactly this:
1. Add a regression test FIRST (in `internal/sim/bounds_test.go` or next to the existing
   `scaled` tests) that fails on the current code: `scaled(k, -1) == -k` for a few `k`
   including 1 and `math.MaxInt64`, the reviewer's `ScheduleFromBytes` reproduction, and
   the other boundaries of `scaled` (d = 0, 1, -2, MinInt64; k = 0) so the guard is pinned
   on both sides.
2. Apply the smallest fix (the reviewer's, or an equivalent you can prove correct). No
   exported change. Golden digests must not move.
3. Through Docker: `gofmt -l .` empty, `go vet ./...`, `go test ./...`,
   `go test ./internal/sim -rapid.checks=2000`. Then commit the test and the fix (one or
   two commits, by explicit path), and append three lines to the "Post-adversarial round"
   section of `agent_work/0b-simulator/build-notes.md` under a heading "REVIEW-3 nit"
   (what, test name, fix) and commit that.
Final message: commit list and one line. Do not run the full gate; the orchestrator does.
