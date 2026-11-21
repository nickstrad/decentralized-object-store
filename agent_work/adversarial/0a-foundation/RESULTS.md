# Chunk 0a adversarial results (gate G7)

Harness: `run-all.sh` (seven scripts over `harness.py`), written by Astra, run by the
orchestrator. Attack inventory and reading notes: `ATTACKS.md`. Raw output: `evidence/`.

| Run | Tree | Held | Broken |
|---|---|---|---|
| 1, before the repair round | d6d6201 | 62 | 46 |
| 2, after the repair round | 9d754c7 | 99 | 9 |

Run 1 output is kept in `evidence/orchestrator-run-1-pre-repair.txt`; run 2 in
`evidence/run-all.txt` and `evidence/summary.txt`. Four attacks were added between the runs.

## Per claim, run 2

| Claim | Held | Broken | Outcome |
|---|---|---|---|
| C1 a hygiene violation cannot get a green G0 | 16 | 1 | narrowed (N1) |
| C2 detlint catches every entropy source in checked packages | 31 | 2 | narrowed (N1, N2) |
| C3 no dependency or comment gets past the checks | 15 | 1 | narrowed (N3) |
| C4 astdiff / prose-check pass only comment-only changes | 14 | 3 | narrowed (N4, N5) |
| C5 a result counts only from a clean full in-container run | 11 | 2 | narrowed (N6, N7) |
| C6 the site cannot publish a broken or private page | 12 | 0 | held |

## The nine broken attacks, each narrowed in writing

- **N1 — test files are outside the determinism lint** (`C1 test-only-entropy`,
  `C2 test-only-clock`). `detlint` checks the product files of the simulator and protocol
  packages; a `_test.go` file may read the wall clock (test timeouts, re-executing the test
  binary). Narrowed claim: *product code* of a checked package has no entropy source.
  Test-side nondeterminism that reaches a simulation is caught by the replay meta-tests and
  seed sweeps of gate G5, not by the lint. Stated in `gate.sh` above `detlint_dirs`.
- **N2 — the standalone lint checks only the directories it is given**
  (`C2 helper-subpackage`). Called by hand on one directory, `detlint` does not follow
  imports. The gate does: `detlint_dirs` passes the in-module import closure, and the gate
  canary `helper-package` proves a clock hidden in a helper package turns G0 red.
  Narrowed claim: the *gate* lints the closure; the bare tool lints its arguments.
- **N3 — `testdata/` is outside the comment rule** (`C3 testdata-comment`). Fixture trees are
  never compiled into a product package. Stated in `gate.sh` (`product_go_files`).
- **N4 — CLI help text is prose** (`C4 embed-payload`, `C4 embedded-help`). A prose commit
  may change `cmd/*/help.txt`, which changes what the binary prints. That is the prose rule
  as designed (OQ-8); `prose-check.sh` states it as a known limit and rejects any other
  embedded file in a prose commit.
- **N5 — a repair may delete a comment with its code** (`C4 post-comment-deletion`). After
  the prose stage a non-prose commit may not add or change a comment, but may remove one.
  Stated in `prose-check.sh` rule (c).
- **N6 — a usage error leaves old result files alone** (`C5 stale-usage-failure`). The gate
  called with no chunk name cannot know which result to clear. `land.sh` does not trust a
  result by its presence: it requires the result's `sha` to be the branch tip, apart from
  work-note files.
- **N7 — `FUZZ_ONLY` is a developer filter** (`C5 fuzz-filter`). `fuzz-short.sh` run by hand
  with a filter that matches nothing reports zero targets and exits 0. The gate unsets
  `FUZZ_ONLY` before G3, so a landing result always fuzzes every target.

Each narrowing was accepted as a scope decision by the arbiter (agent_work/0a-foundation/
ARBITRATION-1.md, table "Narrowing / Decision"). They bound what the gates claim; they are
not proof that every violating tree is rejected. The arbitration found four further defects
outside this harness (A–D: a self-certifying comment baseline, the direct-container entry
point, the first-push boundary in CI, an empty map loop assigning an outer variable); those
were repaired in 6e8fc66 and are covered by the gate canaries `self-baseline`,
`unnamed-baseline`, `landed-prose`, the detlint fixture `maprange/outervar`, and
`agent_work/0a-foundation/arbitration-controls.sh`.
