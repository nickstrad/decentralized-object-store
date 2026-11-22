VERDICT: FAIL

## Findings

1. **MAJOR (Q1 — Change A): Plants in `canaries.sh` can still fail silently (producing vacuous passes on `landed-prose`, `self-baseline`, `stage-typo`, and `panic-shadow`), and `plant_symlink` still omits `mkdir -p`**
   - **File & lines:** `agent_work/gates/canaries.sh:10, 50, 70-260, 269, 299, 350, 359`
   - **Reasoning / reproduction:**
     - Commit `242fad4` added `mkdir -p internal/buildcheck;` to all 12 `internal/buildcheck` plant functions, and every other subdirectory-writing plant already runs `mkdir -p` **except** `plant_symlink` (`agent_work/gates/canaries.sh:108`: `ln -s ../../STATE.md book/chapters/zz-private.md`, which assumes `book/chapters/` exists).
     - More importantly, `canaries.sh` runs with `set -u` only (line 10), never checks the exit status of `fresh` or `$plant` (lines 50, 269, 350, 359), and chains commands inside multi-statement plants (`plant_gofmt`, `plant_asm`, `plant_shadow`, `plant_helper`, `plant_tagclock`, `plant_nestedmod`, `plant_indirect`, `plant_coverage`, `plant_race`, `base_pkg`) with `;` or newlines without `set -e`. If an earlier command fails, the plant still returns 0 when its last command succeeds; if the plant returns non-zero, `canaries.sh` ignores it and scores the gate anyway.
     - Concrete silent-pass cases:
       - `landed-prose` (line 269), `self-baseline` (line 350), and `stage-typo` (line 359) all call `plant_comment` without checking its status. Whenever `plant_comment` fails to write (as happened before `242fad4` when `internal/buildcheck/` was missing), `landed-prose` runs on an unmutated tree and passes all gates (`CAUGHT` vacuously), while `self-baseline` and `stage-typo` still exit 2 at `gate.sh:113` and `gate.sh:103` before `g0_comments` runs (`CAUGHT` vacuously).
       - `plant_shadow` (line 102) writes `zz_shadow.go` then `zz_canary.go`, and `canary panic-shadow` (line 299) only checks for `'panic'` in `G0-panics.log`. If the first write fails (or `panicDecls` breaks), `zz_canary.go`'s unallowlisted `panic("unlisted")` still matches `'panic'`, marking `panic-shadow` as `CAUGHT` for the wrong reason.
       - In `canary()` (line 50), any other plant failure still runs the full requested gate on a clean copy, wasting time and printing the green gate log tail instead of the plant error.
   - **Smallest fix:**
     - Add `mkdir -p book/chapters` to `plant_symlink` (line 108).
     - Add a helper (e.g. `plant_in <name> <fn>`) that runs `( set -euo pipefail; cd "$work/$1" && "$2" )` and fails loudly on non-zero exit (recording the canary as `false` with the plant's error and skipping `run_gate`, or aborting with exit 2); check `fresh` exit status too.
     - Tighten line 299's log pattern from `'panic'` to `'panic-shadow'`.

2. **MAJOR (Q2, Q3 — Change B): `canaries.yml` omits `.github/scripts/**` from `paths:` and drops the `pull_request` trigger, making CI green on gate-breaking PRs and site-builder changes**
   - **File & lines:** `.github/workflows/canaries.yml:9-18`, `.github/workflows/ci.yml:3-7`, `agent_work/gates/site_lint.py:41, 358`
   - **Reasoning / reproduction:**
     - `gate.sh:219-220` runs `agent_work/gates/site_lint.py` (both tree lint and `--selftest`), which executes `.github/scripts/build_site.py` (`site_lint.py:41, 358`). Editing `.github/scripts/build_site.py` directly changes `G0/selfcontained` (tested by `broken-link`, `doc-path`, `chapter-symlink`, `clean`), but `.github/scripts/**` is missing from `canaries.yml:12-18`. (Omitting `agent_work/orch/land.sh` is correct because `canaries.sh` does not invoke it; `agent_work/gates/panic-allowlist.txt` and `detlint-allow.txt` are already covered by `agent_work/gates/**`, which is right because edits to them change what `G0/panics` and `G0/detlint` accept; `.dockerignore` can also be added alongside `Dockerfile` and `compose.yaml`.)
     - Unlike `ci.yml:6`, `canaries.yml:9-21` has no `pull_request:` trigger. Under the old `ci.yml`, a PR touching `agent_work/gates/**` ran `canaries.sh` and failed `CI` if a gate was broken. Under Change B, a PR that breaks a check in `gate.sh` or `detlint` gets a green `CI` check and never triggers `canaries.yml` until after merge to `main`.
   - **Smallest fix:** Add `'.github/scripts/**'` (and `'.dockerignore'`) to `paths:` in `.github/workflows/canaries.yml`, and add a `pull_request:` trigger with the same `paths:` filter.

3. **NIT (Q2, Q3 — Change B): Dropping all canaries from `ci.yml` leaves product-tree-dependent canaries blind for up to a week; keep the cheap G0/G6 subset in `ci.yml`**
   - **File & lines:** `.github/workflows/ci.yml:25-33`, `.github/workflows/canaries.yml:9-21`, `agent_work/gates/canaries.sh:70-260, 279-364`
   - **Reasoning:**
     - Canaries whose outcome depends on the **product tree** (not the tester) and can rot unseen between tooling changes:
       - `race` (`canaries.sh:218-243, 314`): plants `package node` with `var Shared int` and `TestZZRace` into `internal/node/`, which does not exist yet on `main`. When the node chunk lands, a symbol collision with `Shared` or a different package name makes `G4/race` fail to compile instead of reporting `DATA RACE` (`MISSED`), unseen until Sunday unless that landing touches `agent_work/gates/**`.
       - `tagged-only` (`canaries.sh:97, 289`): already rotted once `internal/sim` became populated (Finding 4).
       - The 18 `internal/sim` canaries (`base_pkg`, `plant_timenow`..`plant_sortties`, `plant_coverage`, `plant_unitfail`, `plant_prop`, `plant_unnamed`, `plant_fuzz`, `plant_unifuzz`, `plant_dst`, `plant_indirect`): depend on `internal/sim` remaining `package sim`, not colliding with `Double`, `TestDouble`, `Untested`, `zz*`, `TestZZ*`, `FuzzZZCanary`, `FuzzÉcanary`, and on `cat internal/sim/*.go | wc -l` remaining large enough to pull `internal/sim` below 85% coverage.
       - `chapter-symlink` (`canaries.sh:108`, assumes `book/chapters/` exists), `broken-link` (`canaries.sh:107`, overwrites `docs/index.md` and assumes `docs/nowhere.md` does not exist), `doc-path` (`canaries.sh:87`, appends to `README.md`), `nested-module` (`canaries.sh:101`, assumes no regular file `extras`), and the 12 `internal/buildcheck` canaries (if a future chunk ever creates `internal/buildcheck`).
       - The 8 full-gate canaries (`clean`, `landed-prose`, `dirty-tree`, `dirty-unknown`, `short-fuzz`, `bad-sha`, `outside-container`, `unnamed-baseline`): depend on the product tree staying green under `GATE_STAGE=build` (whereas `ci.yml` runs `GATE_STAGE=final`, skipping `g0_comments`) and on total runtime staying inside `timeout-minutes: 350`.
     - Moving the expensive G1–G5 and 8 full-gate `counts` canaries to `canaries.yml` (tooling changes + weekly + on-demand) is the right tradeoff. However, the 33 G0/G6-only canaries (`gofmt`..`chapter-symlink`, `partial-run`, `bad-image`, `self-baseline`, `stage-typo`) take only ~1–2 minutes total and immediately catch the exact `internal/buildcheck`-style rot on every push and PR.
   - **Smallest fix:** Keep a fast G0/G6 canary step in `ci.yml` (via `canaries.sh`'s regexp argument or a `--fast` mode selecting only G0/G6 canaries), while running the full 50-canary set in `canaries.yml`.

4. **NIT (Q2 — pre-existing product-tree rot): `tagged-only` no longer tests a build-tag-only simulator package**
   - **File & lines:** `agent_work/gates/canaries.sh:97, 289`; `agent_work/gates/gate.sh:165-169`
   - **Reasoning:** `plant_tagonly` writes `//go:build missingtag` into `internal/sim/zz_canary.go` to verify `detlint_dirs()` (`gate.sh:169`, `go list -e`) catches a simulator directory whose *only* file is behind a build tag. Now that `internal/sim/` contains 25 real buildable `.go` files, `tagged-only` is identical to `tagged-clock` (`canaries.sh:96`) and no longer exercises the no-buildable-files path, yet still reports `CAUGHT`.
   - **Smallest fix:** Plant into a fresh subdirectory under a checked root: `mkdir -p internal/sim/zz_tagonly && printf ... > internal/sim/zz_tagonly/zz_canary.go`.

5. **NIT (Q2 — Change B): `Upload canary result` uploads the stale git-tracked `canaries.json` on early failure or timeout**
   - **File & lines:** `.github/workflows/canaries.yml:39-47`, `agent_work/gates/canaries.sh:366-370`
   - **Reasoning:** `agent_work/0a-foundation/gates/canaries.json` is tracked in git with `"all_caught": true` (2026-11-21). `canaries.sh` only overwrites it at line 369 and never deletes it at startup (unlike `gate.sh:121`). Because `Upload canary result` runs with `if: always()`, an early failure (e.g., Docker image build failure) or a 350-minute timeout uploads the stale committed `all_caught: true` JSON as the failed job's artifact.
   - **Smallest fix:** When `$only = .`, `rm -f agent_work/0a-foundation/gates/canaries.json` near the start of `canaries.sh` (or allow `CANARY_OUT` to write CI runs to a gitignored path such as `agent_work/ci/gates/canaries.json`).

6. **NOTE (Q1 — Change A): Creating `internal/buildcheck/` does not trip any check for the wrong reason**
   - **File & lines:** `agent_work/gates/canaries.sh:76-106`, `agent_work/gates/gate.sh:163, 225, 263`
   - **Reasoning:** `internal/buildcheck` is not in `detlint_dirs()` (`gate.sh:163`), `floor_pkgs` (`gate.sh:225`), or `g4_race`'s `-count=3` list (`gate.sh:263`). All 12 `internal/buildcheck` plants write `gofmt`-clean, valid Go except for the single intended violation; `tagged-import` (sole `//go:build never` file) is skipped without error by `go list`/`vet`/`build` and caught by `importcheck` in `G0/deps`; `landed-prose` has `[no test files]` for G1–G5. (The only wrong-reason risk is the pre-existing `'panic'` regex on `panic-shadow`, covered in Finding 1.)

7. **NOTE (Q2, Q3 — Change B): Workflow syntax, `ci.yml` cleanup, `timeout-minutes: 350` headroom/scaling, concurrency, and gate-strength summary**
   - **File & lines:** `.github/workflows/canaries.yml:1-47`, `.github/workflows/ci.yml:1-41`, `agent_work/gates/canaries.sh:264, 271, 319-322, 333, 356`
   - **Reasoning:**
     - **Syntax:** Both `.github/workflows/ci.yml` and `.github/workflows/canaries.yml` are valid YAML and valid GitHub Actions workflow syntax.
     - **`ci.yml` references:** Removing `Gate canaries` leaves no dangling references; `ci.yml:33-41` only uploads `agent_work/ci/gates/result.json` and `agent_work/ci/gates/*.log` from `gate.sh ci`.
     - **`timeout-minutes: 350`:** Inside GitHub's 360-minute (6-hour) per-job limit for hosted `ubuntu-latest` runners (10 min margin). Scaling watch-item: `canaries.sh` executes **8 full G0–G6 runs** (`clean`, `landed-prose`, `dirty-tree`, `dirty-unknown`, `short-fuzz`, `bad-sha`, `outside-container`, `unnamed-baseline`) plus 2 G3-only runs (`fuzz`, `unicode-fuzz`). With 12 product fuzz targets already under `internal/`, G3 alone takes ~55 minutes inside `canaries.sh`; once `internal/node`, `cmd/`, and `test/e2e` (`g4_race` `-count=3`) land, total runtime can breach 350 minutes unless the 6 `counts_for_landing` negative controls stop re-running full product G1–G5 suites.
     - **Concurrency:** `cancel-in-progress: false` guarantees an in-progress run is never cancelled; GitHub Actions still retains at most one *pending* run per group (a 3rd push replaces the 2nd pending one, slightly narrower than the comment on line 8).
     - **Gate strength (Q3):** Neither Change A nor Change B weakens `gate.sh` or `ci-prose.sh` — full G0–G6 (`GATE_STAGE=final`) and prose provenance still run on every push and PR in `ci.yml`. Change B makes `CI` green on trees the old setup would have failed only for **gate/canary regressions**: PRs breaking `agent_work/gates/**` (Finding 2), pushes editing `.github/scripts/build_site.py` (Finding 2), product pushes breaking a canary (Finding 3), and pushes to `main` breaking `agent_work/gates/**` (where `CI` turns green immediately while `Gate canaries` fails asynchronously ~2 hours later).

## Verification

- **Verified by running:**
  - `agent_work/gates/canaries.sh '^(gofmt|vet|build|panic|comment)$'` (via Docker) — 5 of 5 caught.
  - `agent_work/gates/canaries.sh '^(rapid-in-product|tagged-import|go-path|assembly|panic-shadow|fake-directive|generated-header)$'` (via Docker) — 7 of 7 caught (all 12 `internal/buildcheck` G0 canaries verified).
  - `agent_work/gates/canaries.sh '^(chapter-symlink|tagged-only|self-baseline|stage-typo)$'` (via Docker) — 4 of 4 caught.
  - `python3 -c 'import yaml; ...'` parsed both `.github/workflows/ci.yml` and `.github/workflows/canaries.yml` cleanly.
  - `git show 242fad4 -- agent_work/gates/canaries.sh`, `git status`, `git diff`, `git ls-files agent_work/0a-foundation/gates/canaries.json`, `wc -l internal/sim/*.go`, and `grep '^func Fuzz'` across the tree (12 product fuzz targets in `internal/`).
- **Verified by reading:**
  - `agent_work/gates/canaries.sh` (full), `agent_work/gates/gate.sh` (full), `agent_work/gates/fuzz-short.sh`, `agent_work/gates/site_lint.py`, `agent_work/gates/ci-prose.sh`, `agent_work/gates/prose-check.sh`, `agent_work/gates/panic-allowlist.txt`, `agent_work/gates/detlint-allow.txt`, `agent_work/gates/detlint/{detlint.go,load.go,cmd/{paniccheck,nocomments,importcheck}/main.go}`, `agent_work/orch/land.sh`, `.github/workflows/{ci.yml,canaries.yml,pages.yml}`, `.github/scripts/build_site.py`, `Dockerfile`, `compose.yaml`, `.dockerignore`, `go.mod`, `docs/knowledge/chunk-0a-foundation.md`, `STATE.md`, and `agent_work/0a-foundation/gates/canaries.json`.
  - Did not run the full 50-canary suite (per brief instructions).
