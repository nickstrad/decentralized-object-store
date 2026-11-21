# Brief — Chunk 0b REPAIR (the one repair round): the deterministic simulator

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/0b-simulator
(branch `chunk/0b-simulator`). You are repairing work a previous builder committed; you did
not write it. FIRST read `agent_work/0b-simulator/objstore-p2-builder-rules.md` (binding: no
comments in Go, Docker only, tests first, commit in this worktree, never push), then the
original brief `agent_work/0b-simulator/objstore-p2-0b-simulator-build.md`, then
`agent_work/0b-simulator/build-notes.md` and the review
`agent_work/0b-simulator/REVIEW-1.md` (verdict FAIL: three MAJOR, three NIT, three NOTE).

The reviewer judged the exported API of `internal/env`, `internal/sim` and `simtest` sound
and ready to freeze. KEEP IT: this round changes behaviour and file layout, not exported
signatures. If a fix truly needs an exported change, make the smallest one and list it under
"API changes" in the build notes with the reason; regenerate `frozen-api.txt` either way.

## Must fix (each: a regression test written FIRST that fails on the current code, then the fix)
1. REVIEW finding 1 — request/response order within a connection under Pause + Clog
   (both paths: re-clogging an event that already arrived; `HealAll` release order; also
   manual `Unclog` and `Resume` at the same instant). Use the reviewer's reproduction as the
   test; add the subtests to `TestReleaseKeepsFIFOAtTheReleaseInstant` or next to it. Also add
   a rapid property `TestPropStreamOrderPerConnection`: random sequences of requests,
   pause/resume, clog/unclog, heal on a small cluster never deliver two requests (or two
   responses) of one ordered pair of nodes out of send order, and never deliver one twice.
2. REVIEW finding 2 — `Budget`: blobs with `GroupOf == ""` are not grouped; `FaultReadError`
   (per blob and whole-node) counts as damage for `MaxDamagedPerGroup` in both `Allows` and
   `Check`; `FaultBitRot` with an empty blob name is judged on the blobs it would really hit.
   Extend `TestBudgetGroupLimit` and `TestPropNemesisRespectsBudget` (the property must now
   include read errors and ungrouped blobs and assert the per-group bound after every event).
3. REVIEW finding 3 — disk damage bookkeeping: an unsynced `Write`/`Delete` must not clear
   the damage mark; `Sync` clears it only when the durable copy is really replaced or
   removed; crash-before-sync leaves the blob damaged AND marked. Add the crash-before-sync
   test and extend `TestPropDiskModel` so the reference model tracks "durably damaged" and
   `Damaged()` is compared with it after every step, including across crash and torn crash.
4. REVIEW finding 4 — `CrashTorn` can tear the FIRST unsynced `Append` of a new blob (a
   later chunk's saga log starts that way). Test over seeds that both outcomes occur.
5. REVIEW finding 5 — a panic inside a node shows its stack in the failure report.
6. REVIEW finding 6 — `seeds.txt` tolerates tabs; the log-tail header count matches the lines
   printed; no `panic(` in `clock_test.go` (use `t.Fatal`).
7. REVIEW finding 8 — the event log must hash the payload bytes as they crossed the wire, not
   after a handler may have mutated them: take the hash before the handler runs (or hand the
   handler its own copy). Add a test with a handler that mutates its input.
8. Gate hygiene found by the orchestrator's run of the landing gate on your tree:
   - `agent_work/gates/panic-allowlist.txt`: the second entry must name the enclosing
     function as `Sim.serve` (methods are written `Type.Method`), not `serve`.
   - `internal/sim/sim.go` imports `runtime/debug` for the panic stack. The determinism lint
     allows only a fixed list of standard packages in `internal/env` and `internal/sim`
     (bufio, bytes, cmp, container/*, crypto/sha256, crypto/sha512, crypto/subtle, embed,
     encoding/binary, encoding/hex, encoding/base64, errors, fmt, hash, hash/crc32,
     hash/crc64, hash/fnv, io, iter, maps, math, math/bits, math/rand/v2, slices, sort,
     strconv, strings, time, unicode, unicode/utf8, unicode/utf16, and this module). The
     orchestrator is adding ONE exemption for `runtime/debug` in `internal/sim/sim.go`, used
     only to capture the stack of a recovered panic for the report; keep that use confined to
     that file and that purpose, and make sure the stack never reaches the event log, a
     digest, a message or a decision. Do not add other imports outside the list.
   - The lint also rejects: any value that can print an address passed to a `fmt` function
     (a pointer, chan, func, uintptr, or a struct/slice/map containing one — pass the fields
     you mean instead), `fmt` functions used as values, a range over a map unless its body
     only appends keys/values to one slice and the very next statement is `slices.Sort(s)`,
     `sort.Strings(s)` or `sort.Ints(s)` (or use `slices.Sorted(maps.Keys(m))`), floats,
     build tags and platform-suffixed files in these packages. Your existing code passed all
     of these; keep it that way in what you add.

## File moves for later kernel extraction (no behaviour change, no exported change)
A later project will extract a small simulation kernel (PRNG streams, virtual clock and
timers, event queue and loop, event log and digest, seed plumbing) from this code. Do NOT
redesign for it and do NOT create new packages. Only where it is a pure move of existing
declarations between files of the same package, with tests and golden digests untouched:
- `internal/sim/simtest`: seed and sweep handling (`-seed`, `DST_SEED*`, pinned seeds,
  `Seeds`, `Sweep`) in its own file; determinism check and failure report in another; the
  cluster builders in a third.
- `internal/sim`: the generic stream derivation (`NewStream`, `fnv64a`) in a file with no
  domain names; the stream labels and `NodeStream` in another.
- `internal/sim`: the virtual clock and timer code in its own file, apart from the network
  and disk methods, as far as that is a pure move.
List in the build notes what moved and every remaining tangle you noticed (kernel code that
still refers to network, disk, node, region or nemesis types), as an "Extraction notes"
section. That list is input for a knowledge entry; it is not something to fix now.

## Golden digests
Findings 1, 4 and 7 can change schedules or log bytes. If `TestGoldenDigests` changes, update
`testdata/golden.txt` ON PURPOSE in the same commit as the cause and say why in that commit
message and in the build notes. `TestReplaySameProcess` / `AcrossProcesses` must stay green.

## Before you stop (all through Docker; record command and result in the build notes)
`gofmt -l .` empty; `go vet ./...`; `go build ./...`; `go test ./...`; `go test -race ./...`;
`FuzzSimSchedule` for 30 s; `go test ./internal/sim/... -rapid.checks=2000`;
`DST_SEEDS=500 go test ./...`; `go test -cover ./...` (floor 85% on `internal/sim`).
Update `agent_work/0b-simulator/build-notes.md` with a "Repair round" section: each finding,
the test that now proves it, what changed, measurements that moved; regenerate
`agent_work/0b-simulator/frozen-api.txt`. Everything committed, `git status` clean. Final
message: the commit list and a three-line summary. Do not claim anything you did not run.
