VERDICT: NITS

The original MAJOR is closed. No new BLOCKER or MAJOR found in the durable-record fix. Landing requires the documentation correction below. All requested package checks completed successfully.

Reviewed product tree 4a94b64 (HEAD later advanced to 33dc0d1, adding only prose briefs).

1. The original ARBITRATION-1 reproduction was run unchanged in a temporary HEAD export. It reaches the identical version c7bd04bd6c737491 and the identical two orphan removals, then fails its obsolete assertion that the manifest remains live. A second run changes only the final assertions: every node must have a non-live manifest and a live ~lost/<version> record, and GET must answer not found. PASS. Both readable controls (2+2 and 4+2) PASS. Exporting test-only commit 2d348714503903515204e2cf6649b07e716956a6 with git archive and running the DeclaredLost selection produces all six expected still-live failures; the within-m readable control passes.
2. Safety: the focused loss tests, paused/partitioned-holder controls, late-write refusal, declaration write order, and wiped-holder collection pass. Independent temporary tests confirm refusal after Crash/Restart and that the converse oracle rejects a loss record even when three good shard holders are paused. Reading stateAbsent, rebuild, serveStorage, the OCC persistence/merge paths, migration copying and discard handling found no counterexample without loss of the durable absence evidence. The disk-wipe exception is addressed below.
3. Liveness: focused tests cover mixed unwritten/orphan/rot causes, 1/2/5/6 stripes and 2+2/4+2 layouts. Independent ten-stripe cases additionally pass at 1+0, 1+1, 2+1 and 3+3. The unreachable-holder limitation is real: no timeout counts as evidence. The before-declaration disk-wipe case (b), previously untested, is now independently reproduced in the temporary export; the converse oracle correctly detects a declaration with k good shards. The existing case (a) passes and collects the late shard. These are accepted as an explicitly bounded loss-of-fence residual, not proof of unconditional safety after disk loss. One runtime claim in the description is too strong; see the required correction.
4. The converse oracle is installed unconditionally by newHarness for every full-node DST, independently of gc.off and the per-key orphan exemption. It checks the disks at each local declaration, including paused/down holders. The independent paused-holder self-check proves it fails when it should; the case-(b) test exercises it through a real declaration. The diff does not remove prior assertions or alter internal/env or internal/sim. Determinism lint passes (65 files in the nine selected packages). Full package tests pass. The 2000-check property run, go vet, full package race run and 50-seed full-node DST run all pass.

Required small correction

- agent_work/06-integration/build-notes.md:1146, section 14, “Residual limit”: replace the categorical claim that the real runtime cannot deliver a pre-restart request with the narrower fact that an already-established TCP connection cannot survive the receiving process's restart. internal/noderuntime/runtime.go:623–631 launches roundTrip asynchronously and only then dials the address; it carries no destination incarnation. A request still awaiting scheduling, resolution or connection establishment can connect to a restarted holder before its deadline. This is a code-reading conclusion, not a real-runtime end-to-end reproduction. State that the protocol's durable-fence guarantee assumes the holder retains its absence metadata; loss of that metadata plus a sufficiently late shard request is the residual in either transport. Cases (a) and (b) otherwise describe the observed behavior accurately. No startup delay or blanket refusal of first writes is required for this documentation correction.

Why the ordinary safety argument holds

