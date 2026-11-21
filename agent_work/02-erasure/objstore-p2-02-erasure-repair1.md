# Brief — Chunk 2 REPAIR (the one repair round): Reed-Solomon erasure coding and the chunk store

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/02-erasure`).
You are repairing work a previous builder committed. FIRST read
`agent_work/02-erasure/objstore-p2-builder-rules.md` (binding: no comments in Go, Docker
only, tests first, commit here, never push), then the original brief
`agent_work/02-erasure/objstore-p2-02-erasure-build.md`, `agent_work/02-erasure/build-notes.md`
and the review `agent_work/02-erasure/REVIEW-1.md` (verdict FAIL: four MAJOR, four NIT, one
NOTE). The reviewer's reproductions and "smallest fix" notes are your starting point; a
better fix is welcome if you can prove it. You own `internal/erasure/` and the chunk-store
files of `internal/storage/` only.

## Must fix (each: a regression test written FIRST that fails on the current code)
1. Finding 1 — `ChunkStore.Put` must not report success for bytes that are only in the
   unsynced written layer: "Put returned nil" means durable. Test with a crash right after
   the second Put (the simulator drops unsynced data); extend the chunk-store reference
   model property so it tracks durability across crashes, and re-check the "PUT is
   acknowledged at k+1 DURABLE shards" claim end to end in `TestStripedPutGetDST`.
2. Finding 2 — no callback ever runs inside the call that registered it (the simulator
   contract: callbacks run as later events). Audit every callback-taking method in
   `internal/storage` for every early-error path, not only the one the reviewer found.
3. Finding 3 — arithmetic on hostile `(k, m)`, sizes and counts cannot overflow or panic:
   validate before adding; same audit for every size computation fed by decoded or
   caller-supplied numbers (finding 6's `ShardSize`, `Identity`; finding 7's
   `MaxStripeSize` in `DecodeShard`).
4. Finding 4 — fuzz seeds that really run under plain `go test` (none may SKIP).
5. Findings 5, 6, 7, 8 — as the reviewer describes.
6. Finding 9 (NOTE, per-bucket `(k, m)` for the integration chunk): read it; if the
   reviewer shows the current API cannot carry a per-object or per-bucket layout that the
   manifest already records, make the smallest change that removes the obstacle and say
   so; otherwise answer it in the build notes.

## Also
- The gate's determinism lint does not cover `internal/storage` (a later chunk adds HTTP
  to that package). Run it by hand on your storage files and record the result:
  `docker compose run --rm -T dev bash -c 'cd agent_work/gates/detlint && go build -o /tmp/dl . && cd /src && /tmp/dl -root /src internal/storage internal/erasure'`.
- Re-measure any number in the build notes your changes can move (throughput, memory).

## Before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 02-erasure`;
it must end `overall=pass`. Add a "Repair round" section to
`agent_work/02-erasure/build-notes.md`: each finding, the test that now proves it, what
changed, API changes, numbers that moved, anything you chose not to do and why. Everything
committed (gate result files may stay uncommitted). Final message: commit list and a
three-line summary. Do not claim anything you did not run.
