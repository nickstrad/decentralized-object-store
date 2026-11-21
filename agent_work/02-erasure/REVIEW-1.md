VERDICT: FAIL

## Findings

### 1. [MAJOR] `ChunkStore.Put` returns `nil` without syncing when matching bytes exist in the unsynced written layer
- **File & line:** `internal/storage/chunkstore.go:78-83`
- **What is wrong:** `Put` calls `s.read(id)` (which reads `env.Disk`'s *written* layer, not the durable layer) and immediately returns `nil` when `bytes.Equal(old, blob)` without calling `s.disk.Sync(name)`. This violates the simulator contract ("write, then `Sync`, means durable; unsynced changes are lost on crash") and the build-notes claim ("Write then `Sync` before returning, so a returned `Put` is durable"). In Chunk 6's real runtime, if `Write` succeeds and `Sync` fails (transient `fsync` error), a coordinator retry of `Put(id, blob)` reads the unsynced file from the OS page cache, hits `bytes.Equal(old, blob)`, and returns `nil` without ever calling `Sync`.
- **Reproduction:**
  ```go
  s, node, d := simDisk(t)
  st := storage.NewChunkStore(d)
  id := storage.ShardID{Bucket: "b", Version: 1, Stripe: 0, Index: 0}
  blob := shardFor(t, id, []byte("unsynced"))
  mustNil(t, d.Write(id.BlobName(), blob)) // written layer only
  mustNil(t, st.Put(id, blob))             // returns nil without calling Sync
  mustNil(t, s.Crash(node))
  _, ok := s.Disk(node).PeekDurable(id.BlobName()) // false: acknowledged Put lost
  ```
- **Smallest fix:** In the `bytes.Equal(old, blob)` branch (`internal/storage/chunkstore.go:81-83`), call `s.disk.Sync(name)` and return its wrapped error if non-nil before returning `nil`. Add a regression test covering unsynced write + `Put` + `Crash` / `PeekDurable`.

### 2. [MAJOR] `Node.Put` invokes its callback synchronously (re-entrantly) when `Place` fails on stripe 0
- **File & line:** `internal/storage/node.go:176, 203-212`
- **What is wrong:** `Node.Put` defers `op.finish` through `n.later` (`Clock.AfterFunc(0, ...)`) for invalid bucket/key and 0-byte objects, matching the simulator's non-re-entrant callback contract and `build-notes.md` ("never inside the `Put`/`Get` call"). However, line 176 calls `op.fill()` synchronously inside `Put`; when `op.launch(0)` returns an error (e.g., `Place` returning `ErrUnavailable` on fewer than `k+m` members, or `ErrInvalid` on duplicate/empty member IDs), `op.fill()` calls `op.finish(err)` immediately on line 209, invoking `cb` **before `Node.Put` returns**. Any caller (in tests or Chunk 6's node loop) that initializes per-operation state after calling `Put` or relies on non-re-entrant callbacks will misbehave.
- **Reproduction:**
  ```go
  // Using the 3-node cluster from TestNodePutWithTooFewMembers (k=4, m=2):
  called := false
  nodeOf(t, s, ids[0]).Put("b", "k", []byte("x"), func(_ *storage.Manifest, _ error) {
      called = true
  })
  // called is already true here, before s.RunUntilQuiet() runs.
  ```
- **Smallest fix:** In `putOp.fill()`, on `err := op.launch(i); err != nil`, defer `op.n.later(func() { op.finish(err) })` and `return` from `fill()` so no further stripes launch and `cb` never runs re-entrantly. Add an assertion in `TestNodePutWithTooFewMembers` (and `TestNodeRejectsBadInput`) that the callback has not fired before `Put`/`Get` returns.

