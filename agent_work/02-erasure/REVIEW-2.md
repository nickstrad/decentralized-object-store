VERDICT: NITS

## Repair-brief item table

| # | Repair item | Status | Evidence (code, tests, and check against `3ae7e3fb`) |
|---|---|---|---|
| 1 | Finding 1 — unsynced `Put` durability, `Delete` durability, model property, DST durable check | FIXED | `internal/storage/chunkstore.go:81-85,107-118` calls `s.disk.Sync(name)` in the identical-bytes `Put` branch and on both `Delete` paths (ignoring `env.ErrNotFound` from `Sync`). `TestChunkStorePutOfUnsyncedIdenticalBytesIsDurable`, `TestChunkStoreSecondPutAfterAFailedSyncIsDurable`, `TestChunkStoreDeleteOfAnUnsyncedRemovalIsDurable`, and the two-layer `TestPropChunkStoreModel` all fail on `3ae7e3fb` and pass on `HEAD` (including `-rapid.checks=2000`). `dst_test.go:104-117` checks ≥ `k+1` durable shards via `PeekDurable` + `BlobChecksum` inside every PUT callback. |
| 2 | Finding 2 — non-re-entrant callbacks across `internal/storage` | FIXED | `internal/storage/node.go:208-220,264` sets `op.stopped = true` and defers `op.finish(err)` via `n.later` (`AfterFunc(0)`) on launch failure; `putOp.reply` ignores replies once `stopped`; `Node.Get` defers all early errors via `n.later`. `put`/`get` helpers (`node_test.go:63-74,90-101`) assert callbacks never fire before `Put`/`Get` returns. `TestNodePutWithTooFewMembers` and `TestNodePutPlacementFailureNeverCallsBackInsideTheCall` fail on `3ae7e3fb` (`put callback ran inside the Put call`) and pass on `HEAD`; `TestNodeCallbacksNeverRunInsideTheCall` pins the full early-path audit. |
| 3 | Finding 3 — `k+m` overflow & size-arithmetic audit | FIXED | `internal/erasure/matrix.go:34,72,145`, `codec.go:28`, `internal/storage/shard.go:133,209` validate `k > MaxShards || m > MaxShards-k` before adding, guard `n*n` and `m.rows*o.cols` via division, compute `(dataLen-1)/c.k + 1`, and cap `DecodeShard` `PayloadLen` at `MaxStripeSize`. `TestNewConfigTable`, `TestLayoutTable`, `TestHostileCodeShapesAreErrorsNotPanics`, and `TestGetOfAManifestWithAHostileLayoutCallsBackWithErrInvalid` panic (`makeslice: len out of range`) on `3ae7e3fb` and pass on `HEAD`. |
| 4 | Finding 4 — fuzz seeds must run without skipping under `go test` | FIXED | `internal/erasure/fuzzseeds_test.go` supplies 11 deterministic 4,096-byte seeds each for `FuzzGF256` and `FuzzErasureCoding`; `TestFuzzSeedsRunToCompletion` asserts `!sub.Skipped()`. On `3ae7e3fb`, `go test -v -run '^Fuzz' ./internal/erasure` skips 5/5 `FuzzGF256` and 12/12 `FuzzErasureCoding` seeds; on `HEAD`, all 11/11 + 11/11 (and 7/7 `FuzzShardDecode`) report `PASS` with 0 `SKIP`. |
| 5 | Findings 5, 6, 7, 8 — `Membership` nil/clone, `Identity`/`ShardSize`, `DecodeShard` max size, parity benchmark | FIXED | `placement.go:47-50` rejects nil `Membership` (`ErrInvalid`) and clones `ms.Members()` before sorting; `matrix.go:34`, `codec.go:28`, `shard.go:209` fix `Identity`, `ShardSize`, `DecodeShard`; `bench_test.go:52` erases shards 1 and 4 (1 data + 1 parity). `TestPlacementWithNilMembershipIsErrInvalid` (panicked), `TestPlacementDoesNotReorderTheMembershipSlice`, `TestIdentityOfNonPositiveOrHugeSizeIsEmpty`, `TestShardSizeAtTheIntLimit`, and `TestDecodeShardRejectsPayloadOverMaxStripeSize` all fail on `3ae7e3fb` and pass on `HEAD`. |
| 6 | Finding 9 — per-bucket `(k, m)` layout | FIXED | `internal/storage/node.go:322-326` builds `op.codec` from `man.Layout.Codec()` when the manifest's `(K, M)` differs from `n.codec`, and every `getOp` method (`fill`, `reply`, `checkExhausted`) uses `op.codec`. `TestNodeGetUsesTheManifestLayout` fails on `3ae7e3fb` (`ErrInvalid`) and passes on `HEAD`. `TestNodeRejectsBadInput` legitimately updated for the unwritten 2+1 manifest to expect `ErrUnavailable`/`ErrNotFound`. `Node.Put` stays on `Config.Layout` until Chunk 6 bucket config exists (documented in build notes). |
| 7 | C30 `window-pending-stripes` — define what `Window` bounds | CORRECTLY NARROWED | Behaviour kept (quorum early ack); `build-notes.md:110-113,432-473` narrows `Window` to stripes launched and awaiting quorum/decode, states the outstanding-request bound (`N - AckQuorum` per stripe for ≤ `RequestTimeout`) and per-request size, and `node.go:198` drops `op.data = nil` in `putOp.finish`. `NodeStats`/`EncodeState` add `PutAwaitingPeak`, `PutOutstanding`, `PutOutstandingBytes`. `TestNodePutWindowBoundsStripesAwaitingQuorum` pins the narrowed claim on Astra's exact setup (seed 220211, 2/8/64 stripes; requires new `NodeStats` fields so does not compile on `3ae7e3fb`); `TestNodePutReleasesTheObjectAtTheAck` fails on `2aad1a8` (before `op.data = nil`) and passes on `HEAD`. Adversarial harness untouched by builder. |

