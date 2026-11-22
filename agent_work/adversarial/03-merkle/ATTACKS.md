# Merkle independent test report

## Result and scope

**70 HELD checks, 4 BROKEN checks, 0 setup errors; final runtime 76 seconds.** The four counterexamples concern
reply size and cost bounds. No missed divergence, structural root collision, invalid
repair write, or unbounded session was observed in the tested cases. Passing finite
checks are evidence for their inputs, not a proof for all inputs.

Product revision: `ad6c99738212d3985d06c9f19b25defb2e2464a6`.
Task: `agent_work/03-merkle/objstore-p2-03-merkle-adversarial.md`.
Sources: `PLAN.md` sections 4.2/7.4, the builder's claims table, the Merkle source/tests,
and the simulator build notes/frozen API. All authored files are under this directory.
No product edits, git writes, or commits were made. The latest complete run is recorded
in [evidence/run-all.txt](evidence/run-all.txt); the final timing is in its summary.

Run from the worktree root:

```bash
bash agent_work/adversarial/03-merkle/run-all.sh
```

The expected current exit is **1** because four checks reproduce broken claims.
Drivers return 0 for all HELD, 1 for any BROKEN, and 2 for setup failure. Setup lines
are explicitly identified and are never counted as product failures. Four no-argument
drivers run independently: `a01-tree.sh`, `a02-wire.sh`, `a03-sim.sh`, and
`a04-regression.sh`. All Go builds and executions use `docker compose run --rm -T dev`
(with an explicit optional environment variable for selecting one independent check).
The single nested module uses only the standard library and the local product module.
Container-local executables are removed on exit; evidence files are run results.

The 40 independent checks use exported APIs, an independently encoded recursive hash
oracle, a map-union diff oracle, and a new disk-backed simulator fixture. The 34
supplemental checks rerun existing tests and fuzz seed corpora; their names start with
`regression-` so they are not represented as independent implementations. Raw test
output is in [evidence/regression.jsonl](evidence/regression.jsonl).

## Numbered claims and observed checks

C1–C28 follow the builder's claims-table order. C29 and C31 are additional plan
obligations; C30 exercises failure paths the builder identified as uncovered.
Names in the third column refer to independent checks in the evidence. All C1–C28
also have supplemental existing-test coverage, explicitly mapped in `regression.py`.
A BROKEN label applies to the stated subclaim, not every behavior in that row.

