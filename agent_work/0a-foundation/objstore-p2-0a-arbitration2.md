# Brief — Chunk 0a ARBITRATION, confirm pass: are items A–D closed?

Seat: Astra (gpt-6-astra), arbiter (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0a-foundation (branch `chunk/0a-foundation`).
READ-ONLY: do not edit, commit, or run git write commands. The ONLY file you create is
`agent_work/0a-foundation/ARBITRATION-2.md`. Your verdict is final for this chunk.

## Where this stands
`agent_work/0a-foundation/ARBITRATION-1.md` (your seat, an earlier fresh run) ruled FAIL at
e0409b5 and set the landing condition: "Close A–D below and rerun their positive and
negative controls before landing." Everything else was ruled fixed or acceptably narrowed;
do not reopen it. Read ARBITRATION-1.md first — items A, B, C, D and the closing criteria
are defined there.

The builder's answer is commit `54b4811` (`git show 54b4811`):
- A — `agent_work/gates/gate.sh`: `prepare_baseline` requires the baseline to be a strict
  ancestor of HEAD; a build-stage run on main (HEAD its own baseline) exits 2 with no
  result; the gate side also exits 2 if the named baseline equals the gated commit; a
  build-stage result counts for landing only with a baseline named by a full commit SHA.
  Canaries `self-baseline` and `unnamed-baseline` added.
- B — called directly (inside the container or with GATE_NO_DOCKER) at the build stage
  without a baseline, the gate now works the baseline out from git itself with the same
  rule; where git cannot read the checkout from inside the container (a linked git
  worktree, whose metadata is not mounted) it exits 2 with guidance instead of reporting
  landed prose as a violation.
- C — `agent_work/gates/ci-prose.sh`: when the push boundary is empty, all zeros, unknown
  or not an ancestor, it checks the WHOLE first-parent history since a fixed bootstrap
  commit (a78cffc3…, the last commit before any tooling existed; overridable with
  `CI_PROSE_BOOTSTRAP` for tests) and exits 2 when that commit is not an ancestor of head.
  Runs of direct commits are checked as one range each.
- D — `agent_work/gates/detlint/detlint.go` `orderInsensitive`: any `=` range over a map
  that assigns a non-blank key or value is rejected before the empty-body exemption;
  fixture `testdata/mod/maprange/outervar/a.go` holds your exact shape plus value and
  field-assignment variants and the safe forms (`for range m`, `for _ = range m`).
- The builder's own controls: `agent_work/0a-foundation/arbitration-controls.sh` (run it:
  about three minutes; it builds a throwaway repo under /tmp with commented code already
  on main). Its last run printed eleven OK lines. The full canary suite was re-run after
  the fix (see `agent_work/0a-foundation/gates/canaries.json` if present, or run a filtered
  set: `agent_work/gates/canaries.sh '^(clean|landed-prose|comment|self-baseline|unnamed-baseline)$'`).
- Two prose commits by the prose seat sit on the branch as well (`README.md`,
  `docs/index.md`); they are not under arbitration, but the README now tells a reader to
  use `GATE_STAGE=final` on a checkout of main, because the build stage refuses to run
  there.

## What to decide
For each of A, B, C, D: CLOSED or NOT CLOSED, by your own controls — re-run the
reproductions from ARBITRATION-1.md (rebuild them under /tmp; the earlier fixtures may be
gone) plus the positive controls: a correct chunk branch on a commented main stays green
at the build stage from the host; a comment the branch adds is still red; a valid landing
merge and valid direct commits pass ci-prose; safe map loops still pass the lint. Then:
did `54b4811` introduce a new BLOCKER or MAJOR (a gate green on a violating tree, or red on
a correct one, in a supported invocation)? Judge only that. Everything runs through Docker
from the worktree root (`docker compose run --rm -T dev <cmd>`). Leave nothing behind
outside /tmp and your one output file.

## Output: agent_work/0a-foundation/ARBITRATION-2.md
First line exactly one of: `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`.
PASS: A–D are closed and the chunk may land. NITS: it may land once the listed small items
are closed (each with file and smallest fix). FAIL: something is NOT CLOSED or a new
BLOCKER/MAJOR exists — say exactly which and what closes it. Then one line of evidence per
item (what you ran), then what you verified by running versus by reading.
