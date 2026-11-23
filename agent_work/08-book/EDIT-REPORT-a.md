# Consistency Edit Report — Chapters 1 to 4

## Verification summary

- `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src`: `site-lint: ok (0 findings)` (21 pages built cleanly).
- `DRAW_VISUAL_MAX_WIDTH=80 python3 /root/.claude/skills/draw-visual/scripts/render.py --check` on all 4 chapters: all 8 embedded diagrams checked clean (no `.mmd` sources changed).
- All 16 Go code excerpts across chapters 1–4 verified byte-for-byte identical against their named repository source files.
- All 4 edited chapters sit within the 1,800–2,600 word target (`wc -w`).

---

## Chapter 1: `book/chapters/01-deterministic-simulation.md`

- **Words (`wc -w`):** 2,715 before → 2,572 after (−143).
- **Headings renamed:**
  - `## How the simulator fits this repository` → `## How deterministic simulation maps to this repository`
  - `### The environment boundary (\`internal/env\`)` → `### The environment boundary`
  - `### The simulation engine (\`internal/sim\`)` → `### The simulation engine`
  - `### Test plumbing and CLI (\`internal/sim/simtest\`, \`cmd/objsim\`)` → `### Test plumbing and CLI`
  - `### Keeping the simulator honest` → `### Testing on the simulator`
  - `### What the simulator does not prove` → `### What the tests do not prove`
- **Excerpts corrected:**
  - `internal/sim/stream.go` (lines 10–19, 10 lines): restored the omitted comment line `// fnv64a computes the 64-bit FNV-1a hash of b.` between `NewStream` and `fnv64a` so the excerpt is an exact copy of the file.
  - `internal/sim/queue.go` (6 lines), `internal/sim/meta_test.go` (12 lines), and `internal/sim/simtest/determinism.go` (20 lines): already exact.
- **Paths or names fixed or removed:**
  - Removed `- **Adversarial harness and results:** \`agent_work/adversarial/0b-simulator/\` (\`ATTACKS.md\`, \`RESULTS.md\`, and \`run-all.sh\`).` from `## Go deeper in the repo` (operational `agent_work/` path and chunk prefix violate the self-containment rule; all adversarial results remain in prose).
  - Replaced `cmd/objsim` in running text with `` `objsim` (`cmd/objsim`) `` to match vocabulary rules; replaced stale "in a later chapter" / "not built yet; each will arrive in a coming chapter" references with relative links to Chapters 2–7.
  - Verified all remaining repository paths, test names (`TestReplaySameProcess`, `TestReplayAcrossProcesses`, `TestGoldenDigests`, `TestDifferentSeedsDiffer`, `TestCanaryUnseededDraw`, `TestCanaryMapOrder`, `TestCanaryDroppedInvariant`, all 8 `TestProp*` in `internal/sim/prop_test.go`), benchmark (`BenchmarkChaos100`), fuzz target (`FuzzSimSchedule`), CLI scenarios (`toy-chaos-5`, `toy-chaos-20`, `toy-chaos-50`, `toy-chaos-100`), and command flags exist.
- **Cross-chapter disagreements:**
  - `detlint` checked file/package count (Chapter 1 vs Chapter 7 — neither changed):
    - Chapter 1 (`book/chapters/01-deterministic-simulation.md`): `` `detlint` checks simulated product code (`internal/env`, `internal/sim`, `internal/sim/simtoy`, and future protocol packages — 15 files in 3 packages today), not `_test.go` files or `internal/sim/simtest`. ``
    - Chapter 7 (`book/chapters/07-object-store-and-sync-engine.md`): `determinism lint passed on 72 files in 11 packages including internal/node`
- **Could not verify:** Historical throughput, sweep timings, coverage percentages, and adversarial attack run counts were preserved as written without re-running full sweeps or benchmarks; the two shell commands were verified against package paths, test regexes, scenario names, and flags (`-seed`, `-scenario`) without executing them.

---

## Chapter 2: `book/chapters/02-swim-gossip.md`

- **Words (`wc -w`):** 2,796 before → 2,579 after (−217).
- **Headings renamed / added:**
  - `### Merge lattice and self-refutation (\`member.go\`, \`node.go\`)` → `### Merge lattice and self-refutation`
  - `### Probing and indirect relays (\`node.go\`)` → `### Probing and indirect relays`
  - `### Suspicion, piggybacking, and burial (\`config.go\`, \`node.go\`)` → `### Suspicion, piggybacking, and burial`
  - `### Join, sync, leave, and wire format (\`wire.go\`, \`node.go\`)` → `### Join, sync, leave, and wire format`
  - Added `### Testing on the simulator` after `### Design choices and what each costs` to match the shared chapter shape.
- **Excerpts corrected:**
  - None drifted. All four excerpts (`Supersedes` in `internal/gossip/member.go`, 9 lines; `onAck`, 17 lines; `startSuspicion`, 17 lines; `bury`, 19 lines in `internal/gossip/node.go`) matched their source files verbatim.
- **Paths or names fixed or removed:**
  - Removed `- **Adversarial harness and results:** \`agent_work/adversarial/01-gossip/\` (\`RESULTS.md\`, \`run-all.sh\`, \`main.go\`, \`extra.go\`).` from `## Go deeper in the repo` (operational `agent_work/` path and chunk prefix; all adversarial findings remain in prose).
  - Replaced "in coming chapters" references with relative links to Chapters 3–8.
  - Verified all remaining repository paths, testdata files (`wire-golden.txt`, `merge-seeds.txt`, `seeds.txt`), symbols (`Codes()`, `CodeTable()`), test names, fuzz targets (`FuzzGossipWire`, `FuzzViewMergeLaws`), and the sharded replay command (`seed=33` maps to `shard=1` because `33 % 4 == 1` in `shardedSweep`).
