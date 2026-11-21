# Brief — Chunk 0a ADVERSARIAL: break the gates

Seat: Astra (gpt-6-astra), adversarial tester (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0a-foundation (branch `chunk/0a-foundation`).
You write ONLY under `agent_work/adversarial/0a-foundation/`. Do not edit anything else in
the worktree, do not commit, do not run git write commands. Make throwaway copies of the tree
under /tmp for your attacks (the existing `agent_work/gates/canaries.sh` shows one way: copy
the tree without `.git` and `kb`, plant something, run the gate in the copy inside the
container with `GATE_OUT` pointing elsewhere).

## Target
The landing-gate tooling of a Go project that is tested by deterministic simulation. Read
PLAN.md sections 2.1 (determinism rules), 2.5, 2.9 (gates G0–G6), 2.10 (prose rule) and 4.2
"Chunk 0" (0a's adversarial list). Then read `agent_work/gates/gate.sh`, `fuzz-short.sh`,
`canaries.sh`, `prose-check.sh`, `site_lint.py`, and the nested Go module
`agent_work/gates/detlint/` (detlint, nocomments, astdiff, paniccheck, importcheck; usage in
`README-tools.txt`). Everything runs through Docker: `docker compose run --rm -T dev <cmd>`
from the worktree root (repo at /src, Go 1.26.11). The host Go is not the environment.

## The claims to attack (each is a claim the project will publish)
C1. A tree that violates a hygiene rule cannot get a green G0: unformatted Go, a vet failure,
    a build failure, a banned construct in a simulator/protocol package, an unlisted panic
    under internal/, a dependency beyond test-only `pgregory.net/rapid`, rapid reachable from
    product code, a comment in pre-prose Go, a machine path in reader-facing files.
C2. `detlint` catches every entropy or concurrency source in the checked packages
    (internal/env, internal/sim minus simtest, internal/{gossip,erasure,merkle,occ,saga}).
    Hunt for misses: aliased and dot imports, method values, function values stored in
    structs or maps, generics, embedded types, `reflect`, `unsafe`, `maphash`, `%p` (and
    `%v` of a pointer or of a map — say what you find), `select` order, `os.Getpid`, map
    range order reaching output through helpers or iterators (`maps.Keys`, `range` over
    func), `sort.Slice` instability, `sync.Map`, goroutines via `time.AfterFunc` or
    `context`, `runtime` calls, address-dependent ordering (`uintptr(unsafe.Pointer)`),
    floats via named types or type parameters, `init()` order, `go:linkname`, assembly or
    cgo files, a helper sub-package the gate's directory list does not include, a banned
    call moved into a `_test.go`-only or build-tagged file, a file `go list` ignores.
C3. A dependency or a comment cannot be smuggled past the checks: `replace`, `go.work`,
    `vendor/`, `tool` directives, build tags, `_`/`.`-prefixed directories, `testdata`,
    nested modules outside agent_work/, `//go:` look-alike comments, `/* */`, comments in
    files with unusual names, cgo preambles, generated-code headers, a `.go` file that is
    not valid Go.
C4. `astdiff` passes only when two trees differ in comments alone: try to change behaviour
    in a way it does not see (directives, build constraints, string literals, struct tags,
    embedded files, non-`.go` files that affect the build such as `go.mod`, `.s`, embedded
    `help.txt`), and `prose-check.sh` proves (a)(b)(c) of PLAN 2.10 over a commit range
    (build small throwaway git repositories under /tmp to attack it).
C5. A gate result that did not come from a clean, full run inside the dev container at a
    known commit never has `counts_for_landing: true`. Try to forge or confuse it: environment
    variables, `GATE_ONLY`, `GATE_OUT`, a dirty tree, a stale result file left from an earlier
    run, a gate that crashes half-way, checks that pass vacuously (no packages, no tests
    matched by `-run 'Laws|Prop'`, a fuzz target the name scan misses, a test package the
    DST package list misses, a coverage line the parser misreads).
C6. `site_lint.py` and `build_site.py`: a chapter can not be published broken (missing file,
    unlisted file, bad link, machine reference), and nothing under docs/knowledge/,
    agent_work/, kb/ or STATE.md can be published (path tricks in the manifest, symlinks,
    links, assets).

## What to deliver, under agent_work/adversarial/0a-foundation/
- One script per attack or per tight group, bash (or Go in its own nested module if you need
  it — outside libraries are allowed only inside such a nested module). Name them
  `aNN-<slug>.sh`. Each script must be runnable by someone else with no arguments from the
  worktree root, must clean up after itself, and must end by printing exactly one line per
  attack: `HELD <claim> <attack-name>: <one sentence>` or
  `BROKEN <claim> <attack-name>: <one sentence with the smallest reproduction>`.
  Exit 0 when everything in the script HELD, 1 when anything is BROKEN, 2 on a setup error.
  A script must decide HELD/BROKEN from what the tool actually did, never from what you expect.
- `run-all.sh`: runs every script, collects the lines, prints a summary count.
- `ATTACKS.md`: the list of attacks by claim, one line each, with what you tried and what
  happened when YOU ran it, plus anything you could not test and why.
Aim for breadth first (every claim attacked several ways), then depth on whatever cracks.
Keep each gate run cheap: `GATE_ONLY=G0` where G0 is the target. Total runtime of
`run-all.sh` under about 20 minutes. Run it yourself before you stop and report real output.
