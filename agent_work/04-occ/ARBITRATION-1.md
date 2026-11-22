VERDICT: NITS

The five repairs are FIXED. No remaining BLOCKER or MAJOR was found in the package. It may land after the factual correction below; this verdict does not authorize merging or publishing.

One small item must close:

- **Correct the harness measurement and suggested expectation in `agent_work/04-occ/build-notes.md:651` and `:517`.** The statement that all ten seeds produced two hooks and counters `(1,1)` is false for the committed harness schedule. Seeds 4701, 4703, 4707 and 4708 produce one hook and counters `(1,0)`; 4702, 4704, 4705, 4706, 4709 and 4710 produce two hooks and `(1,1)`. All ten produce exactly one distinct losing version. Replace the all-ten/two-record assertion with these results, and remove the fixed “here 2” expectation. The smallest valid harness recommendation is to retain the expected loser-ID assertion, `len(w.lost) == 1`, and `w.counts()`; do not replace `hooks == 1` with `hooks == 2`. A node only records a pair it actually compares. The winner's node need not receive the losing version before the writer learns the winner and stops sending the loser. This is a measurement correction, not a product change.

| Item | Verdict | Evidence |
|---|---|---|
| REVIEW-2 finding 1: discarded-dot gap | FIXED | `store.go:287–301` retires the current actor before another write can inflate the missing dot; `TestDiscardGapNeverClaimed`, extended six-write `TestDotsAreNeverReused`, and `TestPropClassificationMatchesOracle` pass. Both directed `next2` variants fail against pre-round-2 product code. |
| REVIEW-2 finding 2: rejected incoming loser | FIXED | `doc.go` calls the rejection callback even when the document is unchanged; `store.go:270–314` records the loser and persists the observation. Incoming-loser, old-news, failed-persist, deduplication, writer-crash/wipe/rot, disk-loss allowance and canary tests pass. The old product fails the incoming-loser check and all three writer-fate scenarios. |
| REVIEW-2 finding 3: crash before emission | FIXED | `record` appends the outbox and tail before `end` persists; emission precedes drain; `OpenStore` and `Node.open` replay. Crash-before-publish and failed-drain/restart tests pass. A baseline-compatible restart reproduction on old code yields counter 1, logs 0, tail 0. |
| Round-2 item 4: quota overflow | FIXED | `meta.go:218–235` checks both additions before incrementing; `TestRecountObjectsNeverWraps` passes, including typed error propagation and no quota write. Old code returns `{Bytes:0 Objects:2}` without error for sizes MaxUint64 and 1. |
| Round-2 item 5: stale no-op recount | FIXED | `quota.go` re-reads the current precondition after the hook, retries changes and respects the attempt bound; `TestRecountNoopRechecksCurrentVersion` passes. The same test on old code returns a stale no-op after one call. |

Actor retirement and the oracle:

For an honest version of a given key, a missing earlier dot from this actor must have left its writer's held history through a concurrent replacement. The retirement condition catches the first such gap. Ordinary causal replacements retain the earlier history; writes after retirement cannot extend the retired actor's counter. The classification property's expected ancestry comes from `oracle.record`: the held version ID and its independently recorded ancestors. It does not derive expected ancestry from product vectors, `Clock`, `Descends` or `Supersedes`. Product `Supersedes` is compared with that oracle for both replacement and rejection. No honest-history counterexample was found in the directed checks or 2,000-check suite. This is tested evidence plus the inspected invariant, not exhaustive enumeration of all histories.

Recording, persistence and the guarantee:

- The saved 256-entry FIFO deduplicates `(key, loser ID)` observations on a node. Old news is excluded before recording. After eviction, another delivery can increment the counter again; the notes state this over-count and each record still identifies the loser. This does not suppress a first record or change the winner. Counter overflow saturates.
- The rejection transaction marks local state changed even when `Merge` returns document-changed=false. Failed persistence restores document, actor/counter, HLC, counter, tail, outbox and deduplication state, and emits nothing.
- The outbox is persisted with the merge and replays before being cleared durably. A failed drain can repeat emission; it cannot cause zero emissions. The 1,024 cap applies to already-emitted records retained after failed drains, not to first emissions from a large merge. The 64-entry readable tail survives ordinary restart.
- The directed writer-loss scenarios verify that the surviving peer's log names the loser after its writer permanently crashes, wipes/rejoins, or loses its state blob. The history oracle tracks receiving node incarnations independently and permits an otherwise unexplained version only when every receiving incarnation lost its disk while holding it. A surviving receiver or a new receiving incarnation invalidates that allowance; the silent-rejection canaries still fail despite declaring writer disk loss.
- Precisely, the randomized loss oracle proves **historical emission**: its collected callbacks survive simulated node wipes. It does not prove an indefinitely retained archive on live nodes after arbitrary later destruction of all log sinks. The readable tail is explicitly bounded and records are deliberately not replicated. The stated guarantee is supported as “every concurrent discard is counted and emitted, with crash replay; the peer survives the writer-loss scenarios,” not as permanent archival retention independent of the configured sinks. Under that emission contract, no additional unrecorded concurrency-loss case was found. The only permitted never-observed loss is the stated all-holder disk-loss case. The inaccurate ten-seed measurement above is the concrete mismatch between the notes and rerun evidence.