- **Cross-chapter disagreements:** None observed for Chapter 2.
- **Could not verify:** Historical 500-seed sweep measurements, adversarial counts, race/fuzz timings, and coverage percentages were preserved verbatim without re-running them; the two shell commands were checked statically against `internal/gossip/dst_test.go` without executing them.

---

## Chapter 3: `book/chapters/03-reed-solomon-erasure-coding.md`

- **Words (`wc -w`):** 2,866 before → 2,591 after (−275).
- **Headings renamed:**
  - `## How erasure coding fits this repository` → `## How erasure coding maps to this repository`
  - `### Finite-field math and codec (\`internal/erasure\`)` → `### Finite-field math and codec`
  - `### Shards, chunk store, and manifests (\`internal/storage\`)` → `### Shards, chunk store, and manifests`
  - `### Placement and the striped node (\`placement.go\`, \`stripe.go\`, \`shardproto.go\`, \`node.go\`)` → `### Placement and the striped node`
- **Excerpts corrected:**
  - None drifted. All four excerpts (`MulAddSlice` in `internal/erasure/gf256.go`, 17 lines; `CodingMatrix` in `internal/erasure/matrix.go`, 18 lines; `shardExpect.verify` in `internal/storage/stripe.go`, 17 lines; `regionCap` in `internal/storage/placement.go`, 18 lines) matched their source files verbatim.
- **Paths or names fixed or removed:**
  - Removed `- **Adversarial harness and results:** \`agent_work/adversarial/02-erasure/\` (\`ATTACKS.md\`, \`RESULTS.md\`, and \`run-all.sh\`).` from `## Go deeper in the repo` (operational `agent_work/` path and chunk prefix; all adversarial findings remain in prose).
  - Replaced "arrive in coming chapters" / "wait for coming chapters" with relative links to Chapters 2, 4, 5, and 7.
  - Verified all remaining repository paths, symbols (`storage.Codes()`, `storage.StripeGroup`, `PlantSkipChecksum`), test names, canaries, benchmarks (`BenchmarkEncode1MiBStripe`, `BenchmarkReconstructTwoDataShards1MiBStripe`, `BenchmarkReconstructAllMissing1MiBStripe`, `BenchmarkStripedPutGet8MiB`, `BenchmarkStripedPutGet32MiB`), fuzz targets (`FuzzGF256`, `FuzzErasureCoding`, `FuzzShardDecode`), and command flags.
- **Cross-chapter disagreements:**
  - Product file count in `internal/erasure` + `internal/storage` vs later additions in Chapter 7 (neither changed):
    - Chapter 3 (`book/chapters/03-reed-solomon-erasure-coding.md`): `` `detlint`, `go vet`, `gofmt`, `paniccheck`, and stdlib-only imports passed on all 12 product files. ``
    - Chapter 7 (`book/chapters/07-object-store-and-sync-engine.md`) adds `internal/storage/api.go` and `internal/storage/httpapi/` (`service.go`, `http.go`), so `internal/erasure` (4) + `internal/storage` (9) now totals 13 non-test files outside `httpapi/`, though Chapter 3 documents the 12 erasure/storage files of this layer.
- **Could not verify:** Historical benchmark MB/s, memory peaks, 500-seed sweep counts, fuzz execs, coverage, and adversarial check counts were preserved verbatim without re-running them; the two shell commands were verified statically without executing them.

---

## Chapter 4: `book/chapters/04-merkle-anti-entropy.md`

- **Words (`wc -w`):** 2,956 before → 2,596 after (−360).
- **Headings renamed:**
  - `## How Merkle anti-entropy fits this repository` → `## How Merkle anti-entropy maps to this repository`
  - `### Persistent fanout-16 trie (\`tree.go\`)` → `### Persistent fanout-16 trie`
  - `### Level-by-level diff protocol and wire format (\`diff.go\`, \`wire.go\`, \`errors.go\`)` → `### Level-by-level diff protocol and wire format`
  - `### Background scrubber and repair worker (\`worker.go\`)` → `### Background scrubber and repair worker`
- **Excerpts corrected:**
  - None drifted. All four excerpts (`leafHash`, 15 lines, and `newInner`, 15 lines in `internal/merkle/tree.go`; `Session.Feed`, 22 lines, and `diffLeaf`, 20 lines in `internal/merkle/diff.go`) matched their source files verbatim.
- **Paths or names fixed or removed:**
  - Removed `(\`agent_work/adversarial/03-merkle/\`)` from the adversarial testing bullet in `## What the tests show, and what they do not` and removed `- **Adversarial harness and results:** \`agent_work/adversarial/03-merkle/\` (\`ATTACKS.md\`, \`RESULTS.md\`, and \`run-all.sh\`).` from `## Go deeper in the repo` (operational `agent_work/` path and chunk prefix; all adversarial findings remain in prose).
  - Replaced "arrive ... in coming chapters" / "waits for coming chapters" with relative links to Chapters 2, 5, and 7.
  - Verified all remaining repository paths, fuzz corpus file (`internal/merkle/testdata/fuzz/FuzzMerkleWire/2104aef022c948e1`), symbols (`SetSkipBranch`, `MERKLE_MEASURE`, `BitRotRepairBound`, `CleanSettle`, `MissedWriteBound`, `LossConvergeBound`, `LyingConvergeBound`, `refillBound`), test names, fuzz targets (`FuzzMerkleWire`, `FuzzMerkleDiff`), and commands.
- **Cross-chapter disagreements:** None observed for Chapter 4.
- **Could not verify:** Historical diff-cost table numbers, 500-seed sweep maxima, fuzz/race timings, coverage percentages, and adversarial check counts were preserved verbatim without re-running them; the two shell commands were verified statically without executing them.