### 3. [MAJOR] Signed integer overflow in `k+m > MaxShards` panics on hostile `(k, m)`
- **File & line:** `internal/erasure/matrix.go:135-140` (`validConfig`), `internal/storage/shard.go:132-135` (`validShape`)
- **What is wrong:** `if k < 1 || m < 0 || k+m > MaxShards` overflows signed `int` when `k` and `m` are large positive integers (for example `k = math.MaxInt, m = 1`, or `k = 1<<62, m = 1<<62`): `k+m` wraps to a negative value (`<= 256`), bypassing the upper bound check. `CodingMatrix` then calls `newMatrix(k+m, k)` -> `make([]byte, (k+m)*k)` and panics (`runtime error: makeslice: len out of range`), violating PLAN 2.6 ("malformed or hostile input is an error, never a panic"). Because `storage.Layout.Validate` calls `erasure.New(l.K, l.M)`, this panic is also reachable through `Layout.Validate`, `Layout.Codec`, `Config.Validate`, `NewNode`, and `Manifest.Validate`. In `storage/shard.go:133`, `validShape` has the same overflow; it avoids a panic only incidentally because the subsequent `index >= k+m` check fails with a bogus message (`"index 0 of -9223372036854775808"`).
- **Reproduction:**
  ```go
  _, _ = erasure.New(math.MaxInt, 1) // panics: runtime error: makeslice: len out of range
  _ = (storage.Layout{K: math.MaxInt, M: 1, StripeSize: 1024}).Validate() // same panic
  ```
- **Smallest fix:** In both `validConfig` and `validShape`, check `k < 1 || m < 0 || k > MaxShards || m > MaxShards || k+m > MaxShards` (equivalently `k > MaxShards-m`), and add `(math.MaxInt, 1)` to `TestNewConfigTable` and `TestLayoutTable`.

### 4. [MAJOR] Every `f.Add` seed in `FuzzGF256` (5/5) and `FuzzErasureCoding` (12/12) skips without executing any assertion
- **File & line:** `internal/erasure/gf256_test.go:207-214`, `internal/erasure/codec_test.go:501-510`
- **What is wrong:** `rapid.MakeFuzz` converts the fuzz `[]byte` into `[]uint64` words (8 bytes each) for `rapid`'s bitstream and calls `tb.SkipNow()` whenever a generator runs out of input. Each `rapid.Byte()` draw consumes two `uint64`s (16 bytes), and `propErasureCoding`'s generators (`drawCode`, `drawPayload`, `SliceOfNDistinct`) consume substantially more. All 5 seeds in `FuzzGF256` are 0 or 24 bytes (stopping after drawing `a`, before `b`, `c`, or `checkFieldLaws`), and all 12 seeds in `FuzzErasureCoding` are 0–128 bytes (running out of tape before reaching `Encode`/`Reconstruct`/`Join`). Consequently, during `go test ./...`, **100% of the added seeds in both fuzz targets silently skip with zero assertions run**, and fuzzing begins from a corpus of zero valid inputs.
- **Reproduction:** Run `docker compose run --rm -T dev go test -v -run '^Fuzz' ./internal/erasure`:
  - `FuzzGF256/seed#0` .. `seed#4`: all 5 report `--- SKIP` (only `draw a` appears in logs).
  - `FuzzErasureCoding/seed#0` .. `seed#11`: all 12 report `--- SKIP` (none reaches `lost` or `Reconstruct`).
- **Smallest fix:** Provide boundary `f.Add` seeds long enough for `rapid.MakeFuzz` to complete all draws (e.g., >= 64 bytes for `FuzzGF256`, >= 512 bytes for `FuzzErasureCoding`) and verify `go test -v -run '^Fuzz' ./internal/erasure` prints `--- PASS` (not `--- SKIP`) for the boundary seeds.

### 5. [NIT] `Place`/`Replace` panic on nil `Membership` and sort `ms.Members()` in place
- **File & line:** `internal/storage/placement.go:46-48`
- **What is wrong:** `sortedMembers(ms)` dereferences `ms.Members()` without a nil check (so `storage.Place(nil, ...)` and `storage.Replace(nil, ...)` panic instead of returning `ErrInvalid`) and sorts the returned slice in place (`slices.SortFunc(members, ...)`). While `StaticMembership.Members()` clones its slice, a Chunk 6 `Membership` adapter that returns a cached slice would have its slice silently reordered by `Place`/`Replace`.
- **Reproduction:** `storage.Place(nil, "b/0/0", 6, 2)` panics with nil pointer dereference.
- **Smallest fix:** Return `ErrInvalid` when `ms == nil`, and clone with `members := slices.Clone(ms.Members())` before sorting.