No new product regression found:

`Version.Compare` remains stamp `(wall, logical, node)` then version ID, and `Record.Merge` remains the maximum under that order. Vectors only classify. Discard records, deduplication and counters live in the local state encoding, not `Doc.Encode`. Merge/convergence tests continue to pass with different local discard histories. Round 2 does not modify `internal/env` or `internal/sim` (including simtest). Inspection of the product diff found no new nondeterministic dependency. Test changes add receiver/incarnation tracking and fault schedules; the bit-rot change skips only simulator-marked damaged blobs in the invariant reader, rather than accepting arbitrary decode failures. The changed discard assertions enforce the new two-direction rule, and the new dot test extends the old coverage.

RESULTS.md N1:

The central explanation is **confirmed**. The committed check at `agent_work/adversarial/04-occ/sim.go:209` requires exactly one hook globally; its rerun fails at seed 4702, where both nodes record the same loser. There is still exactly one lost version. A temporary-copy check accepting one or two hooks, retaining the expected loser ID, distinct-loser count and `w.counts()`, passes all ten seeds. The stronger assertion in the build notes that every seed records on both nodes is **rejected**, as detailed in the nit. The harness source in the worktree was not changed.

Verified by running:

All Go execution used Docker from this worktree root. Current-tree commands:

- `docker compose run --rm -T dev go test -count=1 ./internal/occ/...` — PASS, 47.036 s.
- `docker compose run --rm -T dev go test -race -count=1 ./internal/occ/...` — PASS, 32.275 s.
- `docker compose run --rm -T -e DST_SEEDS=50 -e DST_ARTIFACTS=/tmp/occ-arbitration-dst dev go test -count=1 -run 'DST|Canary' ./internal/occ/...` — PASS, 36.551 s.
- `docker compose run --rm -T dev go test -count=1 ./internal/occ/... -rapid.checks=2000` — PASS, 53.605 s.
- `docker compose run --rm -T -w /src/agent_work/adversarial/04-occ -e CHECK=partition-two-writer dev go run . sim` — expected failure at seed 4702, reproducing N1.

Old-code checks used a `git archive f1102ec` copy under `/tmp/occ-arbitration-old`, mounted as `/review` in Docker. This is the actual product before round 2; `git show :<file>` now returns the repaired index and is not an old-code baseline. Unchanged test bodies for the gap, incoming loser and stale no-op were copied into that tree and all failed behaviorally. Overflow and restart checks were adapted to the old API (no `ErrUsageOverflow` or `OpenStore`) and also failed behaviorally, as listed above. The three writer-fate scenarios and independent classification property were also run on old product code; all failed, with rapid seed 117. These were runtime failures, not compilation failures. Not every new test can compile unchanged against an API that did not yet exist.

The N1 diagnostic copy was `/tmp/occ-arbitration-current`, exported from HEAD, with only the diagnostic assertion/output changed for that run. Product files were unchanged during the ten-seed diagnostic. Afterwards, in that temporary copy only, I disabled the actor-retirement condition (`if loser.clockAt(s.actor) > winner.clockAt(s.actor)` became `if false`) and ran `go test -count=1 -run TestPropClassificationMatchesOracle ./internal/occ -rapid.checks=2000 -rapid.seed=117` in Docker. It failed after 96 checks (17.953 s), shrinking to replay seed 4773: the product classified a replacement as causal while the independent oracle classified it as concurrent. This isolates the new property's ability to detect finding 1 even with rejection recording already repaired.

Verified by reading:

Read the arbitration/repair/round-2 briefs, REVIEW-2, current build notes, the relevant PLAN sections, STATE.md, RESULTS.md, harness source and driver, round-2 product/test diffs, persistence and oracle implementations, and the saved gate result. The saved gate says overall=pass at `3094597`; `git diff 3094597 HEAD -- internal/occ` is empty, so its package code matches this arbitration tree. Its 500-seed/fuzz/full-repository results are prior gate evidence, not runs claimed here.

Scope and workspace: no product, test, harness, STATE.md or gate file was edited; no commit or git write command was run. Temporary diagnostic files are under `/tmp`; this report is the sole worktree output. The required initial kb search failed because this worktree has no configured index; the feedback attempt also failed for that reason. No knowledge-store result was used.

NITS CLOSED (orchestrator, 2026-11-22): the one item is closed — agent_work/04-occ/build-notes.md now carries the arbiter's per-seed measurement (one record on seeds 4701, 4703, 4707, 4708; two on the other six; one distinct lost version on all ten) and the corrected harness recommendation (keep loser ID, len(lost) == 1 and counts; accept one or two records).
