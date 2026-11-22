# Brief — Chunk 6 CONFIRM pass: the fix for ARBITRATION-1's remaining MAJOR

Seat: Astra (gpt-6-astra), arbiter (fresh). You are in a git worktree (branch
`chunk/06-integration`). READ-ONLY: do not edit, commit, or run git write commands. The
ONLY file you create is `agent_work/06-integration/ARBITRATION-2.md`. Your verdict is
final for this chunk. The machine has 4 shared cores: run package-level tests, not the
whole module at 500 seeds.

## Why you are here
`agent_work/06-integration/ARBITRATION-1.md` (your seat, verdict FAIL) judged the six
review-2 findings fixed and left ONE MAJOR with a deterministic reproduction: a PUT
acknowledged at quorum whose writer is cut off can lose m shards to orphan collection
while another shard was never written; the stripe is then unrecoverable but was never
declared lost, so the key stayed live and unreadable. Read that file first.

The fix is `git log 1dc99da2ac2e23395a431a973a007ae9a9798b2d..HEAD` (1dc99da2ac2e23395a431a973a007ae9a9798b2d is the commit that briefed it). The owner
ruled that this round has no separate review: YOUR independent testing is the only check
between this fix and landing. Be thorough, and try hard to find a counterexample.

The fix rule the owner set: a version is declared lost on durable evidence of
recoverability — never on a count of one cause, and never while a holder that might
still have a good shard is merely unreachable. The account of what was built is section
14 ("Arbitration fix") of `agent_work/06-integration/build-notes.md`: the rule, the
evidence required, the tests, the cost, and the residual limit. The code is in
`internal/node/inventory.go` (`stateAbsent`, `statedAbsent`, `noteAbsent`,
`unrecoverableStripe`, `declareLost`, `rebuild`, `serveStorage`); the tests are
`internal/node/lost_test.go`, `internal/node/absent_internal_test.go`, the
`orphanedAfterAck` part of `internal/node/dst_putget_test.go`, and the oracle additions
in `internal/node/helpers_test.go` and `internal/node/oracle_test.go`.

## What to decide
1. Is the MAJOR closed? Run your own reproduction from ARBITRATION-1.md unchanged against
   HEAD (put it in a temporary export under /tmp, not in the worktree): the version must
   end declared lost on record on every node (manifest not live, `~lost/<version>`
   present, GET answers not found), not live and unreadable. Run the readable control.
   Confirm the new `...DeclaredLost` tests fail on the tree before the fix (export
   commit 2d348714503903515204e2cf6649b07e716956a6, which holds the tests without the code, with `git archive`).
2. Safety — look for a counterexample to each of these, by scenario and by reading:
   - A version is declared lost while k verifiable shards of every stripe exist on its
     holders' disks, or can still be produced (a holder that is down, paused, slow,
     partitioned or restarting and later returns with a good shard; a late shard request
     from the writer; a rebuild in flight; a crash between the absence record and
     anything else; a restart of a holder that wrote a record; metadata arriving in any
     order; two nodes each believing they are the designated declarer; a migration of the
     bucket in progress; a concurrent overwrite or delete of the same key).
   - A node collects shards of a version another node can still serve or rebuild.
   - An absence record that does not bind its writer (the holder later stores, rebuilds
     or serves that shard).
   - Under last-writer-wins and replication: a record erased, a declaration that removes
     a NEWER version of the key without a counted, logged discard.
3. Liveness — once every holder of an unrecoverable stripe is up, holds the manifest and
   can reach the others, is the version always declared lost (all layouts, 1 / 2 /
   more-than-window stripes, mixed causes: orphan collection, never written, bit rot)?
   The notes state that a holder that never returns keeps the key live and unreadable
   (the owner's rule requires an unreachable holder to block the decision); judge whether
   the "Residual limit" paragraph says exactly what the code does — no stronger. The
   notes also name one path as not closed (a holder whose disk is wiped after it wrote
   an absence record, plus a shard write from the writer held in the network across the
   wipe; "Residual limit", cases (a) and (b)). An earlier run of this brief built that
   scenario for case (a): it is now
   `TestAStaleShardWriteToAWipedHolderOfADeclaredLostVersionIsCollected`, which shows the
   late shard lands on a version already dead on record and is collected, with the key
   still lost on record everywhere. A closure that refused shard writes on a node
   started with empty metadata was built and withdrawn because it refused the first
   writes of every new cluster. Judge whether cases (a) and (b) are stated exactly, and
   whether what remains is acceptable as a stated residual (it needs a disk loss on a
   holder of a stripe already below k AND a request that outlives the holder's restart,
   which the real TCP runtime cannot deliver) or is a MAJOR; if a MAJOR, say what
   closure you would accept that does not delay writes on a new cluster.
4. Did the fix introduce a new BLOCKER or MAJOR elsewhere (wrong behaviour, a determinism
   leak in `internal/node` — no goroutines, locks, wall clock or map-order dependence —
   a weakened or removed test, an edit to the frozen packages `internal/env`,
   `internal/sim`)? Is the converse oracle (nothing recoverable is ever declared lost)
   really installed in every full-node scenario, and would it fail when it should?

Everything runs through Docker from the worktree root: `docker compose run --rm -T dev
go test ./internal/node/...` (also `./internal/noderuntime/...`,
`./internal/storage/...`, `./cmd/...`; `-race`; `DST_SEEDS=50`; `-rapid.checks=2000`).
Leave nothing behind outside /tmp and your one output file.

## Output: agent_work/06-integration/ARBITRATION-2.md
First line exactly one of: `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`.
PASS: the chunk may land. NITS: it may land once the listed small items are closed (list
each precisely, with file and the smallest fix). FAIL: a BLOCKER or MAJOR remains — say
exactly which, with a reproduction (a self-contained Go test), and what would close it.
Then one line of evidence per numbered question (what you ran or read), then anything
new, then what you verified by running versus by reading. No style remarks.

## Vocabulary and scope (read this)
This is ordinary quality assurance of the project's OWN Go library code, run only inside
its own deterministic simulator and Docker container on this machine. No network
service, third-party system or security tooling is involved. Use plain testing
vocabulary in everything you write — check, scenario, fault, malformed input,
counterexample. Write the output file EARLY (a placeholder verdict line, then the
answers as you fill them) so an interrupted run still leaves a usable record; make sure
the first line is your real verdict before you stop. An earlier run of this brief was
stopped by the provider's content filter after about four minutes, with only the
placeholder written: keep every line you write, including test names, failure messages
and shell commands, in the plain testing vocabulary above, and write each answer into
the output file as soon as you have it.
