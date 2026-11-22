# Integration robustness testing — round 2

Completed: **658 HELD, 4 BROKEN, 0 setup errors in 467 seconds** (7 minutes 47 seconds). `run-all.sh` exited **1**, as required for confirmed counterexamples. The four failures are one HTTP entity-tag-list parsing defect affecting four mutation routes. All migration, returning-peer, duel, quota, disk and shutdown checks held. Final output: [evidence/run-all.txt](evidence/run-all.txt).

The restored baseline separately passed all 284 original check slots in 263 seconds. The first expanded run had 657 held and five failure lines; four were the same HTTP defect and one was a fixture coverage assertion, corrected and explained below. Its output remains in `evidence/first-expanded-run.txt`.

Source HEAD: `cc7d95e69970fe64f7452c71a51f941f5c353f93`.

Scope: only `agent_work/adversarial/06-integration/` was written. No product edits, commits or git write commands. The project state file was read; this report is the handoff because the brief forbids writing STATE.md. The concurrent gate owns changes under `agent_work/06-integration/gates/`.

## Harness changes and retained checks

All 240 independent simulator checks, six loopback checks and the CLI smoke from round 1 remain. The 37 supplemental test slots remain, with the following changes:

- Runtime imports now use `internal/noderuntime`; HTTP server/limits use `internal/storage/httpapi`. The shutdown stack search uses the new runtime package name.
- Supplemental tests search the two new packages as well as node, storage and objnode.
- `TestRuntimeCloseDuringStartup` was removed by the repair. Its slot now runs `TestCloseWhileStartIsInProgress`, which actually pauses startup.
- `TestRuntimeCancelMidPutLeavesOnlyCollectableShards` and `TestCancelMidGetAndMidMigrationReturnContextErrors` were removed. Their slots now run `TestACallCanceledBeforeItReachesTheLoopNeverApplies` and `TestACallCanceledAfterTheLoopTookItAppliesOnceAndIsReported`. These cover the repaired cancellation contract; the old tests could not be retained as executable tests because they no longer exist. The original claim that those tests proved mid-write orphan collection is withdrawn.
- The existing unknown/stale conditional-write refusals remain 412. New model rows explicitly expect 412 for unknown quoted entity tags, empty/whitespace If-Match fields and weak-only lists, 400 for malformed syntax or unsupported If-None-Match values, and success only for a matching strong validator or `*` on an existing entity. These are intentional re-judgements of repaired behavior; no original harness assertion of unknown-tag 400 existed to change.
- Migration's source-empty check remains for its fully converged fixture. A returning hidden write is allowed to retain its shards in the source region, as the repaired protocol specifies; its readability is checked separately.

Original numbered claims C1–C28, detailed checks and round-1 evidence are preserved in [ROUND1.md](ROUND1.md). C19/C20 are narrowed as above. C23 now also has independent public-API panic injection. The remaining old check bodies were retained.

## Added numbered claims and checks

