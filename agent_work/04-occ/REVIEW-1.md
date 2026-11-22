VERDICT: FAIL

## Findings

### 1. MAJOR — Quadratic CPU blowup in `Decode` / `Record.Merge` (`maximal`) and in `FuzzOCCDecode`'s `checkLaws` oracle
- **Where:** `internal/occ/record.go:39-57` (`maximal`), `internal/occ/version.go:49-64` (`Version.Descends`), `internal/occ/encoding.go:184-210` (`decoder.version`, `decoder.record`), `internal/occ/prop_test.go:17-45, 64-90` (`events`, `strictlyBelow`, `checkLaws`).
- **What is wrong:**
  - *Decoder count audit (specific check):* Every declared count in `encoding.go` (`d.count(1)` for bytes/strings, `d.count(3)` for vector entries, `d.count(8)` for record siblings) and `doc.go` (`d.count(2)` for records, `d.count(3)` for scalars), as well as `ParseKey`, `DecodeUsage`, `DecodeObjectMeta`, `decodeOp`, `decodeResult`, and `decodeState`, IS bounded against `d.rest()/minSize`. There is no unbounded allocation from a forged length prefix.
  - *Product-code blowup:* `Decode` places no cap on siblings per record, and `maximal(vs)` (`record.go:39-57`) runs an `O(S^2)` nested loop over all sibling pairs calling `w.Supersedes(v)` (`w.Descends(v) && !v.Descends(w)`), where `Descends` (`version.go:53-62`) performs `O(C log C)` binary searches over `w.ctx.entries`. A valid canonical message of `B` bytes can pack `S ≈ B / (11 + 3C)` siblings of one key all sharing the same dot `(Actor{"", 0}, 1)` and identical `C`-entry context (or mutually covering contexts), differing only in a 2-byte `value` (hence distinct `VersionID`s). Because all pairs have equal event sets, both `w.Descends(v)` and `v.Descends(w)` scan all `C` entries and `Supersedes` returns `false`, so the inner loop never breaks early: `Θ(S^2 · C log C)` work. At `B = 64 KiB` (`FuzzOCCDecode` cap), `C = 0` yields `S ≈ 5,950` (~70M `Descends` calls per `Decode` or `Record.Merge`); at `B = 16 MiB` (`sim.DefaultMaxMessage`, accepted by `Node.OnRequest(msgPush)` and `onDigestReply`), `S ≈ 1.5M` yields `~2 × 10^12` `Supersedes` calls (a multi-hour CPU DoS from one wire message). Additionally, `d.record()` (`encoding.go:209`) calls `NewRecord(vs...)` (running `maximal`) even when `d.err != nil` on a malformed input, and `d.version()` / `d.vector()` similarly run `NewVersion` (two SHA-256s) / `NewVersionVector` after `d.err != nil`.
  - *`FuzzOCCDecode` timeout cause:* On every valid decoded input, `FuzzOCCDecode` calls `checkLaws` twice (`prop_test.go:295, 298`). `checkLaws` iterates `append(a.Keys(), b.Keys()...)` without deduplicating shared keys and runs two `O(S^2)` loops calling `strictlyBelow(s, t)` (`prop_test.go:68-89`), which recomputes `events(s)` and `events(t)` from scratch on every pair — allocating two `map[occ.Dot]bool` of up to `64 · C` entries per call. A valid 28 KiB input with `S = 400`, `C = 20`, `Counter = 64` executes ~640k `strictlyBelow` calls = 1.28M map allocations and ~1.64 billion map insertions on a single fuzz execution, stalling the worker at 0 exec/s until `context deadline exceeded`.
- **Reproduction:**
  ```go
  // Construct a ~28 KiB valid canonical Doc with 400 siblings on key "k",
  // each having dot ("",0):1, 20 context entries (("",0..19):64), and unique 2-byte value.
  // Passing its Encode() bytes to FuzzOCCDecode (or Decode + checkLaws) stalls for tens of seconds;
  // scaling to 1 MiB (S ≈ 95,000, C = 0) stalls Decode/Record.Merge alone for minutes.
  ```
