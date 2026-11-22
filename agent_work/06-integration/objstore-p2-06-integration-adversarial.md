# Brief — Chunk 6 INDEPENDENT ROBUSTNESS TESTING of integration: the object store and sync engine

Seat: Astra (gpt-6-astra), independent test engineer (fresh). You are in a git worktree (branch
`chunk/06-integration`). You write ONLY under `agent_work/adversarial/06-integration/`. Do not edit anything
else, do not commit, do not run git write commands.

## Target
`internal/node`, the HTTP object API in `internal/storage`, and `cmd/objnode`: reviewed code of a Go project (standard library only) tested by deterministic
simulation. Read the plan item "Chunk 6" in PLAN.md section 4.2 — its "Adversarial (Astra)"
bullet is your test list and is binding — then the builder's claims in
`agent_work/06-integration/build-notes.md` (the claims table: each row is a claim the project will
publish in its book), then the code and tests. The simulator the tests run on is described
in `agent_work/0b-simulator/build-notes.md` and `frozen-api.txt`; the earlier harness
`agent_work/adversarial/0b-simulator/` shows a working layout (a nested module whose path
sits under the product's import path, with a `replace` to `../../..`, so `internal/`
packages can be imported). Everything runs through Docker from the worktree root:
`docker compose run --rm -T dev <cmd>`. Not the host Go.

## What to test
Number the claims C1..Cn yourself from the claims table and the plan item, and test each
several ways; every item of the plan's adversarial bullet must appear as at least one
check. Prefer checks through the package's exported API on the simulator with your own
seeds, schedules, sizes and malformed inputs; outside libraries are allowed ONLY inside your
nested module (for example an independent reference implementation as a differential
oracle). Statistical claims: state your tolerance and sample size.

## What to deliver, under agent_work/adversarial/06-integration/
- Harness code in ONE nested Go module of your own; bash drivers `aNN-<slug>.sh`, each
  runnable by someone else with no arguments from the worktree root, cleaning up after
  itself, printing exactly one line per check:
  `HELD <claim> <check-name>: <one sentence>` or
  `BROKEN <claim> <check-name>: <one sentence with the smallest reproduction, incl. seed>`.
  Exit 0 when everything HELD, 1 when anything is BROKEN, 2 on a setup error. Decide from
  what the code actually did, never from what you expect. A driver must not score a
  correct rejection (an error returned for an invalid input) as BROKEN.
- `run-all.sh`: runs every driver, collects lines into `evidence/run-all.txt`, prints
  `SUMMARY held=N broken=M setup_errors=K ...`. Write evidence only to files that are NOT
  rewritten differently on each run unless they are the run's result (keep the tree quiet).
- `ATTACKS.md`: the numbered claims, the checks by claim (what you tried, what happened
  when YOU ran it, tolerances), exact minimal reproductions for every BROKEN, and what you
  could not test and why.
Breadth first, then depth on whatever cracks. Total `run-all.sh` runtime under about 20
minutes on 4 shared cores. Run it yourself before you stop and report real output.

## Vocabulary and scope (read this)
This is ordinary quality assurance of the project's OWN Go library code, run only inside
its own deterministic simulator and Docker container on this machine: property checks,
fault-injection scenarios, malformed-input decoding tests and differential oracles. No
network service, third-party system or security tooling is involved. In everything you
write (code identifiers, commit-free notes, CHECKS.md, your own planning text) use plain
testing vocabulary — check, scenario, fault, malformed input, counterexample — and avoid
security-flavoured words. The plan's bullet titled "Adversarial (Astra)" is simply the
list of scenarios to cover. Name the report `ATTACKS.md` as the tooling expects, but write
it as a test report. Write the report file EARLY (a skeleton after your first full run)
and update it as you go, so an interrupted run still leaves a usable report.

## Design note for this chunk (binding)
Concurrent versions are NOT kept side by side. When two writers update one key without
seeing each other, last-writer-wins by hybrid logical clock picks one version on every
node, the other is discarded, and every such discard is counted and logged. Losing an
acknowledged write to a concurrent write is therefore the stated design, NOT a
counterexample. Counterexamples are: a GET returning wrong bytes; an acknowledged PUT
inside the fault budget that becomes unreadable; replicas that end with different
metadata; a discarded write with no record on any surviving node; two versions shown to
a reader; the winning version's shards collected; a deleted object coming back; a
goroutine or file descriptor that outlives shutdown.
Scope note: the full-node scenarios are heavy. Keep the whole `run-all.sh` under about
25 minutes on 4 shared cores (small objects, 9–21 nodes, tens of seeds per scenario, not
hundreds), and say in the report what sample sizes you used. Real-socket checks use the
loopback runtime inside the dev container only.
