# Integration robustness test report

Completed: **284 HELD, 0 BROKEN, 0 setup errors in 110 seconds**. Exit status 0 from the final `run-all.sh`. No confirmed product counterexamples. No product files changed; no commits.

Source HEAD: `db64eb1cfeb05853bbef335c10770470e80d7c13`. Evidence: `evidence/run-all.txt` (284 check lines: 240 independent simulator, 6 independent loopback, 37 existing-suite checks, 1 independent CLI smoke).

## Claims

- C1: The joined code table has no collision; every code round-trips to its sentinel; unknown → `ErrRemote`
- C2: No GET returns wrong bytes; every acknowledged PUT reads back after heal; metadata and membership converge (21 nodes, 3 regions, 1% loss, region partition, crash)
- C3: Concurrent writes on both sides of a partition converge to one version per key on every node; every acknowledged metadata write is current, causally superseded or named as a loser in a discard record (oracle independent of occ's classifier)
- C4: After a quota discard the usage is recounted from the manifests and matches them on every node
- C5: The losing version's shards are collected after the grace; no live version's shard is ever collected
- C6: A deleted object stays deleted through anti-entropy, a partition heal and a crashed node rejoining
- C7: A restarted node gets its missing shards back; a wiped node is refilled
- C8: With up to m shards per stripe rotted every GET is correct and every rotted shard is rebuilt, moving the damaged bytes only
- C9: Chunk 5's 78-cell failure matrix holds on real components
- C10: A bucket that migrates away leaves no inventory running on the source; a stopped node runs no worker
- C11: Object round-trip through the full path
- C12: A duplicated request and a lost response never double-apply
- C13: The same Node code runs on the simulator and on real sockets
- C14: Frame codec: malformed, truncated, oversized input is refused; never a panic
- C15: The frame limit fits a full Merkle reply and a shard request
- C16: One HTTP status table, tested row by row through real HTTP; on a real node for the rows a node produces
- C17: Arbitrary HTTP requests give a status in the table and never a GET that disagrees with the last acknowledged PUT
- C18: Oversized bodies (413), oversized headers (431), slow header/body clients are cut off by the timeouts
- C19: Lifecycle: start, double start, use after close, double close, close during start-up, canceled start (synctest)
- C20: Cancellation mid put (leaves only collectable shards), get, migration start and status
- C21: Shutdown under load returns every call; shutdown during repair and restart on the same disk
- C22: No goroutine survives (leak check in `TestMain` of `internal/node` and `cmd/objnode`); an untracked goroutine fails it
- C23: A loop panic stops the node (recovered and logged), datagram overflow drops and counts
- C24: Three Docker peers in three regions start, join, serve a put on one and a get on another, and refuse stale conditional writes
- C25: Measure quota overshoot during partition.
- C26: GET during migration and migration interleaved with partition, bit-rot and restart.
- C27: Conditional PUT races through HTTP.
- C28: Real-socket short reads, resets, malformed clients and shutdown churn.

## How to reproduce

From the worktree root:

```sh
agent_work/adversarial/06-integration/run-all.sh
```

Each `aNN-*.sh` also runs with no arguments from that directory. All Go compilation and execution happens through `docker compose run --rm -T dev`. The only nested module is this directory. No additional dependencies. Drivers clean compiled executables; runtime and CLI temporary disks live here and are removed. Result logs are confined to `evidence/`. No git writes or changes to product files.

The driver returns 0 for all HELD, 1 for any BROKEN, 2 for setup errors. Individual assertion failures print their actual seed and reason; compiler/container failures do not become product counterexamples. Existing-suite output is explicitly labeled `existing-` so it cannot be mistaken for independent evidence.

## Checks by claim

| Claims | Independent checks and supplementary checks |
|---|---|
| C1 | Every joined sentinel wrapped and round-tripped; all unassigned code bytes checked for ErrRemote. Existing collision, round-trip and block tests. |
| C2, C3 | Twenty seeds, 9 nodes, three regions. Isolate r0 from the other six nodes, wait 7 virtual seconds for membership, acknowledge a write on each side, heal, compare metadata digests and nine GETs. Odd seeds use create-only; even seeds share the initial ETag. Identify losing acknowledged version independently from final winning storage version, then require its OCC version ID in both discard callbacks and text logs. Health totals must equal callbacks and logs. Existing 21-node put/get and partition scenarios supplement this. |
| C4, C25 | Two 600-byte distinct objects plus a 701-byte shared-key version accepted on each partition side. After heal, independently sum ListObjects sizes/count, compare all nine quota records, report overshoot over 2200 bytes. Existing partition suite has a different quota/workload. |
| C5 | After 15 virtual seconds healing (GC grace 4 seconds), scan every simulated disk for loser shards, require actual loser collection events, forbid collection of the final winner; all nine winning reads must succeed. Existing suite additionally checks collection throughout its scenarios. |
| C6, C7 | Wipe and restart node 8; compare every expected shard assigned there against its manifest checksum. Crash it again, delete while offline, restart, compare all nine metadata records and require not-found. Existing crash/restart scenarios add other fault schedules. |
| C8, C11 | Sizes 0, 1, 1023, 1024, 1025, 3077 bytes; independently decode object record framing and use the exported manifest decoder. Rot one shard per stripe (m=1), GET on every node while repair runs, check exact bytes and eventual durable shard checksum, require rebuilt bytes to equal damaged blob bytes exactly. Empty objects exercise the zero-stripe path; only nonempty objects contribute repair evidence. |
| C9, C26 | Twenty separate combined-fault scenarios: regional bucket, 2081 bytes, one rotted shard, r2 partition, migration r0→r1, 15 reads through the transition, coordinator crash/restart after read 5, heal, then require Completed, nine agreeing routes and exact bytes. Temporary unavailability during the combined fault is allowed; incorrect successful reads are never allowed. Supplement: all 78 existing real-component migration matrix cells, with determinism replay and distinct seeds. |
| C10 | Independent disk scan requires no bucket shards in the old region after migration. Existing stopped-node and migrated-away-worker checks inspect worker lifecycle, which is not public API. |
| C12 | Twenty simulator seeds, transport wrapper duplicates every storage RPC while enabled, suppresses each seventh successful response as ErrTimeout, overwrites three keys over nine PUTs. Disable faults, converge, compare every acknowledged object's bytes and all nine quota records to actual object sizes/count. Require both duplication and lost successful replies to occur. |
| C13 | Independent scenarios use the exported node on simulation and loopback runtime with exact-byte checks; existing conformance tests repeat the same four scenarios on both runtimes. |
| C14, C28 | Byte-at-a-time TCP frame delivery with an invalid channel must yield an error response. Reset each of 17 frame prefixes; send oversized, empty and invalid-version frames; malformed frames must close, BadFrames must rise and Health must still work. Existing frame round-trips and malformed codec tables add decoder-only coverage. |
| C15 | Check Merkle reply plus overhead and both default/small shard layouts against MaxFrame; reject negative object size. Existing frame-capacity and config field tables cover boundary layouts. |
| C16, C17, C27 | Eight HTTP races of 12 writers using the same ETag: exactly one 2xx, eleven refusals (412 precondition or 503 unavailable), GET equals winning request bytes, stale DELETE is 412. Oversized object/tag bodies, malformed quota/migration JSON, absent bucket and unsupported method have exact expected status. Existing HTTP model and status-row suites cover every error row. |
| C18 | Loopback partial header and partial body, configured 80/120 ms timeouts, must finish within one second; 10 KB header against 1 KB configured bound must get 431. Existing server bound checks add different sizes/timeouts. |
| C19, C20, C21, C22 | Ten loopback runtime cycles, 16 concurrent PUT/GET pairs each, close races with calls, double start must fail, canceled GET must return context cancellation, use after close must return ErrClosed. After 600 ms settle plus GC, exact FD/goroutine baseline (no positive tolerance), and no runtime stack. Existing synctest lifecycle, mid-operation cancellation, shutdown-during-repair, restart and leak checks supplement this. Independent loopback executable is built with `-race`. |
| C23 | Existing loop panic recovery and queue overflow tests run with `-race`; both require unexported controls. No independent private hooks added. |
| C24 | Build objnode, start three subprocesses in three regions using only container loopback and temporary disks; create/put on peer 0, GET on all peers, conditional overwrite then stale 412, SIGTERM all peers and require clean exits within five seconds each. Existing flags/config/help/invalid-config tests and loopback cluster tests supplement this. |

## Measurements and scope

The final combined run returned `SUMMARY held=284 broken=0 setup_errors=0 elapsed_seconds=110`. All 20 partition scenarios settled at **3101 bytes against a 2200-byte limit: 901 bytes overshoot**. Each collected three losing-version shards. Discard callbacks/counters/text logs agreed exactly, ranging from 9 to 57 events per partition scenario. All 300 reads during the 20 combined migration scenarios returned exact bytes, with zero temporary read refusals. Duplicate scenarios each duplicated 45 storage requests and suppressed six successful replies; each retained two acknowledged keys with exact bytes and quota counts. The HTTP races had exactly one successful writer per round and zero 503s in the final run. Ten churn cycles restored the FD/goroutine baseline. The first complete run had 138 HELD, zero BROKEN and zero setup errors; later coverage expansion and two oracle corrections are documented below. Twenty independent seeds are 6101–6120. Simulator layout is k=2/m=1 with 1 KiB stripes, 1% datagram loss and 2% datagram duplication. Cross-region latency uses the simulator defaults. Quota observations are exact workload measurements, not statistical bounds. No tolerance for wrong bytes, metadata disagreement, missing expected discard, or collection of the current winner.

Supplemental existing tests use `DST_SEED_BASE=6281 DST_SEEDS=2`, `GOMAXPROCS=4`, `-parallel=4`, `-race -count=1`. The existing migration test runs all 78 cells on the first seed and assigns the second seed to one cell; each scenario is replayed for determinism. This is not 78 independent rewrites of its oracle. All other existing test names and raw output are retained in `evidence/supplement.jsonl`.

## Counterexamples

No confirmed product counterexample. A preliminary race-enabled run printed BROKEN because its oracle required every losing conditional PUT to return 412; one returned 503 (round 1, real-socket seed marker 0). That is an allowed availability refusal, not a stale conditional write succeeding. The initial helper reused a 400 ms simulator shard deadline. The final helper uses a 5 s real-runtime shard deadline and a 10 s client deadline, and allows 503 refusals while still requiring exactly one successful writer, exact winning GET bytes, and 412 for the later stale DELETE. The preliminary output is preserved in `evidence/preliminary-loopback.txt`. The response body was not retained, so timeout is a plausible cause, not a proven diagnosis. No product fix was made. A later coverage assertion incorrectly required all three duplicated/lost-response keys to be acknowledged; each seed actually acknowledged two, with valid unavailable refusals for the other key. The oracle now requires a nonempty acknowledged set, validates all refusal errors, visits acknowledged keys in sorted order, and checks every acknowledged value and actual quota. That preliminary output is in `evidence/preliminary-duplicate.txt`; it is not a product counterexample. A correct refusal of an invalid request is scored HELD.

## Limits

These samples do not prove all schedules or reproduce the builder's 500-seed latency percentiles. The report makes no population-confidence or latency-distribution claim. Migration fault timing in the independent combined scenario is selected by read index rather than an internal saga-step hook. The existing matrix supplies those exact step/log boundaries. Private panic/queue/worker controls are covered by existing tests, not independent public-API instrumentation. Tests do not inject OS disk-full or fsync faults, external network behavior, or a permanently lost coordinator. The three-peer smoke uses the actual CLI binary and equivalent layout inside one dev container; it does not test the compose cluster profile's separate-container DNS or published ports. Separate-container execution would conflict with the brief's loopback-only real-socket scope.

The project STATE.md was read but is deliberately untouched: this brief restricts every write to this harness directory and forbids commits. This report is the local handoff record.

The required kb search failed before issuing a search ID because this checkout has no kb index. There was no result to judge with an explicit search number; no index or environment files were changed.
