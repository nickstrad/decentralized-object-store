# Brief — Chunk 6 SECOND-EYES pass: review and independent probing of the loss-declaration fix

Seat: Gemini 4 Neon, reviewer and tester (fresh). You are in a git worktree made for this
pass only (branch `review/06-integration-neon2eyes`, a copy of the chunk branch). The owner
ordered this pass so that two independent seats examine the same fix before it lands. Your
judgement must be your own: work only from this worktree at its HEAD. Do not read other
branches, other worktrees, later commits (`git log --all`, `git show <other ref>`), or
anything under `/root/runs` or `/root/briefs`. No git write commands, no commits.

The machine has 4 shared cores and another test run is using them: run package-level
tests with `GOMAXPROCS=2`, never the whole module at 500 seeds.

## The target
The fix for the one MAJOR left by `agent_work/06-integration/ARBITRATION-1.md` (read it
first; it contains the arbiter's deterministic reproduction as a self-contained Go test): a
PUT acknowledged at quorum whose writer is cut off could lose m shards to orphan collection
while another shard was never written; the stripe was then unrecoverable but never declared
lost, so the key stayed live and unreadable.

The fix is `git diff 1dc99da..HEAD -- internal` (`git log 1dc99da..HEAD`). Commit `2d34871`
holds the new tests without the product code. The builder's account, with every claim it
makes, is section 14 ("Arbitration fix") of `agent_work/06-integration/build-notes.md`:
read all of it, including "Residual limit". Product Go is comment-free on purpose (another
seat writes comments later): not a finding.

What was built, in short:
- **Binding absence records.** A placement holder writes a replicated, immutable record
  that it holds no verifiable shard of (version, stripe, index) only after its rebuild got
  an answer from the holder of every other index of the stripe and fewer than k shards
  verified. After that it never stores, rebuilds or serves that shard.
- **Loss-declaration rule.** A version is declared lost only when more than m holders of
  ONE stripe hold absence records. A holder that cannot be heard from blocks the decision.
- **Three safeguards added late:** absence records of a holder whose ID contains `/` are
  no longer ignored; shard writes are refused before the node's metadata has loaded; the
  loss record is written before the manifest tombstone.
- **Tests:** `internal/node/lost_test.go`, `internal/node/absent_internal_test.go`, the
  `orphanedAfterAck` part of `internal/node/dst_putget_test.go`, the converse oracle in
  `internal/node/helpers_test.go` and `internal/node/oracle_test.go`.
- **Documented remaining case:** a holder whose disk is wiped after it wrote an absence
  record, plus a shard write that arrives late (section 14, "Residual limit", cases (a)
  and (b)); in case (a) the key stays lost, on record.

Code: `internal/node/inventory.go` (`stateAbsent`, `statedAbsent`, `noteAbsent`,
`unrecoverableStripe`, `declareLost`, `rebuild`, `serveStorage`), plus whatever those
touch in `internal/node`, `internal/storage`, `internal/occ` and `internal/noderuntime`.

## Part A — code review of the fix against its stated guarantees
For every guarantee and every "cannot happen" sentence in section 14, decide from the code
whether it is true. In particular:
1. Is an absence record written ONLY under the stated conditions (every other holder
   answered; a timeout, not-ready or unreachable reply is never an answer; fewer than k
   verify; the local shard re-checked)? Any path that writes one on weaker evidence?
2. Is the record really binding on every path that can put a shard on that holder's disk
   (wire writes, rebuild, migration copy, anti-entropy repair, restart, a write racing the
   record's own commit)?
3. Declaration: only records from the manifest's actual placement holder of that index
   count; more than m in ONE stripe; correct for every layout including m=0 and k=1, many
   stripes, the last short stripe; two nodes that each think they are the declarer;
   metadata arriving in any order; a staged migration copy; a concurrent overwrite or
   delete of the same key (a newer version must never be removed without a counted, logged
   discard).
4. The three safeguards: are they correct and are their tests able to fail?
5. The tests: does each named test prove what section 14's table says? Find tests that
   cannot fail, assert nothing, or pass for the wrong reason. Is the converse oracle
   (nothing recoverable is ever declared lost) installed in every full-node scenario, and
   would it fail when it should?
6. Is the "Residual limit" paragraph exactly what the code does — no stronger, no weaker?
   Is the remaining case acceptable as a stated residual, or is it a MAJOR?
7. Anything the fix broke elsewhere: a determinism leak in `internal/node` (no goroutines,
   locks, wall clock, map-order dependence), unbounded metadata growth, a weakened or
   removed test, an edit to the frozen packages `internal/env` and `internal/sim`.

## Part B — your own independent probing
Devise and RUN your own scenarios against HEAD. This is not a rerun of anyone else's
checks and not a rerun of the existing tests: design schedules the existing tests do not
cover. Aim them at the four areas the owner named:
- **orphan collection** (which shards get collected, when, under which partitions and
  grace periods; collection racing a manifest that arrives late);
- **loss declaration** (a version declared lost while k verifiable shards of every stripe
  exist or can still be produced; a version that is unrecoverable, with every holder up
  and connected, and is never declared; declarations racing overwrites, deletes,
  migrations, changing membership views);
- **holder restarts** (crash and restart, pause and resume, at each point around the
  absence record, the loss record and the tombstone; a holder that restarts with its disk
  intact versus wiped);
- **late-arriving shards** (a writer's shard request held in the network and delivered
  after orphan collection, after an absence record, after a declaration, after a restart).
Use the simulator harness the package already has (`newHarness`, `SetLinkLoss`, `Isolate`,
`HealAll`, `Crash`/`Restart`, `WipeRejoin`, pause, held messages — read
`internal/node/helpers_test.go` and `lost_test.go` for how). Write each scenario as a Go
test in a NEW file named `internal/node/neon2_<topic>_test.go` (do not edit any existing
file; these files are kept as a record). Aim for at least eight distinct scenarios across
the four areas, with several seeds or layouts where that is cheap. A scenario that passes
is evidence; a scenario that fails is a finding only after you have checked it is the
product's fault and not the scenario's — say which, and why.

Also do these two fixed checks:
- Put the arbiter's reproduction from `ARBITRATION-1.md` in
  `internal/node/neon2_arbiter_repro_test.go` and run it unchanged: report exactly what
  happens on HEAD (it was written to pass on the broken tree), then a variant asserting the
  fixed end state (manifest not live, `~lost/<version>` present on every node, GET not
  found).
- Confirm at least two of the `...DeclaredLost` tests fail on the tree before the fix:
  `git archive 2d34871 | tar -x -C /tmp/neon2-prefix` and run them there with
  `docker run --rm -v /tmp/neon2-prefix:/src -w /src objstore-dev:local go test ...`.

Run everything through Docker from the worktree root, for example:
`docker compose run --rm -T -e GOMAXPROCS=2 dev go test -count=1 ./internal/node -run 'Neon2' -v`
(also `-race`; `-e DST_SEEDS=50` with `-run 'DST$'`; `-rapid.checks=2000` with `-run Prop`).
Do not start the `cluster` compose profile.

## Output: agent_work/06-integration/NEON-SECOND-EYES.md
Write this file EARLY (a placeholder first line, then fill it as you go) so an interrupted
run still leaves a record; make sure the first line is your real verdict before you stop.
- First line exactly one of `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`.
- Numbered findings, each graded `BLOCKER`, `MAJOR` or `NIT`, with file and line, what is
  wrong, a concrete reproduction (the name of your `neon2_*_test.go` test, or a seed and
  steps) and the smallest fix. FAIL needs at least one BLOCKER or MAJOR: wrong behaviour,
  a guarantee in section 14 that is false, or a test that cannot detect the defect it
  names. A stated residual that is described exactly is not a finding unless you judge it
  unacceptable — then say what closure you would accept.
- Part A: one short paragraph per numbered question (what you read, what you concluded).
- Part B: a table of every scenario you ran — test name, area, what it sets up, what it
  expects, result (held / broken), seeds and layouts.
- Last: what you verified by running versus by reading, and the exact commands.
No style remarks. Besides this file and your `internal/node/neon2_*_test.go` files, leave
nothing behind in the worktree; temporary exports go under `/tmp/neon2-*`.
