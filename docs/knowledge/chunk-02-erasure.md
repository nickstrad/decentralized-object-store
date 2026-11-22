# Chunk 2 — erasure coding and the chunk store: findings, narrowed claims, pitfalls

What was built, the shard and manifest formats and every measurement are in
`agent_work/02-erasure/build-notes.md`; the outside harness and its one narrowed check are in
`agent_work/adversarial/02-erasure/RESULTS.md`. This entry holds what a later builder must
know and would not guess.

## Findings

- `ChunkStore.Put` returning nil means the shard is in the durable layer, on every path,
  including a repeat `Put` of identical bytes (it syncs again). `Delete` syncs before it
  returns, also when it returns `ErrNotFound`. Other bytes for a stored valid shard are
  `ErrExists`; a stored blob that fails verification is overwritten
  (`TestPropChunkStoreModel`, which models the written and durable layers and crashes).
- A `Node.Put` or `Node.Get` callback fires exactly once and never inside the call that
  registered it: early errors and the empty object go through `Clock.AfterFunc(0)`
  (`TestNodeCallbacksNeverRunInsideTheCall`).
- The code corrects erasures at known positions; it does not locate errors. Storage finds a
  bad shard (header checks, SHA-256, manifest checksum) and passes it to the decoder as nil.
  A forged shard with a self-consistent checksum is stopped only by the manifest checksum
  (`TestDecodeStripeRejectsForgedShards`). No product code calls `Codec.Verify`.
- Valid codes: `k ≥ 1`, `m ≥ 0`, `k + m ≤ 256`; huge `k`, `m` are errors, not panics.
- GET uses the layout recorded in the manifest, so one cluster can read objects written with
  different `(k, m)` (`TestNodeGetUsesTheManifestLayout`). PUT uses the node's
  `Config.Layout`.
- A PUT is acknowledged when every stripe has `AckQuorum` (default `k+1`) durable shards; the
  DST checks this inside every PUT callback and saw the minimum, 5 of 6, over 500 seeds.
- Wire codes: storage owns block 96–111 and uses 96–100 (`CodeNotFound` … `CodeInvalid`).
  Project blocks: gossip 80–95, storage 96–111, merkle 112–127, occ 128–143, saga 144–159.
  Pass `storage.Codes()` as `sim.Config.Codes`; the integration chunk merges the tables.
- `Place` is stateless and cannot keep shard indices stable when a holder leaves. `Replace`
  starts from the placement in the manifest and moves only the shards it must.
- Measured in the `dev` container on a shared 4-core machine: encode 4+2, 1 MiB stripe, about
  480 MB/s; reconstruct two shards 300–390 MB/s (varies with load). PUT then GET of 8 MiB
  and 32 MiB on 9 simulated nodes: about 5 and 7 MiB of live heap beyond the simulated disks
  and the returned object. 500-seed `TestStripedPutGetDST`: about 175 s; PUT ack at most
  298 ms, GET at most 643 ms within budget (872 ms lifted), in virtual time.

## Narrowed claims

- `Window` (default 4) bounds the stripes launched and not yet at quorum (PUT) or not yet
  decoded (GET). It does not bound requests still outstanding to slow holders: after the
  ack each stripe can have `N − AckQuorum` requests (1 at the defaults) outstanding for up
  to `RequestTimeout` (1 s), each holding one shard blob. `NodeStats.PutOutstanding` and
  `PutOutstandingBytes` count them. The harness check `window-pending-stripes` measures
  those requests and still prints BROKEN.
- Within the simulator's fault budget, permanent damage (bit-rot, wipe) never makes a GET
  unavailable. A transient read error can, if such errors keep arriving on more than `m`
  shards of a stripe for longer than four attempts per shard; then `env.ErrIO` is among the
  causes. None seen in 46,593 GETs over 500 seeds.
- The budget is instantaneous and a GET spans time. Seed 987691 (pinned in
  `internal/storage/testdata/seeds.txt`) put read errors on three shards of one stripe one
  after another, never more than two at once; that is why GET retries transient errors.
  Any protocol whose operations span time needs such retries or a narrower claim.
- The DST does not crash a node during the PUT phase. Durability across a crash is proven by
  the chunk-store model test, the durable count at ack by the DST.

## Left for the integration chunk

- `Node.Get` returns the whole object in one buffer and allocates `Manifest.Size` up front:
  no object-size cap, no per-stripe streaming. `Node.Put` takes no layout argument.
- The manifest is returned to the caller, not stored. No tombstones, shard garbage
  collection or background repair. `VersionID` is a random `uint64` from `env.Rand`.
- `Membership` is declared in `internal/storage`; only `StaticMembership` implements it.

## Pitfalls

- The gate's determinism lint does not list `internal/storage`; run detlint on it by hand.
  HTTP imports in that package would fail the lint.
- A rapid property used as a fuzz seed is skipped, not run, when its seed is too short
  ("ran out of input"). `TestFuzzSeedsRunToCompletion` fails on a skipped seed; seeds are
  4,096 bytes.
- With the shard checksum turned off (the canary), the DST shows `ErrCorrupt` from the
  end-to-end content hash, not wrong bytes. `TestPropCorruptionBecomesErasure` is the
  direct proof.