### 6. [NIT] `erasure.Identity(n)` accepts negative `n` (producing a panicking `Matrix`) and `Codec.ShardSize` can overflow
- **File & line:** `internal/erasure/matrix.go:30-36`, `internal/erasure/codec.go:24-29`
- **What is wrong:** `Identity(-1)` computes `(-1)*(-1) = 1` and returns `Matrix{rows: -1, cols: -1, data: make([]byte, 1)}`, which panics on `Rows()` / `Invert()`. `Codec.ShardSize(dataLen)` computes `(dataLen + c.k - 1) / c.k`, which overflows `int` for `dataLen > math.MaxInt - c.k + 1` (whereas `storage.Layout.Stripes` already uses the overflow-free form).
- **Reproduction:** `erasure.Identity(-1).Rows()` panics (`makeslice: len out of range`); `c, _ := erasure.New(4, 2); c.ShardSize(math.MaxInt)` returns `-2305843009213693952`.
- **Smallest fix:** In `Identity(n)`, return `Matrix{}` when `n <= 0`; in `ShardSize`, return `(dataLen-1)/c.k + 1`.

### 7. [NIT] `DecodeShard` does not enforce `MaxStripeSize`
- **File & line:** `internal/storage/shard.go:146, 209-214`
- **What is wrong:** `EncodeShard` rejects payloads larger than `MaxStripeSize` (64 MiB), but `DecodeShard` does not check `h.PayloadLen > MaxStripeSize`. A checksum-valid blob larger than 64 MiB is therefore accepted by `DecodeShard` even though `EncodeShard` rejects re-encoding it, breaking the claim in `build-notes.md` ("A blob is accepted only if it re-encodes byte for byte").
- **Reproduction:** Construct a valid-checksum shard blob with `len(payload) == storage.MaxStripeSize + 1`; `DecodeShard` returns `nil` error, then `EncodeShard` returns `ErrInvalid`.
- **Smallest fix:** Add `if h.PayloadLen > MaxStripeSize { return bad("payload over max stripe size") }` in `DecodeShard`.

### 8. [NIT] `BenchmarkReconstructAllMissing1MiBStripe` erases two data shards, not one data + one parity
- **File & line:** `internal/erasure/bench_test.go:48-57` vs `agent_work/02-erasure/build-notes.md:221`
- **What is wrong:** `build-notes.md` reports `BenchmarkReconstructAllMissing1MiBStripe` as *"Reconstruct one data + one parity (`Reconstruct`), 1 MiB: 280 MB/s"*, but line 52 sets `shards[1], shards[3] = nil, nil` for a `(k=4, m=2)` codec. Indices `0..3` are all data shards, so the parity rebuild loop (`i := c.k; i < c.N(); i++`) never runs in that benchmark.
- **Reproduction:** Read `internal/erasure/bench_test.go:52` (`k=4, m=2`, indices 1 and 3 are both `< k`).
- **Smallest fix:** Change line 52 to `shards[1], shards[4] = nil, nil` and update the measured figure in `build-notes.md`.

### 9. [NOTE] Per-bucket `(k, m)` layout support for Chunk 6 integration
- **File & line:** `internal/storage/node.go:72, 158-166, 309-311`
- **What is wrong:** PLAN 7.2 specifies `(k, m)` as configurable per bucket, and `Manifest` already carries `Layout{K, M, StripeSize}`. However, `Node` holds a single `codec` from `cfg.Layout`, `Node.Put` cannot take a per-bucket `Layout` (or an OCC-supplied `VersionID`, already noted in `build-notes.md`), and `Node.Get` rejects any manifest whose `Layout.K`/`Layout.M` differs from `n.cfg.Layout` (tested in `TestNodeRejectsBadInput`) even though `Node.Get` already respects `man.Layout.StripeSize`.
- **Impact:** Acceptable for Chunk 2's single-layout scope; Chunk 6 (which also owns `internal/storage`) will need to let `Node.Get` construct/cache a codec from `man.Layout.Codec()` and add a layout/version parameter on `Put` when wiring per-bucket settings and OCC.

---

## Determinism check on `internal/storage` (requested specifically)

Reviewed all 8 product files in `internal/storage` (`errors.go`, `shard.go`, `chunkstore.go`, `stripe.go`, `manifest.go`, `placement.go`, `shardproto.go`, `node.go`) both by running `detlint` directly on `internal/storage` and by manual inspection:
- **Maps:** Zero `map` declarations or map iterations anywhere in product code of `internal/storage` (or `internal/erasure`). `placement.go` uses sorted slices, parallel slice counts (`regionCount`), and `slices.BinarySearchFunc`.
- **Floats:** Zero float/complex types or arithmetic.
- **Wall clock / global rand / concurrency / pointer formatting:** Zero `time.Now`/`Sleep`/`After`, zero `math/rand`/`crypto/rand` imports (only `n.e.Rand` in `node.go`), zero `go`/`chan`/`select`/`sync`/`sync/atomic`, zero `%p` or pointer arguments to `fmt`.
- **Frozen simulator packages and rules:** `git diff --name-only main..HEAD` touches only `agent_work/02-erasure/`, `internal/erasure/`, and `internal/storage/`. `internal/env`, `internal/sim`, `internal/sim/simtest`, `go.mod`, and `agent_work/gates/` are untouched. Zero comments in Go files (`nocomments` clean), zero panics in product code (`paniccheck` clean), stdlib-only product imports (`importcheck` clean).

