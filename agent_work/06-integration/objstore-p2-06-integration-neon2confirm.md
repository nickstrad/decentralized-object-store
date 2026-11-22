# Brief — Chunk 6 FOCUSED CONFIRM: the fixes for the second-eyes findings

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree made for this pass
(branch `review/06-integration-neon2eyes`, a copy of the chunk branch at its tip). No git
write commands, no commits. The ONLY file you create in the worktree is
`agent_work/06-integration/NEON-CONFIRM.md`; temporary files go under `/tmp/neon2c-*`.
The machine has 4 shared cores: use `GOMAXPROCS=2`, package-level tests only.

## What happened
`agent_work/06-integration/NEON-SECOND-EYES.md` (your seat, an earlier run; verdict FAIL)
found four MAJORS and two NITS in the loss-declaration fix. Read it first. The builder's
account of what was changed is section 15 of `agent_work/06-integration/build-notes.md`.
The changes are `git diff 8170771..HEAD -- internal` (three commits: the scenarios added
as failing tests, then the fix). The scenarios from the earlier run now live in
`internal/node/secondeyes_*_test.go` (names without the `Neon2` prefix, comments removed).

## What to decide — this pass is narrow; do not re-review the whole chunk
For each of the six findings: is it closed? Specifically:
1. Finding 1 (false absence on a local disk read error). Read `stateAbsent`, `scrubShard`
   and `rebuild` in `internal/node/inventory.go`. The builder hardened `stateAbsent` only
   and deliberately kept the scrub dropping an unreadable shard from the inventory (so a
   rebuild is still tried and can rewrite the shard). Is there still ANY path that writes
   an absence record while the holder's own shard is intact on disk — read errors at any
   point (scrub, the rebuild's reads, `stateAbsent`'s read), in any order, repeated,
   on one or several holders, with a rotted peer? Devise at least three new schedules of
   your own beyond the two existing subtests and run them. Does the kept scrub behaviour
   cause any harm (a good shard overwritten with wrong bytes, a rebuild loop)?
2. Finding 2 (a finishing rebuild ignoring its holder's absence record). Is the re-check in
   the rebuild's completion sufficient on every path that stores a shard on a holder that
   has its own record in metadata? Try at least two new orders (record arrives before the
   first reply, between replies, with a crash and restart in the middle).
3. Finding 3 (oracle on migrated versions). Is the oracle fix sound — can it now pass a
   declaration it should fail (for example by reading a manifest of a different version,
   or by finding none and staying silent)? It must still fail a version it cannot find.
4. Finding 4: does `TestServeStorageRefusesShardWriteBeforeMetadataLoads` fail when the
   refusal is removed? Check with a temporary copy under `/tmp`.
5. Findings 5 and 6 (nits): closed?
6. Did these changes break anything: run `go test -count=1 ./internal/node/...`, the same
   with `-race` for the `secondeyes` and loss tests, `-run Prop -rapid.checks=2000`, and
   `DST_SEEDS=50 -run 'DST$'`. Is section 15's "Residual added" (a holder whose own disk
   keeps failing reads blocks the declaration) exactly what the code does, and acceptable?

Run through Docker from the worktree root, for example
`docker compose run --rm -T -e GOMAXPROCS=2 dev go test -count=1 ./internal/node -run 'X' -v`.
For your own new scenarios, export the tree (`git archive HEAD | tar -x -C /tmp/neon2c-src`),
add test files THERE, and run with
`docker run --rm -e GOMAXPROCS=2 -v /tmp/neon2c-src:/src -w /src objstore-dev:local go test ...`.
Paste the full source of any scenario that fails into your output file.

## Output: agent_work/06-integration/NEON-CONFIRM.md
Write it EARLY (placeholder first line, fill as you go). First line exactly one of
`VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`. Then one paragraph per numbered
question with what you ran or read, then any new finding graded BLOCKER / MAJOR / NIT with
file, line, reproduction and smallest fix, then the exact commands run. FAIL needs a
BLOCKER or MAJOR. No style remarks.
