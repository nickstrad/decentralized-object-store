# Brief — Chunk 6, structure fix: move the HTTP server out of `internal/storage`

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/06-integration`).
Binding rules: `agent_work/06-integration/objstore-p2-builder-rules.md` (no comments in Go,
Docker only, commit here, never push). Stage only the files you change, by explicit path.

## Why
`internal/node` (the protocol core the simulator executes) has just been added to the
determinism lint's roots. The lint checks every root package AND every module package a
root imports. `internal/node` imports `internal/storage`, and `internal/storage/http.go`
is the HTTP server: `net/http`, `context` deadlines, `sync.Mutex`, `sync.WaitGroup`,
goroutines. So the lint now fails on the storage package:
`docker compose run --rm -T dev bash -c 'GATE_ONLY=G0 GATE_NO_DOCKER=1 agent_work/gates/gate.sh x'`
is not the way to see it (the gate needs its host wrapper); run the lint directly the way
`g0_detlint` in `agent_work/gates/gate.sh` does, or simply run
`agent_work/gates/canaries.sh '^partial-run$'` from the host — it must end "1 of 1 caught"
when you are done (today it is MISSED because G0 detlint is red on `internal/storage/http.go`).

## What to do (a move, not a redesign; no behaviour change)
1. Move the HTTP server — `internal/storage/http.go`, its tests, and whatever else in
   `internal/storage` needs `net/http`, `context` deadlines, goroutines or locks — into a
   new package `internal/storage/httpapi`. Everything the protocol core imports from
   `internal/storage` (chunk store, shard format, striping, manifest, placement, the
   striped node, and the pure parts of the object API such as the status table and
   request/response types if `internal/node` uses them) stays in `internal/storage` and
   must pass the determinism lint with NO allowlist entry. Decide file by file where
   `api.go` and its tests belong by that rule; say what went where in the notes.
2. Update every importer (`internal/noderuntime`, `cmd/objnode`, tests, fuzz targets and
   their `testdata/fuzz` corpus directories — a corpus directory is named after the
   package path and the target, so move it with the target).
3. Keep every test, fuzz target, canary and claim. No test may be weakened or deleted;
   test names stay the same.
4. Through Docker: `gofmt -l .` empty; `go vet ./...`; `go build ./...`;
   `go test ./internal/storage/... ./internal/node/... ./internal/noderuntime/... ./cmd/...`
   and the same with `-race`; the determinism lint over the gate's roots clean (see
   `detlint_dirs` and `g0_detlint` in `agent_work/gates/gate.sh` for the exact root list
   and invocation); statement coverage of `internal/storage` and of
   `internal/storage/httpapi` each at or above 85% (the gate holds `internal/storage` to
   the floor; report both figures); `agent_work/gates/canaries.sh '^partial-run$'` from
   the host ends "1 of 1 caught"; `agent_work/06-integration/cluster-smoke.sh` passes.
5. Commit (one or two commits), then add a short "HTTP package split" section to
   `agent_work/06-integration/build-notes.md` (what moved, the two coverage figures, the
   commands you ran) and correct file paths elsewhere in the notes; commit that.
Do NOT run the full gate (the orchestrator does), do NOT edit `agent_work/gates/`,
`agent_work/orch/` or `agent_work/adversarial/` (the independent harness will be updated
by its own author after you finish — expect it to be broken by the move).
Final message: commit list and three lines. Do not claim anything you did not run.
