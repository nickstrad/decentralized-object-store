# Brief — Chunk 6, closing the arbitration's remaining MAJOR (integration)

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/06-integration`).
Binding rules: `agent_work/06-integration/objstore-p2-builder-rules.md` (no comments in Go,
Docker only, test first, commit here, never push). Stage only the files you change, by
explicit path. Do not edit `agent_work/gates/`, `agent_work/orch/` or `agent_work/adversarial/`.

Read `agent_work/06-integration/ARBITRATION-1.md` in full (verdict FAIL; its table judges
the six review-2 findings fixed; ONE MAJOR remains, with a deterministic reproduction and
a self-contained Go test in the file), then the passages it cites and the "Residual
limit" paragraphs of `agent_work/06-integration/build-notes.md`. The arbiter's verdict is
final: the chunk lands only when this is closed.

## The defect
A PUT is acknowledged at k+1 durable shards; the remaining holders may never have stored
theirs (the storage layer allows outstanding shard requests after the acknowledgement).
If the writer is then cut off before its manifest spreads, the holders that did store
shards see them as orphans and, after the orphan grace, record and collect them. When
the writer returns, the loss detector counts only ORPHAN RECORDS per stripe and declares
the version lost only when more than m were collected. A stripe that lost m (or fewer)
shards to collection and had another shard that was never stored has fewer than k
shards left, cannot be rebuilt, and is never declared lost: the manifest stays live and
every GET fails forever (k=2, m=2, quorum 3: two collected, one never written, one
left). The same arithmetic holds at the production layout (4+2, quorum 5).
This violates the guarantee the notes state: after heal, every key the metadata lists
as live is readable, unless it is declared lost on record.

## What to build
Close it in the code. The arbiter's constraints bind you:
- Do NOT simply lower the orphan-count threshold (that would discard recoverable objects
  whose shards all landed).
- Decide loss on durable evidence of RECOVERABILITY, not on a count of one cause: a live
  version is declared lost when the cluster has established that fewer than k shards of
  one of its stripes can still be produced — however they went missing (collected as
  orphans, never written, rotted beyond repair, lost with a disk). Design what "has
  established" means so that it can never fire while a holder that might still have a
  good shard has simply not been heard from: state exactly which evidence from which
  holders is required (for example a positive "I do not hold a verifiable shard
  (version, stripe, index)" statement from every placement holder that is a current
  member, replicated like the orphan records, plus the view being settled), how long the
  cluster waits, and what a holder that is down or partitioned does to the decision
  (it must block it, not be counted as missing). An alternative you may choose instead,
  if you can prove it: change collection or acknowledgement so this state cannot arise
  (for example a holder never collects an orphan shard of a version whose writer may
  still hold an acknowledged manifest — think about what bounds that). Whichever you
  choose, write the rule down in the notes as "when is a version declared lost, and on
  what evidence", and state its cost.
- Once declared lost, the outcome is on the record and visible (the lost marker, the
  health surface, a log line), GET returns the status the plan's table gives for a
  permanently lost object rather than a transient "unavailable", and the remaining
  shards are collected through the normal death-evidence path.
- The declaration must be safe under last-writer-wins and replication: no node may
  declare a version lost and collect its remaining shards while another node can still
  serve or rebuild it.

## Tests (first, failing on the current code)
1. The arbiter's reproduction, taken from ARBITRATION-1.md (put it in the package's test
   files under a name of yours), and its readable control (all shards landed, the same
   isolation: the object must stay READABLE and must NOT be declared lost).
2. The same at the production layout 4+2 with quorum 5, and with a multi-stripe object
   where only one stripe is unrecoverable.
3. Over-eager declaration: a holder that is merely down or partitioned at decision time,
   then returns with good shards — the version must not have been declared lost.
4. Unrecoverable for other reasons (more than m shards rotted beyond repair with no
   orphan record at all): readable-or-declared-lost must hold there too.
5. Keep the multi-tick and the 1/2/more-than-window size cases.
6. The full-node DST oracle's post-heal invariant ("every key listed live is readable
   unless declared lost on record") must cover this class: extend at least one sweep
   scenario so a writer is isolated right after an acknowledgement with a holder
   unreachable, long enough for orphan collection, then healed; and make the oracle also
   assert the converse — nothing readable-and-recoverable is ever declared lost.

Then, through Docker: `gofmt -l .` empty; `go vet ./...`; `go test ./internal/node/...
./internal/noderuntime/... ./internal/storage/... ./cmd/...`; `-race` on the same;
`go test ./internal/node -rapid.checks=2000 -run 'Prop'`; the determinism lint over the
gate's roots clean; `DST_SEEDS=100 go test -count=1 ./internal/node`. On the host:
`agent_work/06-integration/cluster-smoke.sh` and
`agent_work/adversarial/06-integration/run-all.sh` (record the SUMMARY line; restore its
evidence files with `git checkout -- agent_work/adversarial`).
Commit in small commits; add an "Arbitration fix" section to
`agent_work/06-integration/build-notes.md` (the rule, the evidence required, each test,
the cost, and the residual limit restated EXACTLY as the code now behaves — no stronger)
and correct every earlier sentence in the notes that claims more.
Final message: commit list and three lines. Do not run the full gate. Do not claim
anything you did not run.