| Claim | Scenario and oracle | Sample |
|---|---|---|
| C29, C2–C4 | Non-coordinator create/overwrite/delete traffic throughout a migration, with 1% datagram loss, 2% duplication and simulated network delay. Requests continue at 100 ms intervals until both ten seconds of traffic and terminal migration state; a 60-second cap is enforced. The rollback wrapper runs verify then returns a permanent refusal. Every acknowledged manifest/tombstone is matched only against the same key's final version or discard record using its version vector. Every live key is GET on all nine nodes; bytes must be from an acknowledged value on that key and match winning metadata's SHA-256. All metadata digests agree. Quota is independently summed on every node after heal. | Seeds 7201–7250, separately for completion and compensation: 100 scenarios. |
| C30 | Two starts with no simulated event between them, coordinators 0 and 4, destinations r1 and r2. Same bucket and different buckets. Both sagas must terminate, at least one complete (both for different buckets); all metadata must converge and all nine original-object reads must be exact. Per-bucket quota recounts on every node. Both same-bucket sagas may report completed when one observes the winning flip; that alone is not an error. | Seeds 7301–7320 × two variants: 40 scenarios. |
| C4 | 100-byte initial object; isolated source side writes 500 bytes, other side later writes 100 bytes. Assert the same-size write leaves its quota version unchanged. Assert the first manifest discard occurs before any quota discard; subsequent repair writes may themselves conflict. After heal all nine quota records must equal independently summed live objects. | 20 seeds, 7301–7320. |
| C33, C4 | Begin a PUT on peer 1, then drop its outbound links after shard requests have left. Wait for acknowledgement, isolate it, and pause it on even seeds. Assert all other eight nodes lack the key. Complete migration while it is absent, resume/heal, then require all nine reads to return its exact acknowledged bytes and all quota records to match recounts. | 20 seeds, 7301–7320: ten partitioned, ten paused and partitioned. |
| C31 | Independent status/mutation table through the real HTTP handler and real runtime, including object PUT, object DELETE, tag PUT, ACL PUT. Empty fields, whitespace, empty quoted tags, malformed quoting, unknown tag, weak validator, star, matching/nonmatching lists, empty list members, star mixed into list, and If-None-Match variants. Each refusal must preserve the complete metadata digest; successful writes must change it, and objects are read back. A quoted comma inside an entity-tag is also exercised. | 21 cases × four mutation routes = 84 checks. |
| C23, C22 | Panic in `Runtime.Inspect` on the node loop. Require ErrClosed, Done closed, ErrPanicked and listener refusal before explicit Close. | One race-enabled independent check plus retained existing panic tests. |
| C21, C22 | Hold a real saga's first step without calling its completion. Verify it is nonterminal, call Shutdown, require Done and socket closure and post-close API refusal. | One race-enabled independent check. |
| C32 | Go overlay adds an independent in-package test using the package's existing test-only memFS fault layer. Overwrite two paths with sizes 0, 1, 4097 and 65537; inject crash or I/O failure at every operation boundary, then recover both durable-directory and retained-directory images. Require old or new bytes only, and new bytes whenever Sync succeeded. | 108 boundary checks, two recovery images each. |
| C29, C33, C32 | Supplement repaired targeted late-flip and isolated-late-write tests, each with deterministic replay; original file-disk crash suite also checks new directories, append, delete and short writes. | Seeds 7401–7420 for each targeted scenario, each replayed for determinism; exact subtests in repair.jsonl. |

Go builds/tests run only through `docker compose run --rm -T dev`. Final drivers use GOMAXPROCS=2, build parallelism 2 and test parallelism 2. All six drivers run serially. The retained migration matrix tests seed 6281 in all 78 cells and assigns seed 6282 to one cell: 79 seed/cell combinations, each with deterministic replay (confirmed from JSON leaf results). The loopback checks and retained supplemental suite use the race detector. The independent simulator and disk/targeted supplement do not.

## Final measurements

The 100 independent migration scenarios made **11,753 traffic attempts**, including **6,773 while migration was nonterminal**, and acknowledged **5,519 mutations**, including **1,444 deletes**. Per-scenario attempts ranged from 100 to 165. Fifty migrations completed and fifty compensated. All 40 duel scenarios, 20 returning-peer scenarios and 20 corrected manifest-discard quota scenarios held. The 84 HTTP model checks held 80 cases and failed the four quoted-comma cases. All 108 independent disk boundary checks and all four supplemental repaired-suite checks held. These counts are checks/samples, not population bounds.

## Reproduction

From the worktree root:

```sh
agent_work/adversarial/06-integration/run-all.sh
```

Output is one HELD/BROKEN line per check, followed by `SUMMARY held=N broken=M setup_errors=K elapsed_seconds=S` in `evidence/run-all.txt`. Exit 0 means all held, 1 means a counterexample, 2 means a setup error. Drivers preserve full logs under `evidence/`; compiler/container errors are not product findings.

For a single new simulator seed (7301 also selects its returning-peer, quota and two duel checks):

```sh
docker compose run --rm -T dev env GOMAXPROCS=2 GOFLAGS=-p=2 CHECK_SEED=7301 sh -c 'cd agent_work/adversarial/06-integration && go build -o evidence/repro . && ./evidence/repro extended; r=$?; rm -f evidence/repro; exit "$r"'
```