## New findings

### 1. [NIT] `PutAwaitingPeak` counts a stripe whose `launch` immediately fails
- **File & line:** `internal/storage/node.go:212-218`
- **What is wrong:** `putOp.fill()` increments `op.inflight` and updates `op.n.stats.PutAwaitingPeak` before calling `op.launch(i)`. If `launch(0)` fails immediately (e.g. `Place` returns `ErrUnavailable` with fewer than `k+m` members), no shard requests are ever sent and no stripe ever awaits quorum, yet `n.Stats().PutAwaitingPeak` becomes `1`.
- **Reproduction:** Call `n.Put("b", "k", []byte("x"), cb)` on a 3-node cluster with `k=4, m=2` (as in `TestNodePutWithTooFewMembers`) and inspect `n.Stats().PutAwaitingPeak`: it is `1` instead of `0`.
- **Smallest fix:** Call `if err := op.launch(i); err != nil { op.stopped = true; op.n.later(func() { op.finish(err) }); return }` before incrementing `op.inflight` and updating `op.n.stats.PutAwaitingPeak`.

### 2. [NIT] Parenthetical byte approximation in C30 build notes omits `len(blob name)`
- **File & line:** `agent_work/02-erasure/build-notes.md:447-448`
- **What is wrong:** The exact per-request size formula `1 + 1 + len(blob name) + 64 + ceil(L/k)` is accurate (and verified byte-for-byte by `TestNodePutWindowBoundsStripesAwaitingQuorum`), but the parenthetical example `(≈ 256 KiB + 70 B at the default 1 MiB stripe)` only counts the `1 + 1 + 64` fixed bytes and omits `len(blob name)` (32–105 B depending on bucket length), so actual overhead is ≈ 256 KiB + 98–171 B.
- **Reproduction:** Compare `len(storage.EncodeShardRequest(storage.OpPutShard, storage.ShardID{Bucket: "b", Version: 1, Stripe: 0, Index: 0}, make([]byte, storage.HeaderSize+256<<10)))` (`262242 = 256 KiB + 98 B`) against `+ 70 B`.
- **Smallest fix:** Update `+ 70 B` to `+ 100–170 B` in `build-notes.md:448`.

### 3. [NOTE] Pre-rebase commit SHAs in `build-notes.md`
- **File & line:** `agent_work/02-erasure/build-notes.md:318-320, 553`
- **What is wrong:** The repair-round narrative cites pre-rebase SHAs (`4b7ac61`, `30c9689`, `aad5cc0`, `096560a`, `6cb7dc9d`) rather than the rebased branch SHAs (`dee9ed3`, `2aad1a8`, `6b8d559`, `396eea5`, `c239a45`). Matches the orchestrator's cherry-pick/rebase flow; no code impact.

## Determinism, test integrity, and frozen-package checks