| Claim | Published or required behavior | Independent checks and observed result |
|---|---|---|
| C1 | Pinned/reference hash roots | `reference-roots`: HELD at depths 1–16, 132 entries including binary/empty keys; existing vectors also passed. |
| C2 | Insert/update/delete maintain Len/Get/Root; complete deletion yields zero | `model-updates`: HELD for 3,000 operations at depths 1, 3, 16, including repeated hashes and missing deletes. Sorted-accessor/string regressions passed too. |
| C3 | Empty root is zero; a single difference needs Depth+1 trips; self-diff needs 1 trip/1 hash | `empty-single-equal`: HELD for all 16 depths in both empty and nonempty cases. |
| C4 | Depth limits and DepthFor | `depth-boundaries`: HELD for four invalid depths in New/Build and nine boundary counts. |
| C5 | Key/duplicate limits and leaf capacity | `key-and-duplicate-limits`, `leaf-cap`: HELD; 1024 accepted/1025 rejected, duplicates rejected, 4096 entries accepted/4097 rejected, failures atomic, replacement still allowed. |
| C6 | Snapshots survive later writes | `snapshot-and-returned-slices`, `mutate-during-diff`: HELD; writes in both directions, returned slices changed, and initiator writes/deletes between all protocol steps. |
| C7 | Level-by-level protocol, stable requests, 35-byte root request and 1-byte equal reply | `protocol-state`: HELD for every level and pre/post-session misuse. |
| C8 | Leaf replies are bounded to 1 MiB, split, and complete | `multi-leaf-batching`: HELD, 4,000 long keys, seven trips, exact results; `single-leaf-reply-size`: **BROKEN**, one valid leaf emits 1,049,541 bytes. |
| C9 | Frontier cap truncates honestly and repeated rounds converge | `small-frontier-convergence`: HELD at caps 1, 3, 7 with 140 keys, checking every reported hash/presence bit and requiring progress. |
| C10 | Golden/canonical wire encoding; malformed inputs reject | `malformed-wire`: HELD for 13 truncated, unordered, noncanonical and wrong-kind messages; existing golden/identity tests also passed. |
| C11 | Diff contains exactly all differing/one-sided keys and their hashes | `diff-oracle`: HELD for 400 directional comparisons at randomized depths; batching and capped-repair checks also compare with explicit expectations. |
| C12 | Hashes ≤ 1+16·d·Depth+d·LeafLoad(remote), trips ≤ Depth+1, equal trees one trip | `cost-grid`: HELD for 18 size/divergence pairs through N=100,000; `batched-round-trip-bound`: **BROKEN** for the unconditional trip bound, with 984 long keys at depth 1; the leaf-load hash bound holds. |
| C13 | Equal roots iff equal maps | `structure-boundaries`: HELD for eight distinct maps at each of 16 depths, including empty versus present zero, binary/boundary shifts, and a 514-byte zero key; reference roots and randomized existing map comparisons passed. |
| C14 | Incremental changes equal bottom-up Build | `rebuild-sequence`: HELD through 500 transitions, matching root, length and maximum leaf load. |
| C15 | Insertion order does not affect roots | `permutations`: HELD for 36 shuffled constructions of 257 entries across depths 1, 4, 16. |
| C16 | Changing/fabricated replies end within Depth+Frontier with permitted errors | `changing-responder`: HELD for 200 sessions with changing trees, depths 1–16 and caps 1–16; `one-leaf-per-reply`: HELD, terminates in 17 trips at depth 1/cap 16 without changing local state. |
| C17 | Arbitrary wire bytes never panic; errors are classified; successful responses decode | `random-wire`: HELD for 20,000 seeded byte strings as both requests and replies; protocol/size checks feed real Answer outputs; both existing fuzz seed corpora passed. |
| C18 | Rot detection/repair is bounded, fetch bytes proportional to damage; clean replicas fetch zero | `sim-rot`, `sim-clean`: HELD across eight seeds each; five simultaneous damaged chunks repair within 1.357081592 s, fetched bytes ≤4× damage, no clean fetches. Existing during-traffic scenarios also passed. |
| C19 | Missed writes recover within 20 s after partition heal | `sim-partition`: HELD, eight seeds, 37 writes on one side of a two-second isolation, max 4.421262303 s after heal. |
| C20 | Under 20% link loss rounds fail/retry/account correctly and converge within 60 s | `sim-loss`: HELD, eight seeds, every directed link at 200,000 PPM, max 53.602941719 s. Each run required failures, convergence and round conservation. |
| C21 | Fabricated peers cannot persist invalid bytes, honest replicas converge, peer rounds stay ≤Depth+1 | `sim-invalid-fetch`: HELD, eight seeds, ErrRejected and zero invalid puts, then recovery; `sim-prefix-reply-tight-bound`: **BROKEN**, actual worker uses three trips at depth 1 for two leaves. Existing mixed-peer convergence and validity checks passed. |
| C22 | Scrub notices disk wipe and read errors; refills data | `sim-wipe`, `sim-read-error`: HELD, eight seeds each; maximum wipe refill 2.815342027 s, errors/drops counted and contents verified. |
| C23 | Peer vanishing mid-diff fails with transport error, no repairs, retry after restart works | `sim-peer-vanishes`: HELD on three seeds, crash on level 1, then all 37 chunks recovered after restart; existing 20-chunk scenario passed. |
| C24 | Malformed replies/requests fail with ErrMalformed, with remote error mapping | `leaf-path-validation`, `malformed-wire`: HELD, including a structurally valid leaf containing a key outside its requested path; existing worker/remote-error cases passed. |
| C25 | Excessive levels/depths and differing peer depths yield ErrDepth | `depth-wire`, `depth-boundaries`: HELD for API and protocol cases; existing registered/unregistered transport-code tests passed. |
| C26 | Every application code row round-trips | `registered-codes`: HELD for wrapped sentinels and ErrRemote; existing code-table tests passed. |
| C27 | Worker validates configuration and supports Record/Forget | `worker-config-hooks`: HELD for seven invalid settings, overlong Record, repeated Start, present/missing Forget and self-only membership. |
| C28 | The skipped-branch canary detects incomplete diff and failed convergence | Existing canary's completeness subcase and all three replication seeds passed; mutation hook exists only in the product's test build, so this is explicitly supplemental coverage. |
| C29 | Stored-tree corruption must not permanently hide damaged/missing data | `sim-tree-record`: HELD across eight seeds after five wrong cached hashes and a phantom entry, max 1.333294593 s; `sim-restart`: HELD, root rebuilt from durable chunks at restart. There is no persisted tree blob to corrupt. |
| C30 | Deadline callbacks and Put failures cannot cause stale writes or lost retry work | `sim-deadline-late-callback`, `sim-put-failure-cap`: HELD; three-second callbacks exceed two-second deadlines and never apply stale writes; MaxRepairs=3 defers work, Put errors surface, and 37 chunks eventually recover. |
| C31 | PLAN 7.4: hash count ≤16·d·depth, even interpreting depth as levels | `clustered-hash-cost`: **BROKEN**, 46 keys in one depth-2 leaf, one changed hash, 49 hashes >48; depth equals DepthFor(46). The refined builder bound remains valid. |