- **Smallest fix:**
  1. In `encoding.go`: check `if d.err != nil { return ... }` before calling `NewVersion`, `NewVersionVector`, and `NewRecord`.
  2. In `version.go`: replace `v.ctx.Get(e.Actor)` inside `Descends` with a linear two-pointer merge scan (`O(len(v.ctx) + len(w.ctx))`).
  3. In `record.go` / `doc.go`: in `maximal`, deduplicate/group versions by identical `(dot, ctx)` before pairwise dominance comparisons (so forged same-context siblings are compared once), and enforce an explicit per-record sibling bound in `Decode` (returning `ErrMalformed` above the bound) so a single 16 MiB wire message cannot trigger `Ω(B^2)` work in `Decode` or `Record.Merge` even with distinct concurrent contexts.
  4. In `prop_test.go`: deduplicate `keys` in `checkLaws`, and precompute `events(v)` once per sibling in `union` (or compare downsets without allocating maps inside the `O(S^2)` loop).

### 2. MAJOR — `Doc.MaxStamp()` and `Store.Merge()` allow one far-future stamp to disable HLC synchronization cluster-wide, and corrupt HLC under negative clock skew
- **Where:** `internal/occ/doc.go:136-151` (`Doc.MaxStamp`), `internal/occ/store.go:45-47` (`newStore`), `internal/occ/store.go:143-148` (`Store.Merge`).
- **What is wrong:**
  1. *Cluster-wide HLC poisoning:* `Store.Merge(o)` calls `s.hlc.Observe(o.MaxStamp())` only on the single global maximum stamp in `o`. If any one key in `o` holds a far-future stamp (`Wall > local + MaxClockLead`, which `Store.Merge` intentionally keeps in `s.doc`), `o.MaxStamp()` always returns that stamp, `Observe` fails with `ErrClockAhead`, and **none of the valid in-range stamps on any other key in `o` are observed**. Because replication exchanges full documents, a single far-future stamp on one key permanently disables HLC `Observe` across all honest nodes for all scalar fields until real time passes that stamp.
  2. *Empty / pre-epoch `MaxStamp()` zero-value bug:* `Doc.MaxStamp()` initializes `var m Stamp` (`Wall: 0`). On an empty `Doc` (or a `Doc` containing only `Wall < 0` stamps), it returns `Stamp{Wall: 0}` instead of reporting absence / the negative max. When a node starts at `t = 0` with negative clock skew (as in `TestMultiRegionConvergenceDST`, skew `±400ms`), `newStore` calls `s.hlc.Observe(Stamp{Wall: 0})`, prematurely advancing `HLC.wall` to `0` ahead of the node's own clock. If a node's skew is `< -MaxClockLead` (e.g. `-2h`, valid under `sim.MaxSkew = 2^61`), `newStore` on an empty doc and every `Store.Merge(NewDoc())` fail `Observe` with `ErrClockAhead` and increment `AheadStamps`.
- **Reproduction:**
  ```go
  // (a) Far-future stamp on unrelated key breaks LWW causal takeover on "b1":
  fast, fc := newTestStore(t, "fast", 1, 1)
  slow, sc := newTestStore(t, "slow", 1, 2)
  fc.now = env.Time(10 * time.Second)
  sc.now = env.Time(9 * time.Second)
  fast.SetRoute("b1", "region-a")
  poison := occ.NewDoc()
  poison.MergeScalar("unrelated", occ.Scalar{Stamp: occ.Stamp{Wall: int64(fc.now) + int64(2*occ.MaxClockLead), Node: "rogue"}})
  fast.Merge(poison)
  slow.Merge(fast.Doc()) // Observe rejects "unrelated" and skips "b1"'s 10s stamp
  sc.now = env.Time(9500 * time.Millisecond)
  slow.SetRoute("b1", "region-c")
  r, _ := slow.Route("b1") // returns "region-a" instead of "region-c"!

  // (b) Negative skew at startup on an empty doc:
  c := &fakeClock{now: env.Time(-2 * time.Hour)}
  s, _ := occ.NewStore(occ.StoreConfig{Node: "a", Epoch: 1, Clock: c, Rand: rand.New(rand.NewPCG(1, 1))})
  // s.AheadStamps() == 1 on an empty document.
  // With c.now = env.Time(-400 * time.Millisecond), s.HLC().Now() returns Wall=0, Logical=1 instead of Wall=-400ms, Logical=0.
  ```
