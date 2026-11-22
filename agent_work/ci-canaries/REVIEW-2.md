VERDICT: NITS

Reviewed fix commit `8d224ed` (`HEAD~1`: *"gates, ci: make canary plants fail loudly; run the fast canaries on every push and the full set from its own workflow"* — note `61ab019` at `HEAD~2` recorded REVIEW-1). No new BLOCKER or MAJOR issues were introduced; no gate was weakened and no canary lost its ability to fail relative to the prior setup.

## REVIEW-1 findings status

| # | REVIEW-1 finding | Status | Evidence |
|---|---|---|---|
| 1 | Silent plant failures, unchecked `fresh`, missing `mkdir -p` in `plant_symlink`, loose `panic-shadow` pattern | **PARTLY FIXED** | `plant_symlink` (`canaries.sh:136`) adds `mkdir -p book/chapters`; `fresh` (`canaries.sh:32-37`) checks `mkdir` and both `PIPESTATUS` elements and exits 2 on failure (verified in `/tmp` copy); `plant_in` (`canaries.sh:43-52`) runs `( set -euo pipefail; cd "$work/$1"; "$2" )` from 4 call sites all outside `if`/`&&`/`||` (`canaries.sh:73, 297, 378, 387`) and turns broken plants into `MISSED` + `PLANT FAILED` + exit 1 (verified on `gofmt`, `panic-shadow`, `self-baseline`, `stage-typo`); only `panic-shadow`'s pattern (`canaries.sh:327`) was changed to `' panic: '` instead of `'panic-shadow'` (NIT 1). |
| 2 | `canaries.yml` omits `.github/scripts/**` and drops `pull_request` trigger | **FIXED** | `.github/workflows/canaries.yml:10-31` adds `'.github/scripts/**'` and `'.dockerignore'` to `paths:` and adds a `pull_request:` trigger with the same 8-path list, using plain valid GitHub Actions YAML (no anchors/aliases). |
| 3 | Keep cheap G0/G6 canaries in `ci.yml` via `CANARY_FAST=1` | **FIXED** | `canaries.sh:20` forwards `-e CANARY_FAST="${CANARY_FAST:-}"` into Docker; fast mode runs the exact 33 G0/G6 canaries (29 G0 `canary` entries + `partial-run`, `bad-image`, `self-baseline`, `stage-typo`), skips all 8 full-gate runs and all 9 G1–G5 canaries (verified 0 of 0 on all 17 skipped names and 3 of 3 on `tagged-only\|panic-shadow\|chapter-symlink`), and skips overwriting `canaries.json` (`canaries.sh:395`); `.github/workflows/ci.yml:33-36` runs it after `gate.sh ci` and `ci-prose.sh` so red trees fail at the gate first. |
| 4 | `tagged-only` no longer tests a build-tag-only simulator directory | **FIXED** | `plant_tagonly` (`canaries.sh:125`) deletes `internal/sim/*.go` before writing `//go:build missingtag` `zz_canary.go`, exercising `detlint_dirs()`'s `go list -e` path (`gate.sh:169`) on a directory with zero buildable files; inspecting `G0-detlint.log` confirms `tagged-only` matches `internal/sim/zz_canary.go:1:1: buildtag: build constraint: //go:build missingtag` (collateral failures from deleted `internal/sim/*.go` emit `typecheck`/`stale-allow`, never `buildtag`). |

## New and remaining findings

1. **NIT — `panic-shadow` checks `' panic: '` instead of `'panic-shadow'`, so breaking `panicDecls` still reports `CAUGHT`**
   - **File & lines:** `agent_work/gates/canaries.sh:130, 327`; `agent_work/gates/detlint/cmd/paniccheck/main.go:104-108, 122-123`
   - **Reproduction:** `plant_shadow` writes both `zz_shadow.go` (`func panic(any) {}`, which emits `panic-shadow:`) and `zz_canary.go` (`panic("unlisted")`, which emits `panic:`). Because `canaries.sh:327` matches `' panic: '` rather than `'panic-shadow'`, disabling the `panicDecls` loop in `agent_work/gates/detlint/cmd/paniccheck/main.go:104` (tested in a `/tmp` copy) still passes `agent_work/gates/canaries.sh '^panic-shadow$'` with `CAUGHT` and exit 0. (`plant_in` does now catch a failed write of `zz_shadow.go`, and `paniccheck/main_test.go:30-42` unit-tests `panic-shadow`, though `gate.sh` only runs `go build` on `detlint`, not `go test`.)
   - **Smallest fix:** On `agent_work/gates/canaries.sh:327`, change the log pattern from `' panic: '` to `'panic-shadow'`.

2. **NIT — `plant_coverage` loses its `n=8` fallback under `set -euo pipefail` if `internal/sim` ever has no `.go` files**
   - **File & lines:** `agent_work/gates/canaries.sh:44, 200-205`
   - **Reproduction:** No plant fails under `set -euo pipefail` on the current correct tree (verified across all 31 plant functions; `internal/sim` currently has 25 `.go` files / 6,887 lines). However, `n=$(cat internal/sim/*.go 2>/dev/null | wc -l)` previously relied on `wc -l` returning 0 when the glob failed to match so `[ "$n" -ge 8 ] || n=8` could kick in; under `set -o pipefail`, an empty `internal/sim` causes the pipeline to exit 1 and abort `plant_coverage` (verified in Docker: `EMPTY_SIM_EXIT=1`).
   - **Smallest fix:** Replace line 201 with `local n; n=$( (cat internal/sim/*.go 2>/dev/null || true) | wc -l )`.