## Reduced reproductions for every BROKEN check

All four use fixed seed **731903**. Tree cases are deterministic constructions with
no random draws; the seed identifies the harness run. These are reduced within the
stated fixture families, not claims of global minimality over every possible byte string.
Each selection command prints only the named check and exits 1 on this revision.

### C8: one legal leaf exceeds MaxReplyBytes

```bash
MERKLE_CHECK=single-leaf-reply-size bash agent_work/adversarial/03-merkle/a01-tree.sh
```

Construct 992 distinct 1024-byte keys as follows: enumerate `i=0,1,...`, take
`fmt.Sprintf("ground-%08d", i)` followed by enough `z` bytes to reach length 1024,
and retain keys whose SHA-256 first nibble is zero. Assign each value hash to
SHA-256(key), then call `Build(1, entries)`. Send exactly hex `0101010100` to
`Answer`: request kind 1, depth 1, level 1, one path, path 0.

Both Build and Answer return nil error. Answer emits **1,049,541 bytes**, exceeding
`MaxReplyBytes=1,048,576`. The immediate 991-entry control emits **1,048,483 bytes**
and fits. At this key size, each entry uses 1,058 bytes and the framing uses five;
992 is the first exceeding count. `MaxLeafEntries=4096` permits this input.

Cause: `Tree.Answer` only stops adding leaves when it already has at least one
leaf; a single large leaf is always emitted whole. The per-leaf entry-count limit
alone does not bound encoded size to 1 MiB. This is an actual size-limit failure,
not an input-validation rejection and not a claim of corrupted data.

### C12: honest batching exceeds Depth+1 trips

```bash
MERKLE_CHECK=batched-round-trip-bound bash agent_work/adversarial/03-merkle/a01-tree.sh
```

Local tree: empty, depth 1. Remote: 984 entries where key `i` is
`fmt.Sprintf("key-%08d",i)` followed by `x` bytes to length 1024, for `i=0..983`;
value hash is SHA-256(key). Compare with frontier 16,384.

The result is complete, untruncated, and error-free, but needs **three trips**,
exceeding `Depth+1=2`. The otherwise identical 983-key control needs two trips.
The conservative leaf-size estimator makes this boundary appear before the exact
encoding reaches 1 MiB. Batching is useful and works; the unconditional trip claim
must account for extra batches. The broader `Depth+Frontier` bound still holds.

### C21: prefix leaf replies exceed the worker's tight trip bound

```bash
MERKLE_CHECK=sim-prefix-reply-tight-bound bash agent_work/adversarial/03-merkle/a03-sim.sh
```

Two simulator replicas, seed 731903, depth 1, period 200 ms, timeout 100 ms,
latency 1–3 ms. The local replica is empty. The remote stores the literal byte
strings `prefix-0` and `prefix-1`, under their SHA-256 hex keys. The harness verifies
that these keys occupy distinct leaves. The remote answers a multi-leaf request
with the first requested leaf only, using the normal exported Answer path; this
is a valid nonempty-prefix reply.

The local worker converges with valid data and balanced round accounting but records
**MaxRoundTrips=3**, exceeding two. Two different leaves are the minimum needed to
split a leaf reply at depth 1. This reproduces the builder's C21 tight-bound failure
through the actual Worker, not just Session. A separate 200-key session exercise
uses 17 trips, exactly the general `Depth+Frontier` upper bound. No infinite loop
or invalid write was observed.