- Each record is written only after every other index answered successfully, not-found or corrupt, fewer than k gathered blobs verify, and a fresh local read finds no matching shard. A timeout/not-ready/unreachable response cannot establish absence. The manifest must still name this live version; staged migration copies do not qualify.
- The record is persisted before it is advertised. Later wire writes consult current metadata and are refused; maintenance also skips recorded indices. There is at most one rebuild per local shard. An ordinary restart reconstructs the fence from disk, as independently tested. Delayed successful rebuild responses from another index do not create information: producing a missing shard still requires k verifiable sources. Late writer arrivals before a record are covered by the final local check; arrivals after it are refused.
- More than m binding records from the actual placement holders imply fewer than k usable indices. Two designated declarers or reordered replication cannot weaken that arithmetic. Old records do not become absence evidence for a migration's new storage version or an overwrite's version. Tombstoning uses IfMatch on the examined manifest; a remote concurrent version settles through the existing counted/logged LWW merge path. No ordinary operation removes the bucket-scoped records.
- A crash after the loss tag but before the manifest tombstone leaves evidence and permits retry. A crash after the tombstone can skip the GC marker and usage adjustment, as already documented; ordinary orphan collection and quota audit repair those consequences. The order test exercises the commit sequence, not every physical crash point.
- Orphan collection can still remove a shard of an acknowledged version whose manifest remains isolated beyond OrphanGrace. That pre-existing, explicitly accepted residual is exercised with the GC oracle exemption; the change does not promise that collection never happens. Once enough durable absence records exist, death collection cannot destroy a recoverable version unless those fences have themselves been lost.

Independent residual check

In the existing ten-node stale-write scenario, hold the writer-to-holder request, rot two other placement shards, and stop when three placement holders have each committed their own absence record but no loss is yet declared. Wipe/rejoin the missing holder, release the held request before metadata sync, and observe its shard land. The next declaration is rejected by the converse oracle because k verifiable shards now exist. This confirms case (b), rather than silently excluding it from the oracle. Acceptability is limited to the documented compound fault: an already-unrecoverable stripe, destruction of a holder's durable fence, and a late writer request. Preserving the fence or binding writes to a destination incarnation would be needed for a stronger guarantee; neither is claimed here.

Verification by running

- Docker package run, count=1, GOMAXPROCS=2, -p 2: node 176.315 s, noderuntime 5.997 s, storage 6.368 s, httpapi 1.867 s, both cmd packages PASS.
- Original reproduction, final-state variant, readable controls, focused loss/absence/order tests, independent layout/restart/oracle tests: results above.
- Pre-fix test-only export: expected six behavioral failures, no compilation failure.
- Independent before-declaration wipe test: PASS by observing the expected converse-oracle failure.
- Determinism lint: PASS. Frozen-package diff: empty.
- `go test -count=1 ./internal/node -run Prop -rapid.checks=2000 -timeout 15m`, GOMAXPROCS=1: PASS (124.617 s).
- `go vet ./internal/node/... ./internal/noderuntime/... ./internal/storage/... ./cmd/...`: PASS.
- `go test -race -count=1 -p 2 ./internal/node/... ./internal/noderuntime/... ./internal/storage/... ./cmd/... -timeout 15m`, GOMAXPROCS=2: PASS (node 505.749 s; runtime 8.837 s; storage 32.346 s; httpapi 5.770 s; both cmd packages passed).
- `DST_SEEDS=50 go test -count=1 ./internal/node -run 'DST$' -timeout 15m`, GOMAXPROCS=2: PASS (527.809 s).
- All Go commands above ran through `docker compose run --rm -T dev` from the worktree root; temporary exports were mounted at /src for independent and old-tree checks.

Verification by reading

Read the prior arbitration, section 14, every changed product path and oracle, storage request/quorum paths, OCC persistence/preconditions/discard handling, migration copy/stage/version paths, and real-runtime request/connection lifetime. The safety conclusions for concurrent declarers, metadata ordering and concurrent overwrite/delete are supported by those paths and existing scenario coverage, not an exhaustive exploration of all schedules.

Only this output file was written in the worktree; exports, added tests and logs are under /tmp/obj-confirm-5kf2chjy and /tmp/obj-confirm-*. The pre-existing deleted gates/result.json was left untouched. The required kb search could not run because this worktree has no kb index; feedback was attempted and likewise could not be recorded. No environment knowledge-store files were opened or changed.

NITS CLOSED (orchestrator, 2026-11-22): the one required correction is made in build-notes.md section 14, "Residual limit" (commit b0efdd4): the claim about the real runtime is narrowed to an established TCP connection not surviving the receiver's restart, the dial-after-scheduling path is named, and the refusal is stated to assume the holder keeps its absence metadata.
