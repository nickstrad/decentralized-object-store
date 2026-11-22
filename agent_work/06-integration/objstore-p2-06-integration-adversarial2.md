# Brief — Chunk 6 INDEPENDENT ROBUSTNESS TESTING, round 2: update the harness after the repair

Seat: Astra (gpt-6-astra), independent test engineer (fresh). You are in a git worktree
(branch `chunk/06-integration`). You write ONLY under `agent_work/adversarial/06-integration/`.
Do not edit anything else, do not commit, do not run git write commands. (Another process
may be running the project's gate in this worktree at the same time; it writes only under
`agent_work/06-integration/gates/`. Keep your Docker runs modest: at most 4 cores are shared.)

## Where things stand
An earlier engineer wrote the harness in `agent_work/adversarial/06-integration/`
(`ATTACKS.md` is its report; `run-all.sh` ran 284 checks, all held). Since then a repair
round changed the product (`git log --oneline -24`, account in section 10 "Repair round"
of `agent_work/06-integration/build-notes.md`, findings in
`agent_work/06-integration/REVIEW-1.md`):
- the HTTP server moved from `internal/storage` to a new package `internal/storage/httpapi`
  (status rows, request/response types and API error codes stay in `internal/storage`);
- the real runtime moved from `internal/node` to a new package `internal/noderuntime`
  (the harness still refers to `node.Runtime`, so `run-all.sh` now ends with set-up
  errors: 18 held, 0 broken, 4 set-up errors);
- an `If-Match` naming an entity-tag the node does not hold now returns 412, not 400;
  conditional headers are parsed as RFC 9110 lists and a present precondition is never
  ignored;
- migration: before retiring the source, every live peer must agree with the coordinator
  on the route, the flip owner and a digest of the bucket's manifests; a purge deletes
  only versions with a recorded death marker; shard deletes over the wire are refused;
- quota usage is recounted after a discarded object write and audited;
- file-backed disk: fsync before rename, directory fsync, short writes handled.

## What to do
1. Make the harness build and run against the current tree (package path and any API
   that moved). Every check that existed must still exist; if one cannot be kept, say why
   in the report. Re-judge any expectation the repair changed on purpose (list each).
2. Add scenarios for the fault class the review found and the first harness did not
   cover — the review's findings are the list of what a 20-seed sample missed:
   - writes (create, overwrite, delete) that keep arriving on NON-coordinator nodes
     through a whole bucket migration, with message loss and delay, in both outcomes
     (completed and rolled back): afterwards every acknowledged write is readable, or was
     superseded or deleted by a later acknowledged write, or appears in a discard record;
     no key that the metadata lists as live is unreadable;
   - a node that is paused or partitioned while a migration runs and returns holding a
     write the others never saw;
   - two migrations started at the same instant by different coordinators (same bucket;
     different buckets);
   - quota usage equals an independent recount from the live objects on every node after
     every heal, including when an object write (not a quota update) was discarded;
   - HTTP: for every method that takes a precondition, a request carrying ANY
     precondition header (empty, whitespace, malformed, a list, a weak validator, `*`) is
     never applied as an unconditional write — compare against your own small model;
   - the file-backed disk under simulated crash points between write, sync and rename
     (use whatever fault layer the package exposes for tests; do not touch product code);
   - shutdown: a panic on the node loop, and shutdown during a migration.
   Use more seeds than before where a scenario is cheap (aim for 50–100 per scenario for
   the migration-with-traffic scenarios), and say the sample sizes.
3. Run `run-all.sh` yourself (keep it under about 25 minutes) and update `ATTACKS.md`:
   what changed in the harness, the numbered claims and checks, real output, exact
   minimal reproductions (with seed) for anything BROKEN, and what you could not test.
Output format and exit codes as before: one line per check, `HELD <claim> <check-name>:
...` or `BROKEN <claim> <check-name>: ... (smallest reproduction, incl. seed)`; exit 0 all
held, 1 any broken, 2 set-up error; `SUMMARY held=N broken=M setup_errors=K ...` at the end
of `evidence/run-all.txt`. Decide from what the code actually did. Docker only:
`docker compose run --rm -T dev <cmd>` from the worktree root.

## Design note (binding)
Concurrent versions are NOT kept side by side: last-writer-wins by hybrid logical clock
picks one version on every node, the other is discarded and every discard is counted and
logged. Losing an acknowledged write to a CONCURRENT write is the stated design, not a
counterexample, provided the loss is on a discard record. Counterexamples are: a GET
returning wrong bytes; an acknowledged write that is neither readable, nor superseded,
nor deleted, nor on a discard record; a key listed as live whose object cannot be read
after the cluster has healed; replicas that end with different metadata; a deleted object
coming back; the winning version's shards collected; a precondition ignored; a goroutine
or socket that outlives shutdown.

## Vocabulary and scope (read this)
This is ordinary quality assurance of the project's OWN Go library code, run only inside
its own deterministic simulator and Docker container on this machine: property checks,
fault-injection scenarios, malformed-input tests and differential oracles. No network
service, third-party system or security tooling is involved. Use plain testing vocabulary
in everything you write — check, scenario, fault, malformed input, counterexample. Update
the report file EARLY (after your first full run) and keep it current, so an interrupted
run still leaves a usable report.