3. **NIT — `canaries.sh` header slightly overstates `CANARY_FAST=1` scope and omits the `canaries.json` write guard**
   - **File & lines:** `agent_work/gates/canaries.sh:9-13, 395`
   - **Reproduction:** Lines 9–10 describe `CANARY_FAST=1` as *"the ones a push can break by changing the PRODUCT tree under the plants"*, whereas the 9 skipped G1–G5 canaries (notably `race` planting `var Shared int` in `internal/node` and the 8 `internal/sim` G1–G5 canaries) also depend on the product tree and wait for `canaries.yml`. Line 13 states *"Writes agent_work/0a-foundation/gates/canaries.json"*, though line 395 skips writing it when `CANARY_FAST=1` or a name regexp is supplied (which is the right behavior).
   - **Smallest fix:** Clarify lines 9–13 to *"the cheap G0/G6 product-tree canaries"* and *"A full unfiltered run writes `agent_work/0a-foundation/gates/canaries.json`."*

## Verification

- **Verified by running:**
  - `CANARY_FAST=1 agent_work/gates/canaries.sh '^(tagged-only|panic-shadow|chapter-symlink)$'` in the worktree (via Docker) — `3 of 3 caught`.
  - In a throwaway copy under `/tmp/opencode/canary-test` (via Docker, worktree untouched):
    - Broke `plant_gofmt` to write to nonexistent `internal/nonexistent/zz_canary.go` without `mkdir`: `agent_work/gates/canaries.sh '^gofmt$'` reported `MISSED`, printed `PLANT FAILED (plant_gofmt, exit 1): ... No such file or directory`, and exited `1`.
    - Broke `plant_comment` (removed `mkdir -p`) and first command of `plant_shadow`: `agent_work/gates/canaries.sh '^(self-baseline|stage-typo|panic-shadow)$'` reported `0 of 3 caught` (`MISSED` + `PLANT FAILED` on all three) and exited `1`.
    - Broke `fresh` (`tar -C /nonexistent-src`): `agent_work/gates/canaries.sh '^gofmt$'` stopped immediately with `canaries: copying the tree for gofmt failed` and exited `2`.
    - `CANARY_FAST=1 agent_work/gates/canaries.sh '^(clean|landed-prose|unit-fail|coverage|property|unnamed-property|fuzz|unicode-fuzz|race|dst-seeds|indirect-dst|dirty-tree|dirty-unknown|short-fuzz|bad-sha|outside-container|unnamed-baseline)$'` — `0 of 0 caught` (confirmed `CANARY_FAST=1` enters the container and skips all 17 slow/full-gate canaries).
    - Ran `gate.sh` (`GATE_ONLY=G0`) on a `plant_tagonly` copy and inspected `result.json` and `G0-detlint.log` — confirmed `internal/sim/zz_canary.go:1:1: buildtag: build constraint: //go:build missingtag` and `wallclock` are emitted while collateral failures emit only `typecheck`/`stale-allow`.
    - Ran `gate.sh` (`GATE_ONLY=G0`) on a `plant_shadow` copy and inspected `G0-panics.log` (both `panic:` and `panic-shadow:` lines present); then disabled `panicDecls` in `paniccheck/main.go` and ran `agent_work/gates/canaries.sh '^panic-shadow$'` — still reported `CAUGHT` (`1 of 1 caught`, exit `0`).
    - Tested `plant_coverage`'s `n=$(cat internal/sim/*.go 2>/dev/null | wc -l)` under `set -euo pipefail` in Docker on both the real `internal/sim` (`REAL_TREE_OK n=6887`) and an empty `internal/sim` (`EMPTY_SIM_EXIT=1`).
  - `python3 -c 'import yaml; ...'` parsed `.github/workflows/ci.yml` and `.github/workflows/canaries.yml` cleanly.
  - `git log --oneline -6`, `git show 8d224ed`, `git status --short`.
- **Verified by reading:**
  - `agent_work/gates/canaries.sh` (full, all 31 plant functions and all 50 canary definitions), `agent_work/gates/gate.sh` (full), `.github/workflows/canaries.yml`, `.github/workflows/ci.yml`, `compose.yaml`, `Dockerfile`, `agent_work/gates/detlint/{detlint.go,load.go,internal/report/report.go,cmd/paniccheck/{main.go,main_test.go}}`, `agent_work/ci-canaries/{REVIEW-1.md,objstore-p2-ci-canaries-review.md,objstore-p2-ci-canaries-review2.md}`.
  - Did not run the full 50-canary suite or full 33-canary fast suite (per brief).

NITS CLOSED (orchestrator, 2026-11-22): nit 1 (panic-shadow now matches the panic-shadow finding), nit 2 (coverage plant keeps its fallback under pipefail) and nit 3 (header wording) are fixed in the commit that follows this review; panic, panic-shadow and coverage canaries re-run: 3 of 3 caught.
