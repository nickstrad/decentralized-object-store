# Brief — Chunk 6 ARBITRATION: integration (the object store and sync engine), after a second FAIL

Seat: Astra (gpt-6-astra), arbiter (fresh). You are in a git worktree (branch
`chunk/06-integration`). READ-ONLY: do not edit, commit, or run git write commands. The
ONLY file you create is `agent_work/06-integration/ARBITRATION-1.md`. Your verdict is
final for this chunk. (The project's gate is running in this worktree at the same time
and writes only under `agent_work/06-integration/gates/`; the machine has 4 shared cores
— run package-level tests, not the whole module at 500 seeds.)

## Why you are here
Project rule: a reviewer's second FAIL goes to a third seat. The chunk wires five landed
building blocks (gossip membership, erasure-coded storage, Merkle anti-entropy, metadata
with conditional writes and last-writer-wins, sagas) into one node: `internal/node`
(protocol core, runs under the deterministic simulator), `internal/noderuntime` (real
UDP/TCP/file runtime), `internal/storage/httpapi` (HTTP object API), `cmd/objnode`, and a
three-node Docker cluster profile. Governing text: PLAN.md 2.5, 2.6, 4.2 "Chunk 6", 7.5,
9.1. Binding design ruling: concurrent versions are NOT kept side by side —
last-writer-wins by hybrid logical clock picks one version on every node, the other is
discarded, and every discard is counted and logged. Losing an acknowledged write to a
CONCURRENT write is the stated design; a loss with no record, or a key the metadata lists
as live that is unreadable after heal and not declared lost, is a defect.

History:
- `agent_work/06-integration/REVIEW-1.md` — reviewer (Gemini), FAIL: one blocker (a write
  acknowledged just before a migration's routing flip could have its shards destroyed),
  eight majors.
- one repair round and two small follow-ups (package moves, an entity-tag parsing fix).
- `agent_work/06-integration/REVIEW-2.md` — reviewer, FAIL again: all eleven first-review
  findings FIXED or correctly narrowed, gate changes and package moves judged sound, but
  six NEW findings: two MAJOR (orphan-shard records overwritten across maintenance ticks,
  so a multi-stripe object from a long-absent writer could stay live and unreadable
  instead of being declared lost; `realNet.Request` calling its callback twice on two
  error paths) and four NIT.
- the orchestrator accepted all six. The answer is `git log 94c1a8468806277af07014eda566a79a1a4e0896..HEAD` (brief
  `agent_work/06-integration/objstore-p2-06-integration-fix2.md`; account: "Review 2
  fixes" at the end of `agent_work/06-integration/build-notes.md`). One nit (the fuzz
  retry signature in `agent_work/gates/fuzz-short.sh`) was closed by the orchestrator in
  commit 94c1a8468806277af07014eda566a79a1a4e0896 itself (`git show 94c1a8468806277af07014eda566a79a1a4e0896 -- agent_work/gates/fuzz-short.sh`).
- the independent test harness (your seat's, two rounds) and its results:
  `agent_work/adversarial/06-integration/` (`ATTACKS.md`, `RESULTS.md`: 662 held, 0 broken).
There is no disagreement about the findings; the question is whether the chunk may land.

## What to decide
1. For each of REVIEW-2's six new findings: FIXED, PARTLY FIXED or NOT FIXED — from the
   code and by running. Does each regression test fail on the code before the fix
   (export the tree at 94c1a8468806277af07014eda566a79a1a4e0896 to a directory under /tmp with `git archive` and run the new
   test there through Docker; `git show <rev>:<file>` for single files)? In particular:
   - new finding 1: orphan records are now one key per shard. Under last-writer-wins and
     replication, can any sequence (two ticks, two nodes, a restart, a tombstone or
     later rewrite of the same key) still erase or miss an orphan entry so that a stripe
     with more than m shards gone is not declared lost? Is the new invariant in the
     simulation oracle ("after heal every key listed live is readable unless declared
     lost on record") really checked in the full-node scenarios, and does its self-check
     fail when it should? Is the residual limit stated in the notes exactly what the
     code does: a write from a peer away longer than the orphan grace is either readable
     or declared lost, for objects of every size?
   - new finding 2: every callback-taking path of the real transport calls back exactly
     once (including the extra change the builder made: an oversized request to any
     peer is refused up front).
   - new finding 3: a flip retried after a crash between its two writes during a
     concurrent migration leaves the bucket writable with one route owner.
2. Did these fixes introduce a new BLOCKER or MAJOR (wrong behaviour, a determinism leak
   in `internal/node` — it must stay free of goroutines, locks, wall clock and map-order
   dependence — a weakened test, an edit to the frozen simulator packages `internal/env`,
   `internal/sim`)?
3. REVIEW-2 judged the first-review blocker closed for live peers, restarts and duelling
   migrations. You do not need to redo that work, but if while reading you see a
   schedule in which a key the metadata lists as live ends permanently unreadable after
   heal without a lost record, report it with a reproduction.
4. The fuzz retry in `agent_work/gates/fuzz-short.sh` as it now stands: acceptable?

Everything runs through Docker from the worktree root: `docker compose run --rm -T dev
go test ./internal/node/...` (also `./internal/noderuntime/...`,
`./internal/storage/...`, `./cmd/...`; `-race`; `DST_SEEDS=50`; `-rapid.checks=2000`).
Leave nothing behind outside /tmp and your one output file.

## Output: agent_work/06-integration/ARBITRATION-1.md
First line exactly one of: `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`.
PASS: the chunk may land. NITS: it may land once the listed small items are closed (list
each precisely, with file and the smallest fix). FAIL: a BLOCKER or MAJOR remains — say
exactly which, with a reproduction, and what would close it. Then the per-item table with
one line of evidence each (what you ran or read), then anything new, then what you
verified by running versus by reading. No style remarks.

## Vocabulary and scope (read this)
This is ordinary quality assurance of the project's OWN Go library code, run only inside
its own deterministic simulator and Docker container on this machine. No network
service, third-party system or security tooling is involved. Use plain testing
vocabulary in everything you write — check, scenario, fault, malformed input,
counterexample. Write the output file EARLY (a placeholder verdict line, then the table
as you fill it) so an interrupted run still leaves a usable record; make sure the first
line is your real verdict before you stop.