- **Determinism:** `detlint` passed cleanly on both `internal/storage` and `internal/erasure` (`detlint: checked 12 files in 2 packages`). Manual review of all 6 modified product files (`erasure/codec.go`, `erasure/matrix.go`, `storage/chunkstore.go`, `storage/node.go`, `storage/placement.go`, `storage/shard.go`) confirmed zero maps, zero floats, zero wall-clock/global-rand/goroutine/channel/sync usage, and all three new `NodeStats` counters are deterministic `uint64`s included in `Node.EncodeState()`.
- **No weakened tests:** Commit `396eea5` added `m.dirty` tracking to `TestPropChunkStoreModel` so `rot` only targets clean blobs, matching `internal/sim/disk.go:244-247` (`BitRot` does not modify `b.data` when `b.dirty` is set); this fixes a test-model mismatch without weakening durability assertions. `TestNodeRejectsBadInput` was updated to match the intentional Finding 9 behaviour on a valid 2+1 manifest.
- **Frozen packages & rules:** `git diff --name-only main..HEAD` and `3ae7e3fb..HEAD` touch only `internal/erasure/`, `internal/storage/`, and `agent_work/`. `internal/env`, `internal/sim`, `internal/sim/simtest`, `go.mod`, and `agent_work/gates/` are untouched. `nocomments`, `paniccheck`, `go vet`, and `gofmt` are all clean.

## Verified by running vs by reading

### Verified by running (through Docker `dev` container only)
- `docker compose run --rm -T dev go test -v -count=1 ./internal/erasure/... ./internal/storage/...` — PASS (`erasure` 2.20s, `storage` 4.30s).
- `docker compose run --rm -T dev go test -v -count=1 -run '^Fuzz' ./internal/erasure ./internal/storage` — PASS: 11/11 `FuzzErasureCoding`, 11/11 `FuzzGF256`, 7/7 `FuzzShardDecode`, 0 `SKIP`.
- `docker compose run --rm -T dev go test -race -count=1 ./internal/erasure/... ./internal/storage/...` — PASS (`erasure` 46.31s, `storage` 25.69s).
- `docker compose run --rm -T dev go test -count=1 ./internal/erasure/... ./internal/storage/... -rapid.checks=2000` — PASS (`erasure` 17.05s, `storage` 11.93s).
- `docker compose run --rm -T dev env DST_SEEDS=50 go test -count=1 -v -run TestStripedPutGetDST ./internal/storage/...` — PASS (26.60s; 51 seeds × 2 modes = 4,743 GETs each; within-budget: 4,743 ok / 0 unavailable / 3,409 fallbacks / fewest durable shards at ack 6; budget-lifted: 1,225 ok / 3,518 unavailable / 0 wrong bytes).
- `docker compose run --rm -T dev go test -count=1 -cover ./internal/erasure/... ./internal/storage/...` — PASS (coverage: `internal/erasure` 98.4%, `internal/storage` 97.8%).
- `docker compose run --rm -T dev go test -run '^$' -bench BenchmarkReconstructAllMissing1MiBStripe -benchmem ./internal/erasure` — PASS (210.98 MB/s, 6 allocs/op on shared CPU).
- Linters in Docker (`go vet`, `gofmt -l`, `detlint` on `internal/storage internal/erasure`, `nocomments`, `paniccheck -allow .../panic-allowlist.txt internal`) — all clean.
- Regression verification against `3ae7e3fb25bf94097b899b42f0a090d4e0847965` (and `2aad1a8` for `op.data = nil`) inside ephemeral `/tmp/oldrun` inside Docker (host worktree untouched): confirmed every regression test listed in the item table above fails/panics on the old code (and all 5 + 12 old fuzz seeds `SKIP`).

### Verified by reading
- `agent_work/02-erasure/objstore-p2-02-erasure-review2.md`, `REVIEW-1.md`, `objstore-p2-02-erasure-repair1.md`, `build-notes.md`, `gates/result.json`.
- `agent_work/adversarial/02-erasure/ATTACKS.md` and `RESULTS.md`.
- Full git log and diff for `3ae7e3fb25bf94097b899b42f0a090d4e0847965..HEAD` across `internal cmd agent_work/gates`, plus `git diff --name-only main..HEAD`.
- All product files in `internal/erasure/` (`gf256.go`, `matrix.go`, `codec.go`, `errors.go`) and `internal/storage/` (`chunkstore.go`, `node.go`, `placement.go`, `shard.go`, `stripe.go`, `manifest.go`, `shardproto.go`, `errors.go`), relevant test files (`dst_test.go`, `node_test.go`, `node_repair_test.go`, `manifest_test.go`), and `internal/sim/disk.go`.