---

## Verified by running vs by reading

### Verified by running (through Docker `dev` container)
- `docker compose run --rm -T dev go test -v -count=1 ./internal/erasure/... ./internal/storage/...` — passed (erasure 1.92s, storage 3.45s); revealed all 5 `FuzzGF256` seeds and all 12 `FuzzErasureCoding` seeds skipping (Finding 4).
- `docker compose run --rm -T dev go test -v -run '^Fuzz' ./internal/erasure ./internal/storage` — confirmed 5/5 `FuzzGF256` seeds and 12/12 `FuzzErasureCoding` seeds `SKIP`; 7/7 `FuzzShardDecode` seeds `PASS`.
- `docker compose run --rm -T dev go test -count=1 -cover ./internal/erasure ./internal/storage` — passed; coverage 98.8% (`internal/erasure`), 97.9% (`internal/storage`).
- `docker compose run --rm -T dev go test -count=1 ./internal/erasure ./internal/storage -rapid.checks=2000` — passed (erasure 17.0s, storage 9.4s).
- `docker compose run --rm -T dev go test -race -count=1 ./internal/erasure ./internal/storage` — passed (erasure 40.9s, storage 24.3s).
- `docker compose run --rm -T dev go test -race -count=3 ./internal/storage` — passed (34.8s).
- `docker compose run --rm -T dev env DST_SEEDS=50 go test -count=1 -v -run TestStripedPutGetDST ./internal/storage` — passed (26.8s; 51 seeds × 2 modes = 4,743 GETs each; within-budget: 4,743 ok / 0 unavailable / 3,409 fallbacks / 2,267 faults applied; budget-lifted: 1,225 ok / 3,518 unavailable).
- `docker compose run --rm -T dev go vet ./internal/erasure/... ./internal/storage/...` — clean.
- `detlint -root /src internal/erasure internal/storage` — clean (`detlint: checked 12 files in 2 packages`).
- `nocomments -root /src internal/erasure internal/storage` — clean.
- `paniccheck -root /src -allow /src/agent_work/gates/panic-allowlist.txt internal` — clean.
- `gofmt -l internal/erasure internal/storage` and `importcheck` across all 27 `.go` files — clean.
- Independent Python verification of GF(2^8) (poly `0x11D`, gen `2`) Cauchy matrix `CodingMatrix(4, 2)` and all parity vectors in `TestEncodeKnownVectors` — exact byte-for-byte match.
- `git log --stat main..HEAD`, `git diff --name-only main..HEAD`, `git status --short` — confirmed clean tree and untouched frozen packages.

### Verified by reading
- All 4 product files and 4 test files in `internal/erasure/`.
- All 8 product files, 11 test files, and `testdata/seeds.txt` in `internal/storage/`.
- `PLAN.md` (sections 2, 4.2 Chunk 2, 5.1, 7.1–7.5), `agent_work/02-erasure/objstore-p2-02-erasure-build.md`, `agent_work/02-erasure/objstore-p2-builder-rules.md`, `agent_work/02-erasure/build-notes.md`, `agent_work/02-erasure/gates/result.json` (`overall: pass`, `counts_for_landing: true` on `4e593d2c`), `agent_work/0b-simulator/build-notes.md`, `agent_work/0b-simulator/frozen-api.txt`, `internal/sim/disk.go`, `agent_work/gates/detlint/README-tools.txt`, `agent_work/gates/panic-allowlist.txt`, and `pgregory.net/rapid@v1.3.0/engine.go` (`MakeFuzz`/`checkFuzz`).
- Confirmed every named test in PLAN 4.2 Chunk 2 and `build-notes.md` exists and checks what its name states (except Finding 4 on fuzz seeds and Finding 8 on the parity benchmark), and that both canary tests (`TestCanaryChecksumSkipBreaksCorruptionProperty` and `TestCanaryChecksumSkipFailsStripedDST`) fail for the planted checksum-skip reason.