### C31: clustered leaf entries exceed the plan's level-only hash bound

```bash
MERKLE_CHECK=clustered-hash-cost bash agent_work/adversarial/03-merkle/a01-tree.sh
```

Construct 46 distinct 32-byte keys by enumerating `ground-%08d` plus `z` padding,
retaining keys whose SHA-256 first byte is zero. Set value hashes to SHA-256(key).
Build at depth **2 = DepthFor(46)**. Snapshot it and replace just the first entry's
value hash with zero. Compare the original and snapshot.

The exact one-key difference is found without truncation, but **49 hashes** are
counted: one root, one nonzero child at each of two inner levels, and all 46 remote
leaf entries. Even counting depth as three levels, the plan allows only
`16*1*3=48`. The 45-entry control counts exactly 48; 46 is the smallest count in
this same-leaf/depth-2 family that exceeds the more generous interpretation.

The builder's refined formula allows `1+16*1*2+46=79`, so it holds. The plan's
simplified bound needs a leaf-load term or an explicit distribution/leaf-size
assumption. No SHA-256 collision is involved: only matching the first byte of
ordinary key hashes.

## Sampling, tolerances and limits

- Exact hash/map/encoding checks allow **zero** mismatches, missed keys, spurious
  keys, unexpected error classes or panics. Inputs rejected with the specified
  sentinel are successful checks, never BROKEN.
- Independent randomness is PCG seeded by `(731903, check-stream-number)`;
  simulation sweeps use seeds 731903–731910 (three-seed vanish check: 731903–731905).
  Each sweep recreates the simulator and nodes. These finite samples make no
  statistical failure-rate or universal latency guarantee.
- The simulator fixture uses 37 content-addressed chunks, three replicas,
  scrub batch 7, repair cap 3, period 200 ms and request timeout 100 ms. Five
  chunks rot together on one replica; intact replicas remain available. Checks
  compare actual bytes and tree entries, not root equality alone. Repairer.Put
  records any invalid invocation before rejecting it; accounting requires zero
  such invocations. Fabricated-fetch scenarios also verify an empty disk.
- Rot repair tolerance: 20 simulated seconds and at most 4× rotted payload bytes.
  Partition tolerance: 20 seconds after heal. Persistent link-loss tolerance:
  60 seconds. The eight-seed loss maximum was 53.602941719 seconds, so this sample
  leaves relatively little margin; it does not establish a deterministic worst
  case under independent message loss. Wipe/tree reconstruction use a 60-second
  observation deadline and report actual maxima above.
- Supplemental tests use DST base 731903, 12 consecutive seeds plus repository
  pinned seeds, and 250 rapid cases per property with seed 731903. Explicit
  `DST_SEED` overrides are cleared for reproducibility. Existing deterministic
  simulation tests perform their own repeated-run digest comparison. Both fuzz
  functions run their saved seed corpora; no long fuzz campaign was performed.
- The plan's tree-corruption case is covered by deliberately incorrect cached
  Record hooks plus restart reconstruction. Source inspection confirms the tree
  is not persisted. Simulator disk bit flips cannot directly modify Go heap
  nodes, and no unsafe memory mutation or new persistence format was introduced.
- The public API hides wire decode/re-encode functions and the skipped-branch
  canary switch. Canonical identity/golden vectors and that switch are therefore
  covered by existing tests, while new checks exercise malformed bytes through
  Answer/Feed. No source was copied into a modified product package.
- No proof of SHA-256 collision resistance, exhaustive key-space search,
  integration with the future shard/manifest layer, deletion tombstones, or
  arbitrary-size/performance guarantee is claimed. Deletes across replicas are
  outside the builder's add-only repair contract.
- No live service or external system was tested. The initial environment knowledge
  search failed before allocating a search number because this worktree has no
  kb index. No index was created, no corpus was opened, and numbered feedback was
  unavailable. This did not affect the Docker checks.

## Resume and artifacts

Work is complete when the final `run-all.txt` summary is present. Re-run all drivers
with the command at the top, or select a reduced reproduction as shown above.
The source of every new check is `main.go` or `sim.go`; original-test mappings are
in `regression.py`. No background job is needed after completion. See this
directory's `STATE.md` for the handoff status; root project state was intentionally
left untouched because the brief allows writes only here.
