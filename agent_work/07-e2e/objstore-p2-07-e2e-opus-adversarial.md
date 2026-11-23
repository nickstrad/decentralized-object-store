# Brief — Chunk 7 ADVERSARIAL PASS on the finished end-to-end suite (Opus)

Seat: Claude Opus, adversarial tester (fresh agent). You are in a git worktree (branch
`chunk/07-e2e`); it is your current directory. You write ONLY under
`agent_work/adversarial/07-e2e-opus/`. Do not commit, do not run git write commands, and
leave the tracked tree exactly as you found it when you stop (`git status` must show only
your new directory). `agent_work/adversarial/07-e2e/` is the record of an earlier,
interrupted harness by another seat: read it for ideas if you like, never edit it, and do
not repeat it wholesale.

By owner ruling the seats for this chunk's finish are: Gemini 4 Neon finished the chunk as
builder, Fable reviewed it, and you run the adversarial pass. Your result gates the landing:
a blocker or major you find must be fixed or answered before the chunk lands.

## Target

`test/e2e` (the end-to-end suite: scenarios E1–E9 on the simulator, "tier S", and on real
`objnode` processes, "tier R"; the nemesis-and-heal runner; the whole-cluster fuzz target
`FuzzClusterNemesis`; planted-fault canaries), with `internal/nodetest` and the small
product changes it forced in `internal/node`, `internal/noderuntime` and `cmd/objnode`.
The thing under attack is mostly a TEST SUITE: the question is whether it would catch a
broken object store, not only whether the object store is right.

Read: PLAN.md section 2 (2.7 has the E1–E9 claims; 2.9 budgets) and the item "Chunk 7" in
4.2 with its adversarial bullet; `agent_work/07-e2e/build-notes.md` (sections 3, 4 and 6,
and the finish-round sections 11 to 14); `agent_work/07-e2e/REVIEW-1.md` (the review whose
findings the finish closed); `agent_work/07-e2e/FABLE-REVIEW.md` (the review of the
finish, two rounds: FAIL, then PASS after one fix round — its findings 1 to 3 changed the
E7 per-migration check, the wipe guard and the availability reads, so attack those too); the simulator contract in `agent_work/0b-simulator/build-notes.md`. Everything runs
through Docker from the worktree root: `docker compose run --rm -T dev <cmd>`; never the
host Go. The machine has 4 shared cores and 7 GB: one heavy run at a time.

## Part 1 — FIRST AND ABOVE ALL: do the three repaired coverage holes now bite?

REVIEW-1 found three holes. Each was repaired with new assertions and a kept canary. For
each, attack the repair with mutations of your own — not only the builder's canary. Work on
a scratch COPY of the tree (for example `cp -a` the worktree's tracked files to a
directory under `/tmp`, or use an overlay inside the container) so the worktree itself is
never modified; your drivers must do the copying themselves and clean up.

1. **The fuzzed migrating bucket.** Before: `FuzzClusterNemesis` ran 3 nodes per region at
   k=4, m=2, so the routed bucket never held an object and every fuzzed migration moved
   nothing. Show, with your own instrumentation, how many objects and shards the routed
   bucket holds and how many shards migrations move across the seed corpus and a short
   fuzz run. Then mutate: restore an unplaceable layout; make the routed bucket's PUTs
   fail another way (wrong region, quota); make the migration's copy, verify or purge step
   a no-op in product code. Does the suite (the new check, the fuzz target on its corpus,
   E4, E5, E7) fail each time, and for the right reason? Are there fault mixes under which
   the bucket goes back to being empty for most of a run without the check noticing?
2. **A migration start whose outcome is unknown.** Before: `finishMigration` treated
   `ErrTimeout` / `ErrUnreachable` from `StartMigration` as "nothing started". Mutate the
   runner back in parts (forget only on `ErrUnreachable`; skip the region refresh; skip the
   post-migration shard check; unblock the coordinator wipe while the outcome is unknown)
   and mutate the product (a migration that is started but never finishes after a lost
   reply; one that completes but leaves shards in the source region). Which mutations
   survive? Also build your own schedules: reply lost with the coordinator then paused,
   crashed and restarted, or cut off for longer than the heal bound.