- **Smallest fix:** Have `newStore` and `Store.Merge` iterate over the actual stamps present in the `Doc` (records' siblings and scalars in sorted key order) and call `s.hlc.Observe(st)` on each (counting `ErrClockAhead` per rejected stamp or once per merge), doing nothing when the doc has no stamps. Update `Doc.MaxStamp()` to track `has bool` (or return `(Stamp, bool)`) so negative-wall docs return their real maximum stamp rather than `Stamp{}`.

### 3. MAJOR — `Node` write methods mutate in-memory state before `persist()` and do not roll back on disk write/sync errors
- **Where:** `internal/occ/node.go:359-413` (`Node.Put`, `Node.Delete`, `Node.SetScalar`, `Node.UpdateQuota`) and `internal/occ/node.go:291-296` (`OnRequest` `msgPush`).
- **What is wrong:** Every write method mutates `n.store` (`doc`, `hlc`, and potentially `actor.Epoch`) in memory first, then calls `n.commit(key)` -> `n.persist()`. In `internal/sim`, `Disk.Write`/`Disk.Sync` fail only on a closed handle, masking this path. In Chunk 6's real file-backed runtime (`env.Disk`), `Write` or `Sync` can return `ErrIO` (e.g. `ENOSPC`, `EIO`) while the node is live. When that happens, `Node.Put`/`Delete`/`SetScalar`/`UpdateQuota` return an error (refusing the write to the caller), yet leave the mutated record/scalar live in `n.store.doc`. Subsequent local `Get`s return the failed write, conditional writes chain onto its ETag, and periodic sync (`round`/`OnRequest`) replicates the unacknowledged, unpersisted write to peers.
- **Reproduction:** Construct an `occ.Node` with a custom `env.Disk` whose `Write` (or `Sync`) returns `env.ErrIO` after startup. Call `_, err := n.Put("k", []byte("v"), occ.IfNoneMatch())`. `err` is non-nil (`env.ErrIO`), yet `val, _, gerr := n.Get("k")` immediately returns `"v", etag, nil`, and a peer's `msgDigest` pull receives `"v"`.
- **Smallest fix:** Snapshot the previous record/scalar entry and `actor.Epoch` before calling `n.store.*`, and restore them on `n.persist()` failure (or persist the encoded candidate state before installing it into `n.store.doc`).

### 4. NIT — All `f.Add` seeds in `FuzzOCCConditionalWrite` and `FuzzOCCMergeLaws` silently skip during `go test`
- **Where:** `internal/occ/prop_test.go:328-335` (`FuzzOCCConditionalWrite`), `internal/occ/prop_test.go:519-525` (`FuzzOCCMergeLaws`).
- **What is wrong:** Every `f.Add` seed slice (0–64 bytes for `FuzzOCCConditionalWrite`, 0–128 bytes for `FuzzOCCMergeLaws`) is too short for `rapid.MakeFuzz` to finish drawing even one case, so all 9 seeds silently `SKIP` on every `go test ./internal/occ` run (`--- SKIP: FuzzOCCConditionalWrite/seed#0..4`, `--- SKIP: FuzzOCCMergeLaws/seed#0..3`).
- **Reproduction:** `docker compose run --rm -T dev go test -v -run '^FuzzOCC(ConditionalWrite|MergeLaws)$' ./internal/occ`.
- **Smallest fix:** Add at least one longer boundary seed (e.g. 1024 bytes) to each target so normal `go test` executes the property on the seed corpus rather than skipping all seeds.

### 5. NIT — Typed metadata write helpers live only on `*Store`, not `*Node`, creating an integration footgun for Chunk 6
- **Where:** `internal/occ/meta.go:127-191`, `internal/occ/node.go:90, 352-413`.
- **What is wrong:** `Node.Store()` exposes the mutable `*Store` directly, and the typed mutators (`PutTag`, `PutACL`, `PutObjectMeta`, `PutManifest`, `SetRoute`, `SetContentType`) are defined only on `*Store`. If Chunk 6 calls `n.Store().PutObjectMeta(...)` or `n.Store().SetRoute(...)`, the write mutates memory without calling `n.commit(key)` (never persisted to disk and never broadcast via delta). Chunk 6 can still use `n.Put(occ.ObjectKey(...), m.Encode(), p)` and `n.SetScalar(occ.RouteKey(...), ...)` directly, so it is usable, but exposing the typed write helpers on `*Node` removes the trap.
- **Smallest fix:** Add forwarding methods on `*Node` (e.g. `PutTag`, `PutACL`, `PutObjectMeta`, `PutManifest`, `SetRoute`, `SetContentType`) that go through `n.Put`/`n.SetScalar` (and hence `n.commit`), or document/restrict `Store()` to read-only access.

### 6. NIT — Minor hardening in `TestQuotaOvershootUnderPartitionDST` and `Node.peers()`
- **Where:** `internal/occ/dst_test.go:580-586` (`quotaRun`), `internal/occ/node.go:186-197` (`Node.peers`).
- **What is wrong:**
  1. `quotaRun` does not assert `n.Store().Record(qk).Len() > 1` before calling `n.UpdateQuota` to prove siblings were actually formed and resolved by the recount hook (unlike `offlineRun`, which explicitly asserts `contested > 0`).
  2. `Node.peers()` indexes into the slice returned by `Membership.Peers()` using `n.e.Rand.IntN(len(ps))` without sorting `out` first, trusting every future `Membership` implementation to return peers in canonical order.
- **Smallest fix:** Assert `if n.Store().Record(qk).Len() <= 1 { s.Fail(...) }` before the resolving `UpdateQuota` in `quotaRun`; add `slices.Sort(out)` in `Node.peers()`.

---

## Verification summary

### Verified by running (in Docker `dev` container)
- `docker compose run --rm -T dev go test -v -count=1 -cover ./internal/occ/...` — PASS, 1.58s, 97.3% statement coverage; confirmed all named unit, property, DST, and canary tests pass, and observed all 9 `f.Add` seeds in `FuzzOCCConditionalWrite` and `FuzzOCCMergeLaws` printing `SKIP`.
- `docker compose run --rm -T dev go test -race -count=1 ./internal/occ` — PASS (6.0s).
- `docker compose run --rm -T dev go test -count=1 ./internal/occ -rapid.checks=2000` — PASS (9.0s).
- `docker compose run --rm -T dev sh -c 'DST_SEEDS=50 go test -count=1 -run "DST|Canary" ./internal/occ'` — PASS (13.3s).
- `git diff --name-only main..HEAD` — confirmed only `internal/occ/` and `agent_work/04-occ/` were modified; frozen simulator packages (`internal/env`, `internal/sim`, `internal/sim/simtest`) and `agent_work/gates/panic-allowlist.txt` are untouched.
- `go list -f '{{.Imports}}' ./internal/occ` and `go list -deps ./internal/occ` — confirmed standard library plus `internal/env` only (no external deps in product code).
- Grep over `internal/occ/*.go` — confirmed zero comments, zero panics, zero goroutines/channels/mutexes/`sync/atomic`/wall clock/global rand/`maphash`/`%p`/floats in product code (`float64` appears only in `bench_test.go:41` for `b.ReportMetric`).

### Verified by reading
- Read `PLAN.md` (sections 2, 4.2 Chunk 4, 5.1, 7.1, 7.5, 9.1), `agent_work/04-occ/objstore-p2-04-occ-build.md`, `agent_work/04-occ/objstore-p2-builder-rules.md`, `agent_work/04-occ/build-notes.md`, `agent_work/04-occ/gates/result.json`, `agent_work/0b-simulator/build-notes.md`, `agent_work/0b-simulator/frozen-api.txt`, and every `.go` file in `internal/occ/`.
- Verified mathematically that `Version.Descends` / `Version.Supersedes` (`version.go:46-64`) exactly computes `events(w) ⊆ events(v)` and `events(w) ⊊ events(v)` for all `Dot.Counter >= 1`, making `Record.Merge` commutative, associative, idempotent, and total, and that whole-record replication preserves the dot-inflation causality invariant in honest runs.
- Verified every PLAN 4.2 named test exists, every PLAN 2.6 error sentinel and code row (80–87) is produced and tested with `errors.Is`/`errors.As`, the independent oracles (`oracle`, `model`, `strictlyBelow`) genuinely test their claims, `TestCanaryMergeDropsSibling` fails for the planted sibling-dropping bug, and all map iterations in product code (`doc.go:37, 46`) use collect-then-sort.
- Identified Findings 1, 2, 3, 5, and 6 by code analysis (and Finding 4 by test output + code analysis).
