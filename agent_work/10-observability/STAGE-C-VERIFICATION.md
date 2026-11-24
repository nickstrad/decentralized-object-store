# Stage C verification — DONE

Branch: feat/observability. Resumed from f711468 after the f5c4538 catalogue repair.
O1/O2/O3/O4/A2 are implemented, committed and independently reviewed. **The phase remains
NOT READY** pending Neon's second final pass. No push, main merge or publication occurred.
The frozen metrics interfaces/catalogue are unchanged; go.mod/go.sum are unchanged.

## Implementation commits

| Deliverable | Commits on feat/observability |
|---|---|
| O1 facade, stripe hooks, storage/protocol/node sources | ca8d9b6, b254d38, 99263a7, 88e9b08 |
| O1 cost/accounting repair | ed81d14, 978c322 |
| O2 HTTP and fixes, runtime collector, CLI/lifecycle | a5cbca3, 01253b5, 131b37e, 7b420ad |
| O3 Prometheus/cache | 822bb08, ea0667e |
| O3 CSV/readback/bounds/exact CRLF | f2319e0, d993950, ba2005f |
| O4 pinned DuckDB analysis, real-node smoke/cleanup/cache | 44ee96d, 79b35ba, f69ef35, 060bb3e |
| A2 real-node assertions, Compose profile, CI | 7954bdb, 8f25539 |
| Snapshot cost regression/benchmark | 7304a6b |

Durable findings are in docs/knowledge/observability-{components,runtime,adapters}.md;
reusable commands are indexed in work-scripts/README.md. There is no runtime DuckDB writer:
CSV is disk flush, DuckDB is the analysis consumer. Optional CLI image delta is 21,714,514
bytes (320,741,177 analysis versus 299,026,663 plain dev), version 1.5.0, linux amd64.

## Final acceptance evidence

Product code revision: 978c322. Subsequent changes record evidence/knowledge or add shared
Go cache mounts to the smoke wrapper; no production Go changes followed that revision.

- `go build ./...`: PASS.
- `go vet ./...`: PASS.
- `go test -p 2 -timeout 30m ./...`: PASS, every package, 682.55s total. Full end-to-end
  package 503.535s. No tests were filtered or shortened in this run.
- Docker `ci-fast.sh`: PASS G0/G1/G6, all required coverage floors >=85%. Metrics 96.6%,
  telemetry 91.0%, erasure 98.1%, gossip 96.7%, storage 96.0%, Merkle 95.5%, OCC 96.6%,
  saga 94.0%, node 87.5%, runtime 88.5%, HTTP 89.5%. This short Docker gate is partial and
  `counts_for_landing=false`; no full landing/fuzz/large-sweep gate is claimed.
- Focused Docker `go test -race -count=1` for noderuntime, httpapi, telemetry and objnode:
  PASS on the repaired tree. Runtime/HTTP/cache ownership also received independent race
  probes. Off-mode allocation regressions prove direct callback paths avoid new wrappers.
- Snapshot cost with 1/100/1000 real objects: same seven sources, 109 points, 494 allocations,
  42,376 bytes per collection. Largest fixture: 1004 metadata records, 1000 shards and
  2004 Merkle entries. CPU timing is descriptive; assertions use cardinality/allocations.
- Real Docker `TestObjnodeOTelPrometheus` through check-otel.sh: PASS. Actual objnode HTTP
  counter increases, in-flight gauge present, stored boot_id label count 0, Collector
  objstore_* metrics contain no boot label. Full names/labels/query JSON are archived in
  stage-c-otel-results.json. This is the real node path, not the A1 fixture.
- CSV precision fixture through pinned DuckDB: PASS >2^53 exact UBIGINT, JSON extraction,
  multiple segments, `.tmp` exclusion, counter delta, rate and mean latency.
- Real CSV-enabled objnode through pinned DuckDB: PASS, multiple finalized sequences, all ten
  components, positive HTTP health counter and gauge, ignored `.tmp`, zero goroutines at
  clean process exit. Latest-run summary is stage-c-csv-results.json.
- Independent cross-company reviews: initial component/adapter/analysis NITS repaired or
  documented, then adapter/HTTP confirm PASS, component/cost confirm PASS, runtime/CLI PASS.
  Full reports are under reviews/. No frozen-core blocker remains.

Machine-readable records: stage-c-suite-result.json, stage-c-fast-result.json,
stage-c-otel-results.json, stage-c-csv-results.json. Focused race output is
stage-c-race-results.txt. Local detailed logs and exit files are under
/root/runs/objstore-obs-resume-*; generated service/query logs are under evidence/ (ignored).

## Preserved failures and limitations

The first exact `go test ./...` run passed all packages except test/e2e, which hit Go's
default 10-minute package timeout while the migration matrix was still progressing under
concurrent suite/review load. No earlier assertion failed. The complete rerun used the
repo gate's 30-minute timeout and lower package concurrency, with every test retained, and
passed. The initial summary is stage-c-first-suite-result.json; its full log remains
/root/runs/objstore-obs-resume-fast.log.

The first repeated real CSV smoke hit its 90-second wrapper deadline during a cold Go
build before node startup. Its named container was cleaned. Adding the same shared Go
cache mounts as Compose kept the deadline and made the single retry pass. Logs/exit codes:
objstore-obs-resume-node-csv-final* under /root/runs/.

The independent runtime reviewer saw one pre-existing one-second loopback RPC timeout
under load; isolated reruns and the final complete/Docker suites passed. The reviewer also
left two optional lifecycle nits: joining a hypothetical non-cooperative adapter, and an
unreachable startup-error shutdown context. Built-in adapters honor cancellation and the
export-outage shutdown test passes. These are recorded, not hidden core changes.

CSV retention/error/crash/power-loss limitations and component metric interpretations are
in the durable knowledge entries. Reader-facing README/reference/help/book/C4 polish is
reserved for Neon's second final pass; see STAGE-C-HANDOFF.md. Do not mark READY from this
Stage C record. Completed resumed worktrees and review worktrees are cleaned; the original
interrupted worktrees remain untouched. No background build/review/export process remains.
