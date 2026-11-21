# Chunk 0b adversarial results (gate G7)

Harness: `run-all.sh` (seven drivers over `main.go`, a nested Go module, plus
`reporting.py`), written by Astra, run by the orchestrator. Attack list and exact
reproductions: `ATTACKS.md`. Raw output: `evidence/`.

| Run | Tree | Held | Broken |
|---|---|---|---|
| 1, after review, before the post-adversarial round | 110bd69 | 52 | 8 |
| 2, after that round (final) | 9e09fec | 58 | 2 |

## Per claim, run 2

| Claim | Held | Broken | Outcome |
|---|---|---|---|
| C1 replay is byte-identical for one seed | 3 | 0 | held |
| C2 the network injects what it claims, at the configured rate | 17 | 0 | held (1 repaired) |
| C3 partitions do not leak | 7 | 0 | held |
| C4 disk faults are what they claim; budget never under-reported | 8 | 0 | held (2 repaired) |
| C5 stream independence | 3 | 1 | narrowed (N1) |
| C6 the loop never hangs and never lies | 16 | 1 | held (2 repaired; 1 repaired but still scored broken, see R4) |
| C7 failure reporting | 4 | 0 | held (1 repaired) |

## The eight attacks that broke in run 1

- **R1 `read-count-overflow`, `whole-read-overflow-budget` (C4) — repaired.** Read-fault
  counts saturate instead of wrapping; a non-positive count is rejected. Both now HELD.
- **R2 `nemesis-callback-panic` (C6) — repaired.** Fault and nemesis events run under the
  same panic guard as node code; a panic in a caller's `GroupOf` fails the run with a
  `PanicError` and the seed. Now HELD.
- **R3 `huge-latency-range` (C2), `timer-duration-clamp` (C6) — repaired.** Virtual time
  is bounded by an exported `sim.Horizon` (2^62 ns); latency is drawn without overflow; a
  timer past the horizon never fires instead of firing early. Both now HELD.
- **R4 `clock-overflow` (C6) — repaired; the harness still prints BROKEN.** `AddNode` now
  rejects a skew outside ±`sim.MaxSkew` (2^61 ns) with `ErrInvalidArgument`, so a local
  clock can no longer wrap. The attack's driver wraps `AddNode` in a helper that treats any
  error as a broken claim, so its line reads `BROKEN ... skew ... invalid argument`. The
  behaviour the attack reported (local time going backwards with no error) no longer
  occurs. The harness is kept as Astra wrote it.
- **R5 `cli-failure-report` (C7) — repaired.** A failing `objsim` run prints the log tail,
  the panic stack and a `REPLAY:` line last. Now HELD.
- **N1 `network-nemesis-dynamic-blobs` (C5) — narrowed, stays BROKEN as worded.** Changing
  a network setting changed WHICH blob the nemesis rotted, because a disk fault picks its
  target from the blobs on the node at that instant. Named-stream independence holds; the
  claim was too broad. Narrowed claim: "The nemesis's schedule — when it acts, on which
  node, with which kind of fault — depends only on the seed and the nemesis configuration.
  Which blob a disk fault hits is chosen from the blobs on that node at that instant, so
  it depends on what the protocol has written." The nemesis now consumes the same number
  of draws per fault whatever the inventory; `TestPropNemesisTimingIgnoresInventory`
  proves the narrowed claim.

Also from that round's audit, not from an attack: the incarnation counter can no longer
wrap (`Restart` and `WipeRejoin` return `ErrInvalidArgument` at the last value).

HELD means the executed attack held, not a proof over all uses of the API. C1 has three
attack groups, each sweeping seeds and node counts (see `ATTACKS.md` for sizes).
