# Brief — Chunk 0b ADVERSARIAL: break the deterministic simulator

Seat: Astra (gpt-6-astra), adversarial tester (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0b-simulator (branch `chunk/0b-simulator`).
You write ONLY under `agent_work/adversarial/0b-simulator/`. Do not edit anything else in
the worktree, do not commit, do not run git write commands.

## Target
A discrete-event deterministic simulator in Go (standard library only) that five protocol
packages will be tested on: `internal/env` (Clock, Rand, Transport, Disk interfaces and
sentinels), `internal/sim` (PRNG streams, virtual clock, event loop, fault-injecting network
and disk, nemesis, event log, digests), `internal/sim/simtest` (seed handling, sweeps,
determinism check, cluster builders), `cmd/objsim`. Read `PLAN.md` section 2.1, the
builder's contract and claims in `agent_work/0b-simulator/build-notes.md` (sections
"Semantics Wave B builders must know", "Claims and the tests that prove them", "Repair
round"), the exported surface in `agent_work/0b-simulator/frozen-api.txt`, then the code.
The product Go has no comments on purpose. Everything runs through Docker from the worktree
root: `docker compose run --rm -T dev <cmd>` (repo at /src, Go 1.26.11). Not the host Go.

## The claims to attack (each will be published in the project's book)
C1. Replay. The same seed gives byte-identical event-log and state digests: twice in one
    process, across separate processes, and with `GOMAXPROCS` and `GOGC` varied, at any
    node count and under any fault schedule the API can express. Find ANY use of the
    exported API, schedule or node count where two runs of one seed differ (your own
    protocols on the simulator, not only the built-in toy; odd API orders; faults injected
    from inside handlers, timers, invariants; restart/wipe mid-request; zero and huge
    latencies; many timers at one instant).
C2. The network injects what it claims, at the configured rate: drop, duplicate, delay and
    reorder for datagrams; per-link loss; size limits; for request/response: lost request
    vs lost response, refused, timeout, and NEVER duplicated or reordered within one
    ordered pair of nodes. Check rates statistically against configuration over many seeds
    (state your tolerance). Find a fault that is claimed but not really injected, or
    injected when switched off.
C3. Partitions do not leak: a one-way (asymmetric) partition passes nothing in the blocked
    direction and everything allowed in the other; region partitions and `Isolate` likewise;
    clog holds and releases in order; heal really heals (every message sent after the heal
    arrives).
C4. Disk faults are what they claim: bit-rot is silent (the read succeeds and returns
    changed bytes) and flips what it says; wipe leaves nothing; a crash drops exactly the
    unsynced data and nothing synced; a torn crash keeps a prefix of the unsynced tail of an
    append-only blob; read errors are errors; `Damaged()` and the fault `Budget` (for
    example "no more than m damaged blobs per group") are never exceeded or under-reported
    by any sequence of faults, writes, syncs, deletes and crashes.
C5. Stream independence: extra draws on one named PRNG stream never change another stream;
    changing a network setting never changes the nemesis schedule; two nodes never share
    randomness.
C6. The loop never hangs and never lies: an empty queue, an exhausted step budget, a
    deadline, a livelock (events forever at one instant, a timer that re-arms itself at zero
    delay, a handler that sends to itself) are all reported as stalls or errors with the
    seed — never an infinite loop, never a silent success. A panic in a node, an action or
    an invariant fails the run and names the seed. Virtual time never goes backwards.
C7. Failure reporting: a failing run prints the seed, both digests, a log tail and a
    `REPLAY:` command that, run as printed, reproduces the same failure and digests
    (`simtest`, and `cmd/objsim -scenario ... -seed ...`).

## What to deliver, under agent_work/adversarial/0b-simulator/
- Harnesses as Go in ONE nested module of your own (`agent_work/adversarial/0b-simulator/
  go.mod`, module name of your choice, with a `replace` to the repository root so you can
  import the product module's `internal/...` packages — if Go's `internal` rule blocks
  that, put the harness files in a throwaway copy of the tree made by your script instead,
  never in the worktree's product directories). Outside libraries are allowed only inside
  that nested module. Bash drivers `aNN-<slug>.sh`, each runnable by someone else with no
  arguments from the worktree root, cleaning up after itself, and printing exactly one line
  per attack: `HELD <claim> <attack-name>: <one sentence>` or
  `BROKEN <claim> <attack-name>: <one sentence with the smallest reproduction, including
  the seed>`. Exit 0 when everything HELD, 1 when anything is BROKEN, 2 on a setup error.
  Decide HELD/BROKEN from what the simulator actually did, never from what you expect.
- `run-all.sh`: runs every script, collects the lines into `evidence/run-all.txt`, prints
  `SUMMARY held=N broken=M ...`.
- `ATTACKS.md`: the attacks by claim, one line each: what you tried, what happened when YOU
  ran it, tolerances used, and anything you could not test and why.
Breadth first (every claim attacked several ways), then depth on whatever cracks. Total
runtime of `run-all.sh` under about 20 minutes on 4 shared cores. Run it yourself before
you stop and report real output. A BROKEN result is the valuable outcome: make its
reproduction minimal and exact.
