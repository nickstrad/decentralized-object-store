# Brief — Chunk 0b, post-adversarial repair round: the deterministic simulator

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/0b-simulator
(branch `chunk/0b-simulator`, now rebased on main: the gate tooling under `agent_work/gates/`
is new to this branch). You are repairing work other builders committed. FIRST read
`agent_work/0b-simulator/objstore-p2-builder-rules.md` (binding: no comments in Go, Docker
only, tests first, commit in this worktree, never push), then
`agent_work/0b-simulator/build-notes.md` (contract, claims, "Repair round"),
`agent_work/0b-simulator/REVIEW-2.md` (verdict NITS: finding 1 is yours to close) and
`agent_work/adversarial/0b-simulator/ATTACKS.md` (eight BROKEN attacks with exact
reproductions; harness in `main.go`, run with
`bash agent_work/adversarial/0b-simulator/run-all.sh`). Do NOT edit anything under
`agent_work/adversarial/` or `agent_work/gates/` except the two allowlist files if a fix
truly needs it (say why in the notes).

The exported API of `internal/env`, `internal/sim`, `simtest` is about to be FROZEN. Keep
signatures. Where a fix needs an exported addition (a constant, a sentinel), make the
smallest one, list it under "API changes" in the build notes with the reason, and
regenerate `agent_work/0b-simulator/frozen-api.txt`.

## Items (each: a regression test written FIRST that fails on the current code, then the fix)
1. Attacks `read-count-overflow` and `whole-read-overflow-budget` (C4): `FailReads` counts
   overflow when added past `math.MaxInt`, silently disabling the fault and hiding a
   damaged disk from `Budget.Check`. Saturate or reject (`env.ErrInvalidArgument`); a
   non-positive count is rejected too if it is not already. Same audit for every other
   fault counter or accumulator in `disk.go`, `net.go`/`sim.go`, `nemesis.go`.
2. Attack `nemesis-callback-panic` (C6): a panic inside a caller-supplied callback reached
   from a fault event (`Budget.GroupOf`, and any other user callback the nemesis or
   `Apply`/`Allows`/`Check` calls) escapes `RunUntilQuiet` with `s.Err()` nil. Route it
   through the same guard as node/action/invariant panics: the run fails with a
   `PanicError` carrying the seed. Audit every user-supplied function the simulator calls.
3. Attack `huge-latency-range` (C2): `Latency=Range{0, math.MaxInt64}` is accepted at
   configuration and then panics in `Int64N` at delivery. Validate ranges where they are
   configured (`New`, and any setter): a range whose width overflows or that would push
   delivery time past the representable horizon is `env.ErrInvalidArgument` at
   configuration — or is drawn without overflow. Same audit for every `Range`-typed field
   and the nemesis gap/phase durations.
4. Attack `clock-overflow` (C6): `Skew=math.MaxInt64` makes a node's local clock wrap
   negative. Bound skew at `AddNode`/`AddCluster` validation (there is already
   `MaxDriftPPM`; add the analogous bound) so local time can never overflow within the
   simulator's time horizon; state the horizon.
5. Attack `timer-duration-clamp` (C6): `AfterFunc(1<<62 + 1)` fires one nanosecond early
   because `toGlobal` clamps silently. A timer (or request timeout) beyond the horizon
   must never fire early: make it never fire within the horizon (document that in the
   contract section of the build notes) or reject it where the API allows an error.
6. Attack `cli-failure-report` (C7): a failing `objsim -scenario X -seed N` prints seed and
   digests but no log tail and no `REPLAY:` line. Print both, the REPLAY line last, in the
   same shape `simtest` uses, with a command that reproduces the run as printed. Test it
   with a failing scenario registered from the test.
7. REVIEW-2 finding 1 (NIT): the nemesis's whole-node `FaultReadError` (`Blob == ""`)
   ambushes blobs written later, so `Budget.Check` can fail although every nemesis action
   passed `Budget.Allows`. Apply the reviewer's fix: when `Budget.GroupOf != nil`, the
   nemesis targets a specific existing blob (as bit-rot does) and skips when there is none;
   with `GroupOf == nil` behaviour and golden digests are unchanged. Extend
   `TestPropNemesisRespectsBudget` (or add a property) so blobs of NEW groups are written
   during chaos and the budget still holds after every event.
8. Attack `network-nemesis-dynamic-blobs` (C5): changing a network setting changed WHICH
   blob the nemesis rotted, because the target is drawn from the node's blob inventory at
   injection time and the network changed that inventory. Named-stream independence holds;
   the published claim was too broad. Do this: (a) make sure the nemesis consumes the SAME
   number of draws from its stream per fault whatever the inventory holds (including an
   empty inventory and item 7's skip), so that only the target blob can differ; (b) add
   `TestPropNemesisTimingIgnoresInventory`: two runs of one seed that differ only in what
   the protocol wrote to disk have identical nemesis histories in time, kind and node for
   every step — only blob names may differ; (c) write the narrowed claim in the build
   notes, exactly: "The nemesis's schedule — when it acts, on which node, with which kind
   of fault — depends only on the seed and the nemesis configuration. Which blob a disk
   fault hits is chosen from the blobs on that node at that instant, so it depends on what
   the protocol has written." If (a) changes golden digests, say so and update them on
   purpose in that commit.

## Golden digests and replay
`TestGoldenDigests` must stay unchanged unless an item above forces it (say which and
why, update `testdata/golden.txt` in the same commit). `TestReplaySameProcess` and
`TestReplayAcrossProcesses` stay green.

## Before you stop
1. `bash agent_work/adversarial/0b-simulator/run-all.sh` — record the SUMMARY line and
   every remaining BROKEN line in the build notes. Expected: the eight fixed, except
   `network-nemesis-dynamic-blobs`, which stays BROKEN as worded and is narrowed (item 8).
2. From the worktree root on the host: `agent_work/gates/gate.sh 0b-simulator` (it runs
   G0–G6 in the dev container: hygiene incl. the determinism lint and panic allowlist,
   unit + 85% coverage floor, rapid at 2,000 checks, 30 s fuzz per target, race, 500-seed
   sweep). It must end `overall=pass` with a clean `git status` (commit first; the result
   files under `agent_work/0b-simulator/gates/` may stay uncommitted). The lint rules are
   listed in the previous repair brief `agent_work/0b-simulator/objstore-p2-0b-repair.md`,
   section 8; `agent_work/gates/detlint/README-tools.txt` has the detail.
3. Add a "Post-adversarial round" section to `agent_work/0b-simulator/build-notes.md`:
   each item, the test that proves it, what changed, API changes, the narrowed claim,
   anything you chose not to do and why. Regenerate `frozen-api.txt`.
Everything committed, `git status` clean apart from gate result files. Final message: the
commit list and a three-line summary. Do not claim anything you did not run.