## Confirmed counterexample: quoted commas in If-Match lists (C31)

Four checks fail: object PUT, object DELETE, tag PUT and ACL PUT. This is one shared parser defect. In `internal/storage/httpapi/http.go`, `parseEntityTags` splits the raw field on every comma before parsing quotes. A comma is permitted inside an opaque entity-tag, so `"a,b", "<current ETag>"` is a valid two-member list whose second strong validator matches. The implementation sees the invalid fragment `"a` and returns 400. Expected: 200 for the three PUT routes, 204 for DELETE. No precondition was silently ignored: this counterexample is rejection of a valid matching request.

Minimal operation sequence (real-runtime seed marker **0**): create bucket `b`; unconditional PUT `/b/k` with `initial`, retaining its returned ETag `E`; PUT `/b/k` with `changed` and `If-Match: "a,b", E`. Actual status 400 with `storage: entity-tag "\"a" is not quoted: storage: invalid argument`; expected 200. The generated ETag varies with real time, but the failure does not depend on its value.

Exact isolated reproduction from the worktree root:

```sh
docker compose run --rm -T dev env GOMAXPROCS=2 GOFLAGS=-p=2 CHECK_HTTP_CASE=object-quoted-comma-list sh -c 'cd agent_work/adversarial/06-integration && go build -o evidence/http-repro . && ./evidence/http-repro http; r=$?; rm -f evidence/http-repro; exit "$r"'
```

Expected harness exit is 1. Captured isolated output: `evidence/http-minimal.txt`. Full four-route output: `evidence/run-all.txt` and `evidence/loopback.log`. Change the filter to `delete-quoted-comma-list`, `tag-quoted-comma-list` or `acl-quoted-comma-list` to reproduce the other routes. No product fix was attempted, as required by the brief.

## Preliminary oracle corrections

The first extended diagnostic compared terminal saga states against capitalized strings; the API returns lowercase. Its deadline failures were harness errors, not migration failures. That diagnostic was stopped and corrected. `evidence/preliminary-state-case.log` preserves the diagnostic where available (logs are ignored by git).

The initial quota check required zero quota discards throughout repair. Seed 7301 correctly repaired usage but produced 22 later quota discards. The corrected check requires no quota update from the same-size overwrite and no quota discard before the first object discard; subsequent repair conflicts are allowed. `evidence/seed7301.txt` and `evidence/seed7301-recheck.txt` show the incorrect assertion and its corrected result.

Seed 7315 in the first full expanded run reported two quota discards before the first object discard, although its final quota recount passed. That violates the fixture coverage assertion, not the product quota invariant. The fixture now waits for initial usage to settle, clears setup-period discard observations, and uses a five-second recount settle window to prevent its own early audits from introducing the quota conflict the scenario is intended to exclude. The corrected fixture passed all 20 seeds in `evidence/quota-recheck.txt`, including seed 7315 (`evidence/seed7315-recheck.txt`). The corrected fixture also passed all 20 seeds in the final complete run.

## Limits and handoff

Finite schedules are evidence, not proof. The independent acknowledgement oracle allows a causal same-key successor recorded as discarded to explain an earlier acknowledgement; it never uses another key's version. It does not independently require each discard's text log in the new scenarios; the retained partition checks still cross-check callbacks, health counters and logs. The returning-peer sample returns before OrphanGrace; long-term abandoned writes are outside its claim. No physical power cut or real filesystem power-loss behavior is tested: disk recovery uses the existing fault model, whose assumptions remain shared with product tests. The independent overlay covers overwrite boundaries; append/new-directory/short-write coverage comes from the retained product disk suite. HTTP model fields are passed via httptest to preserve empty/whitespace fields exactly; original socket tests still cover actual HTTP transport.

Shutdown during migration is checked while its first step is held; it is not a sweep of every saga step. The existing matrix and shutdown-under-load/repair checks remain supplemental evidence. There is no new continuous independent shard-deletion oracle: exact winning reads after heal and the existing repaired targeted suite supplement the retained collection checks.

The kb search failed before assigning an ID because this checkout has no kb index. No corpus was opened, no index was created, and no feedback ID existed to judge. No durable environment finding was added.
