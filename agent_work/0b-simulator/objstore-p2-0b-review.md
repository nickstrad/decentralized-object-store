# Brief — Chunk 0b REVIEW: the deterministic simulator

Seat: Gemini 4 Neon, reviewer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0b-simulator (branch `chunk/0b-simulator`).
READ-ONLY review: do not edit code, do not commit, no git write commands. The ONLY file you
create is `agent_work/0b-simulator/REVIEW-1.md`. Write that file before you stop, even if a
question is only partly answered (say which).

## What you are reviewing
`git diff fde6401..HEAD -- internal cmd` (8 commits by an Opus builder). Product Go is
comment-free ON PURPOSE (comments are written later by another seat) — the absence of
comments is not a finding. Read:
- the builder's brief `agent_work/0b-simulator/objstore-p2-0b-simulator-build.md` and rules
  `agent_work/0b-simulator/objstore-p2-builder-rules.md`;
- PLAN.md sections 2.1–2.6, 4.2 "Chunk 0", 5.1, 7.1;
- `agent_work/0b-simulator/build-notes.md` (claims, measurements) and `frozen-api.txt`;
- the code: `internal/env`, `internal/sim` (+ sub-packages), `internal/sim/simtest`,
  `cmd/objsim`.
You may run tests through Docker: `docker compose run --rm -T dev go test ./...` (also
`-race`, `DST_SEEDS=50 go test ./internal/sim/...`). Do not rely on the host Go.

## Why this review matters most
When this chunk lands, the exported API of `internal/env`, `internal/sim` and `simtest` is
FROZEN and five builders (SWIM gossip; Reed-Solomon + chunk store; Merkle anti-entropy; OCC
metadata; Sagas) build on it at once, then an integration chunk runs all five protocols on
one node over one Transport, on the simulator AND on a real UDP/TCP/file runtime. A wrong
interface now costs every later chunk.

## Questions (number your findings)
1. Interface fitness. For each of the five later protocols, walk through what it needs
   (PLAN 4.2 Chunks 1–6) and say whether `env.Clock/Rand/Transport/Disk`, the process/factory
   interface and `simtest` can express it without changing the frozen API: periodic timers
   and cancel/reset; datagrams with a size limit; request/response where the SERVER answers
   later (after its own fan-out); idempotent retries with lost responses; several protocols
   multiplexed on one node; crash/restart over a durable disk with torn tails; wipe and
   rejoin with a new identity epoch; region labels; fault budgets like "no more than m
   shards of one stripe"; invariants over the whole cluster; workloads from a seeded stream;
   a real-runtime implementation of the same interfaces with goroutines behind them (is
   anything in `env` impossible or racy to implement over real sockets and files?).
   The builder notes two gaps (no request re-delivery fault; no mid-run clock jump): judge
   whether either must be fixed BEFORE the freeze.
2. Determinism. Find any path where two runs of one seed can differ: map iteration reaching
   an output, a draw from the wrong stream, time or randomness outside `env`, pointer or
   address-dependent order, float arithmetic in a decision, state shared between nodes,
   test-only switches that leak. Are the replay meta-tests able to fail (not vacuous)? Do the
   three canaries prove the harness catches what they plant?
3. Fault model correctness against PLAN 2.1: every network fault for both delivery classes
   (drop, duplicate, delay/reorder, size limit; lost request vs lost response, refused,
   timeout, never duplicated or reordered within a connection), asymmetric and region
   partitions, clog/release, per-link loss; disk written/durable layers, silent bit-rot,
   wipe, crash dropping unsynced data, torn tail, read error; crash/restart/pause.
   Name any fault that is claimed but not really injected, or injected wrongly.
4. Loop and stall semantics: `(time, seq)` total order, timers as events, `Step`,
   `RunUntil`, `RunUntilQuiet`, step budget, stall reporting (never a hang), panics in a node
   surfacing with the seed.
5. Tests: does each named test in the brief exist and test what its name says? Any test that
   cannot fail, asserts nothing, or duplicates another? Are the error sentinels each produced
   and checked with `errors.Is`? Does a failing DST test print seed, digests, log tail and a
   working `REPLAY:` command?
6. Rules: no comments; standard library only in product code; no goroutine/channel/mutex/
   wall clock/global rand in `internal/env` and `internal/sim`; panics only for violated
   invariants and each listed in `agent_work/gates/panic-allowlist.txt`.
7. Kernel separability (a reading check; findings here are NOTES, never blockers). The
   files that hold (i) PRNG stream derivation, (ii) the virtual clock and timers, (iii) the
   event queue and loop, (iv) the event log and digest, (v) seed plumbing in `simtest`:
   do they refer to network, disk, node, region or nemesis types? Does the loop treat event
   kind/node/peer as opaque integers it does not interpret? Would the loop work with the
   event log switched off or replaced? Is `simtest`'s seed handling in files separate from
   its cluster builders? List each tangle with file and identifier.

## Output: agent_work/0b-simulator/REVIEW-1.md
First line exactly one of `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`. Then numbered
findings, each with severity (BLOCKER / MAJOR / NIT / NOTE), file and line, what is wrong, a
concrete reproduction (a test snippet or command where possible) and the smallest fix. FAIL
needs at least one BLOCKER or MAJOR: a wrong or unimplementable interface, a determinism
leak, a fault that is not what it claims, a test that proves nothing for a headline claim.
An interface change you consider necessary before the freeze is at least MAJOR — say exactly
what signature you would change. Do not pad with style remarks. End with what you verified
by running versus by reading.
