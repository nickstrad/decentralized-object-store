# Brief — Chunk 6, fixes after review 2 (integration)

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/06-integration`).
Binding rules: `agent_work/06-integration/objstore-p2-builder-rules.md` (no comments in Go,
Docker only, test first, commit here, never push). Stage only the files you change, by
explicit path. Do not edit `agent_work/gates/`, `agent_work/orch/` or `agent_work/adversarial/`.

Read `agent_work/06-integration/REVIEW-2.md`: all eleven first-review findings are judged
fixed except the residual of finding 1, and there are six "New Findings" (two MAJOR, four
NIT). The orchestrator accepts all six as correct. Fix them — each with a regression test
written FIRST that fails on the current code:

1. New finding 1 (MAJOR) — orphan records must accumulate, never overwrite: a
   multi-stripe object whose shards are collected as orphans on one holder in DIFFERENT
   maintenance ticks must still be detected and declared lost when more than `m` shards
   of any stripe are gone (key the record per shard or per stripe, or write the union —
   your choice; think about what last-writer-wins does to the record when two ticks or
   two nodes write it, and pick a structure where a concurrent write cannot erase an
   entry). Tests: the reviewer's reproduction as a deterministic scenario (multi-stripe
   object, shards arriving across ticks, the writer isolated past the orphan grace);
   then generalise `TestAWriteWhoseShardsWereCollectedAsOrphansIsRecordedLost` over
   object sizes of 1, 2 and more than `DefaultWindow` stripes; and add the invariant to
   the DST oracle: after heal, no key that the metadata lists as live is unreadable
   UNLESS it is declared lost on record. This closes the residual of finding 1: a write
   from a peer away longer than the orphan grace is either readable or declared lost,
   never live-and-unreadable, for objects of every size.
2. New finding 2 (MAJOR) — `realNet.Request` calls its callback exactly once on every
   path (non-positive timeout, oversized self-request, every other early error), with
   the right error; audit `realNet.Send` and every other callback-taking method of the
   real runtime for the same pattern. Tests in a `synctest` bubble counting invocations.
3. New finding 3 — `Flip` after a coordinator crash between its two writes during a
   concurrent migration must not strand the bucket unwritable: the reviewer's
   reproduction as a test; the bucket ends writable with exactly one route owner.
4. New finding 4 — key the test oracle's content map by object key (and add a case with
   identical payloads on two keys that fails on the old oracle).
5. New finding 5 — `fileDisk.syncChain` retried after a mid-chain error syncs the
   remaining ancestors (test with an injected transient failure on the second directory).
6. New finding 6, product items — `track` refuses connections once the runtime context
   is cancelled or the loop has panicked (test: no connection outlives a loop panic
   beyond the shutdown path); the `OCCFanout` error message names the right field;
   correct the two stale sentences in the build notes.

Then, through Docker: `gofmt -l .` empty; `go vet ./...`; `go test ./internal/node/...
./internal/noderuntime/... ./internal/storage/... ./cmd/...`; the same with `-race
-count=3` for `internal/noderuntime`, `internal/storage/...` and `cmd/objnode`;
`go test ./internal/node -rapid.checks=2000 -run 'Prop'`; the determinism lint over the
gate's roots clean; `DST_SEEDS=100 go test -count=1 ./internal/node` (the orchestrator
runs the 500-seed gate). On the host: `agent_work/06-integration/cluster-smoke.sh` and
`agent_work/adversarial/06-integration/run-all.sh` (record the SUMMARY line; restore its
evidence files with `git checkout -- agent_work/adversarial`).
Commit tests and fixes in small commits; append a "Review 2 fixes" section to
`agent_work/06-integration/build-notes.md` (each item, its test, what changed, the
restated residual limit in one plain paragraph) and commit.
Final message: commit list and three lines. Do not run the full gate. Do not claim
anything you did not run.
