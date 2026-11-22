# Brief — Chunk 6 REVIEW 1: integration: the object store and sync engine

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch `chunk/06-integration`).
READ-ONLY review: do not edit code, do not commit, no git write commands. The ONLY file you
create is `agent_work/06-integration/REVIEW-1.md`. Write it before you stop.

## What you are reviewing
`git diff main..HEAD -- internal cmd agent_work/gates` (an Opus builder's commits; `git
log main..HEAD`). Product Go is comment-free ON PURPOSE (another seat writes comments
later): not a finding. Read:
- the builder's brief `agent_work/06-integration/objstore-p2-06-integration-build.md` and rules
  `agent_work/06-integration/objstore-p2-builder-rules.md`;
- PLAN.md: section 2 (testing architecture), the item "Chunk 6" in 4.2 (deliverable, the
  named tests, the adversarial list), the 2.6 error rows and the section 7 decisions the
  item cites;
- `agent_work/06-integration/build-notes.md` (claims, measurements, uncovered branches, requests);
- the simulator contract the code builds on: `agent_work/0b-simulator/build-notes.md`
  ("Semantics Wave B builders must know") and `agent_work/0b-simulator/frozen-api.txt`;
- the code and tests in `internal/node`, the HTTP object API in `internal/storage`, and `cmd/objnode`.
The orchestrator's gate result for this tree is `agent_work/06-integration/gates/result.json`.
You may run tests through Docker only: `docker compose run --rm -T dev go test ./internal/node/...`
(also `-race`, `DST_SEEDS=50`, `-rapid.checks=2000` on packages that import rapid).

## Questions (number your findings)
1. Correctness against the plan item: is every part of the Deliverable built, and built
   right? Name protocol or algorithm errors concretely (a state, a message order, an input)
   with a reproduction — a test snippet or a seed where possible.
2. The headline claims: for each claim in the build notes, does the named test really prove
   it? Find tests that cannot fail, assert nothing, use a bound so loose it proves nothing,
   or silently skip (zero iterations, empty tables). Do the canaries fail for the planted
   reason? Does every test named in the plan item exist and test what its name says?
3. Determinism and simulator use: any path where two runs of one seed can differ; any
   misuse of the simulator contract (assuming callbacks run synchronously, ignoring
   `ErrTimeout` ambiguity, relying on unsynced disk data surviving a crash, sharing memory
   across nodes, drawing from the wrong stream).
4. Failure handling: PLAN 2.6 rows — every sentinel produced and tested; malformed or
   hostile input is an error, never a panic or unbounded work; resource bounds (memory,
   message size, retries).
5. Interfaces for the integration chunk (Chunk 6 wires all five protocols into one node on
   the simulator AND on a real UDP/TCP/file runtime): are the small interfaces this chunk
   declares (`Membership`, `Repairer`, step interfaces, …) implementable by the other
   chunks as planned? Anything that will not survive a real runtime?
6. Rules: standard library only; no comments; no goroutine/channel/mutex/wall clock/global
   rand/float in protocol code; panics only for violated invariants, each allowlisted; the
   frozen simulator packages untouched.

## Output: agent_work/06-integration/REVIEW-1.md
First line exactly one of `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`. Then numbered
findings, each with severity (BLOCKER / MAJOR / NIT / NOTE), file and line, what is wrong, a
concrete reproduction and the smallest fix. FAIL needs at least one BLOCKER or MAJOR: wrong
behaviour, a headline claim its test does not prove, a determinism leak, an interface the
integration cannot use. Do not pad with style remarks. End with what you verified by
running versus by reading.

## Specific to this chunk — read before you start
This is the integration chunk: about 17 commits, a new package `internal/node` (the node,
its adapters, the real runtime), the HTTP object API added to `internal/storage`,
`cmd/objnode`, a `cluster` compose profile with `agent_work/06-integration/cluster-smoke.sh`,
and one change inside a landed package (`occ.Node.Merge`). The five protocol packages
underneath were each reviewed and landed already; review the WIRING and the RUNTIME, and
any change made inside a landed package. Their facts are in `docs/knowledge/chunk-0*.md`.
Binding design ruling (PLAN.md 7.5 and 9.1): concurrent versions are NOT kept as siblings;
last-writer-wins by hybrid logical clock picks one version, the loser is discarded and
every discard is counted and logged. A key always reads as one version. Do not report the
absence of siblings or the loss of an acknowledged concurrent write as a defect; DO report
a loss with no record, two versions shown to a reader, or replicas that end different.

Spend your effort where a defect would be worst (numbered questions continue the list):
7. Data safety of shard collection. The builder found and fixed a bug where shards were
   collected "because no manifest referenced them", deleting live data on a node
   partitioned for the grace period; shards are now collected only on replicated evidence
   that their version is dead, or after a one-hour orphan grace. Attack the new rule: any
   schedule (partition, crash, restart with a stale disk, a late manifest, a migration in
   flight, a last-writer-wins discard racing a reader) where the WINNER's shards, or the
   shards of a version that can still become the winner on some node, are collected; any
   schedule where a discarded version's shards are never collected. Is "GC safety checked
   continuously in every DST" really an invariant check on every step, independent of
   the product's own bookkeeping?
8. Deletes. Do deleted objects stay deleted through anti-entropy, a partition heal and a
   crashed node rejoining with an old disk — including after tombstone retention expires
   (what then stops a node that was down longer than the retention from bringing the
   object back)? Is the retention rule stated and tested at its edge?
9. Last-writer-wins plumbing in the node: discard counter and records on the health
   surface; quota recount on a quota-key discard (settles, writes nothing when right);
   never two versions to a reader through any API path, including list and head.
10. The real runtime and PLAN 2.5: one owner per goroutine, a WaitGroup and a context for
    each, bounded queues that drop and count, no protocol state touched off the node
    loop (look for any handler, timer or HTTP path that calls into gossip/occ/merkle/saga
    or storage state directly), `Shutdown` returning only when everything has exited,
    no sleep used as synchronisation in tests, nothing timing-dependent under
    `-race -count=3`. File-backed disk: does "write then fsync" really give the same
    durability meaning as the simulator's two-layer disk (directory fsync after create and
    rename, partial writes, a crash between write and sync)? TCP frame codec bounds; HTTP
    limits against slow and oversized clients.
11. One protocol core, two environments: is there any branch on "simulated or real"? Do
    the full-node DSTs run the same node type the binary runs?
12. The builder's three open decisions (build notes, "Requests to the orchestrator"): give
    your judgement on each — (a) the migration failure matrix on real components runs all
    78 cells on the base seed and spreads the 500 sweep seeds across cells (about 7 per
    cell) instead of 78 × 500: is the claim in the notes worded to match that, and is it
    enough for landing if the full product is run in the hardening chunk; (b) the node
    package's 500-seed sweep takes about 14 minutes; (c) the gate does not yet run the
    cluster smoke script or hold `internal/node` to the coverage floor (the orchestrator
    will add both — say what else the gate should check for this chunk).
You cannot run the whole 500-seed sweep or the cluster smoke script in a review; say what
you ran. Docker only.