3. **Partition-time reads and writes in E2.** Before: E2 never required any object read or
   write to succeed while split. Mutate the product so that, while a region is cut off,
   object GET and/or PUT answer `ErrUnavailable` always; only on the minority side; only on
   the majority side; only for keys written before the cut; only on tier R. Mutate so a
   partition-time GET returns stale or wrong bytes. Which mutations does E2 catch on each
   tier? Is any "must succeed" assertion vacuous (zero iterations, a side that never
   qualifies, a tier where it is skipped)?

For every mutation record: the exact diff (kept as a patch file under your directory), the
command, and whether the suite KILLED it (which test, which message) or it SURVIVED. A
surviving mutation that breaks a claim the suite makes is a finding; grade it.

## Part 2 — the same question for the rest of the finish

The finish also closed: an E4 canary that matched any failure; discards and E9 traffic
never required to be non-zero; `s.Err()` versus `s.Verify()` in the fuzz target; the
fault-injection blocklist reload; the nemesis runner accepting any terminal error on GET,
tag and delete; the real-cluster constructor accepting a node that failed to start; E4's
object sizes and seed share. Spot-check each with one or two mutations or planted results.
Then run a broader mutation sample over the scenario oracles (`simoracle_test.go`, the
ledger and lineage oracles, the spread, orphan, loss and deletion checks): for each oracle,
one mutation of the product that the oracle exists to catch. Every canary in
`canary_test.go`: does it fail for its planted reason and only for it?

## Part 3 — the usual robustness probing of the chunk

As far as time allows, breadth first: your own seeds and fault schedules on tier S (not the
builder's corpus); nemesis plans built from bytes at the edges of `planFromBytes` (empty,
maximal, all one fault kind, zero-length cycles); determinism (same seed twice gives the
same trace; the REPLAY command printed on failure really replays); tier R hygiene (no
stray processes, ports or temp directories after a failed or killed test; the default
`objnode` binary carries no fault hook); resource bounds (memory and time per fuzz
execution; an input of 64 KiB). Product defects found on the way count as findings like any
other.

## Deliver, under agent_work/adversarial/07-e2e-opus/

- Bash drivers `aNN-<slug>.sh`, each runnable with no arguments from the worktree root,
  cleaning up after itself, printing exactly one line per check:
  `HELD <claim> <check-name>: <one sentence>` or
  `BROKEN <claim> <check-name>: <one sentence with the smallest reproduction, incl. seed>`.
  For mutation checks HELD means the suite killed the mutation. Exit 0 when everything
  HELD, 1 when anything is BROKEN, 2 on a setup error. Decide from what the code actually
  did. Any Go helper code lives in one nested module of your own (path under the product's
  import path with a `replace` to `../../..`, as `agent_work/adversarial/06-integration/`
  does). `mutations/` holds the patch files.
- `run-all.sh`: runs every driver, collects the lines into `evidence/run-all.txt`, prints
  `SUMMARY held=N broken=M setup_errors=K`. Target total runtime: under about 40 minutes.
- `ATTACKS.md`: write a skeleton after your first full run and keep it current, so an
  interrupted run still leaves a usable report. Contents: a verdict line at the top
  (`ADVERSARIAL VERDICT: CLEAN | FINDINGS`), then every finding graded BLOCKER / MAJOR /
  MINOR / NOTE with the smallest reproduction (seed, command, patch file); the mutation
  table for Part 1 in full (mutation, killed-by or SURVIVED); what you tried that held;
  what you could not test and why. Say what you ran versus what you only read.
Run `run-all.sh` yourself before you stop and report its real output. Do not claim anything
you did not run.
