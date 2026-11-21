# Chunk 0a — foundation tooling: what the gates check and where they stop

## Findings

- The landing gate is one script, `agent_work/gates/gate.sh <name>`. Run on the host it
  builds the dev image and re-runs itself in the container; it writes
  `agent_work/<name>/gates/result.json`. A result counts for landing only for a full,
  green, in-container run on a clean tree at a real commit with a 30 s fuzz budget.
- Two stages. `build` (default) is for a chunk branch before its prose: product Go may have
  no comment beyond those in the commit the branch started from (the merge base with main,
  or `GATE_BASE`). `final` skips that comparison; comment provenance is then
  `agent_work/gates/prose-check.sh <base> <head>` (a chunk) or `ci-prose.sh` (a push).
- `agent_work/gates/canaries.sh` plants one violation at a time (50 cases) and requires the
  owning check to go red with the expected message. Run it after any change to a gate.
- The determinism lint (`detlint`) allows a fixed list of standard packages in simulator
  and protocol packages and lints their in-module import closure. Anything outside the
  list needs a line in `agent_work/gates/detlint-allow.txt`; unused lines fail the gate.

## Narrowed claims (accepted in arbitration)

- The lint covers product files, not `_test.go`. Test-side nondeterminism is the replay
  meta-tests' job (gate G5).
- `testdata/` trees and CLI help files (`cmd/*/help.txt`) are outside the comment rule and
  the comment-only-diff rule respectively.
- A repair after the prose stage may delete a comment with its code; it may not add one.
- A gate result is evidence for an honest operator. `agent_work/orch/land.sh` binds it to
  the branch tip; the file alone proves nothing.

## Pitfalls that cost time

- A build-stage gate run on main is refused (exit 2): a commit compared with itself would
  certify its own comments. On main use `GATE_STAGE=final`.
- Inside the container, git cannot read a linked worktree (its metadata lives outside the
  mount). The gate needs git only for the comment baseline, which the host side supplies;
  calling `gate.sh` directly in the container from a worktree stops with a setup error.
- A check that pipes `go list` into `grep` passes when `go list` fails. Every gate captures
  the listing first and fails closed. The same mistake appeared in the landing script
  (`git diff | grep || true`) one review later.
- A rule that says "no comments anywhere" is right for the first chunk and wrong for every
  later one. Any whole-tree rule needs a baseline once something has landed.
- CI's `github.event.before` is all zeros on a first push and unrelated after a forced
  push. `ci-prose.sh` then checks everything since a fixed bootstrap commit, or fails.
- The reviewer seat (run through opencode) stops early with exit 0 at its default
  reasoning setting on large reviews; the launcher asks for a final marker line and treats
  its absence as a failure.
- The site is manifest-driven: a chapter is published by adding
  `book/chapters/NN-slug.md` and one entry in `book/manifest.json`. The manifest is not
  prose: the chapter is committed by the prose seat, the manifest entry separately.
