# Builder rules — decentralized-object-store, Phase 2 (apply to every Opus build or repair run)

You are a fresh Opus builder for ONE chunk. Your chunk brief names this file; both bind you.

## Where you work
- Only in the git worktree that is your current directory, on its `chunk/...` branch. Never
  touch /root/Software/decentralized-object-store (the main checkout) or any other worktree.
- Commit in your worktree as you go (small logical commits, subject `<area>: <imperative>`,
  body saying what the tests prove). Never push, never merge, never rebase onto anything.
- Never edit: PLAN.md, STATE.md, README.md, agent_work/INDEX.md, docs/, book/, kb/, .github/,
  Dockerfile, compose.yaml, agent_work/gates/ (except appending to
  agent_work/gates/panic-allowlist.txt), agent_work/orch/. If one of them blocks you, write the
  need in your build notes under "Requests to the orchestrator" and continue with the rest.
- Read PLAN.md first: section 2 (testing architecture) governs; then your chunk in section 4.2
  and the technical decisions in section 7 that your chunk cites. Section 9.1 holds rulings that
  override older wording.

## Docker is the environment
- Build and test ONLY through Docker, from your worktree:
  `docker compose run --rm dev go test ./...` (same for `go build`, `go vet`, `gofmt -l .`,
  `go test -race`, `go test -fuzz`). Do not rely on the host's Go. The image has no access to
  git metadata of a worktree; do not call git inside the container.
- The machine has 4 cores and 7 GB of RAM and other builders share it. Keep test runs sane:
  no test may need more than about 1 GB; a full `go test ./...` of your packages should finish
  in under about 3 minutes with default seed counts, and the 500-seed sweep
  (`DST_SEEDS=500`) in under about 10 minutes per package.

## Tests first
- Write the named tests in your brief BEFORE the code they test; commit the tests first where
  practical (a failing-tests commit followed by the implementation is the expected history).
- Test kinds and their rules are in PLAN.md 2.2–2.6. Property tests use `pgregory.net/rapid`
  (imported from `_test.go` files only, never from product code). Each headline property is
  also exposed as a native fuzz target with `f.Fuzz(rapid.MakeFuzz(prop))` where the brief
  names a fuzz target for it. Fuzz targets add boundary seeds with `f.Add`.
- DST scenarios go through `simtest.RequireDeterministic` and must print the REPLAY command on
  failure. No sleeps, no wall clock, no real sockets or files in protocol packages.
- Each chunk plants the canary bug(s) its brief names: a test-only switch that enables the
  bug, and a test proving the suite FAILS with it on (the canary test passes when the
  inner check fails).
- Coverage floor: 85% statement coverage on your primitive package(s); name any uncovered
  branch and the reason in the build notes.

## Code rules
- Go standard library only in product code. No new `require` in go.mod.
- NO COMMENTS in any `.go` file you write, other than machine directives (`//go:build`,
  `//go:embed`, `//go:generate`, `// Output:` in examples, and the `// Code generated` line if
  ever needed). No doc comments, no inline notes, no TODOs. Another seat writes every comment
  later. Explain intent in your build notes instead.
- You own identifiers, error strings, log messages and test names. You do not write reader-facing
  prose: no README/doc/book edits. CLI help text is not yours either: a command's help and flag
  descriptions are loaded from an embedded text file (`help.txt`, `//go:embed`) next to main.go;
  you create it containing only the bare flag names, one per line, and the plumbing that prints it.
- Determinism (protocol packages, internal/env, internal/sim): no `time.Now`/`time.Since`/
  `time.Sleep`/`time.After`, no global `math/rand` functions, no `crypto/rand`, no `go`
  statement, no channel, no `select`, no mutex, no `sync/atomic`, no `os.Getpid`/hostname/env
  reads, no `%p`, no `maphash`, no float arithmetic in a decision (use integers, e.g.
  parts-per-million), and no ranging over a map where the order can reach an output, a message,
  a disk write, a random draw or a digest (collect keys and sort). A static check (detlint)
  and the cross-process replay tests enforce this.
- Errors: PLAN.md 2.6. Sentinels and typed errors, `%w` wrapping with operation and object,
  `errors.Join` for independent failures, callers use `errors.Is/As`. No error is dropped (an
  intentional ignore is written `_ =` and listed in the build notes).
- No panics in library packages except for a violated internal invariant. Every `panic(`
  under `internal/` needs a line in agent_work/gates/panic-allowlist.txt:
  `relative/file.go<TAB>EnclosingFunc<TAB>justification`.
- After Chunk 0b has landed, `internal/env` and the exported API of `internal/sim` and
  `internal/sim/simtest` are FROZEN. If you need a change there, do not make it: write a
  "simulator change request" in your build notes, stop on that point, finish everything else.

## What you hand back
- `agent_work/<chunk>/build-notes.md` (operational, plain): what you built, the design choices
  and why, every claim your tests prove and which test proves it, measured numbers the brief
  asks for, uncovered branches, ignored errors, panics added, anything you could not do, and
  "Requests to the orchestrator". This is the source material for the reviewer and for the
  prose writer, so be exact and complete; do not pad.
- Before you stop, run and record in the build notes (command and result):
  `gofmt -l .` (empty), `go vet ./...`, `go build ./...`, `go test ./...`,
  `go test -race ./...`, each fuzz target for 30 s
  (`go test ./<pkg> -run '^$' -fuzz '^<Target>$' -fuzztime 30s -fuzzminimizetime 5s`),
  the rapid properties at `-rapid.checks=2000` per package, `DST_SEEDS=500 go test ./<pkg>`
  for each DST package, and coverage per package (`go test -cover ./...`). All through Docker.
  If `agent_work/gates/gate.sh` exists in your worktree, also run
  `agent_work/gates/gate.sh <chunk>` and record its result.
- Everything committed; `git status` clean. Your final message: the commit list and a
  three-line summary. Do not claim anything you did not run.
