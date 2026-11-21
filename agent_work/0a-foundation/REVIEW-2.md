VERDICT: FAIL

### REVIEW-1 findings status

| # | REVIEW-1 finding | Status | Evidence |
|---|---|---|---|
| 1 | `gate.sh` / `fuzz-short.sh` pipelines swallow `go list`/`find` failures | FIXED | `agent_work/gates/gate.sh:26,115,119,140,145,185,215` and `agent_work/gates/fuzz-short.sh:11,15-19` enable `set -o pipefail`, capture `go list` before filtering, and run `go test ./...` directly in `g5_dst`. |
| 2 | `counts_for_landing` can be `true` on weakened, dirty, or comment-unchecked runs | PARTLY FIXED | `agent_work/gates/gate.sh:55-60,246-247` now enforces `FUZZ_SECONDS >= 30`, defaults `GATE_DIRTY=true`, and restricts `GATE_STAGE` to `build|final`; `.github/workflows/ci.yml:22-35` adds `fetch-depth: 0` and runs `prose-check.sh` on merge commits, **but** non-merge pushes to `main` still run `GATE_STAGE=final` while skipping `prose-check.sh` (see New Finding 4), and newly added `land.sh` ignores `counts_for_landing` (New Finding 1). |
| 3 | `prose-check.sh` misses pre-prose doc edits, non-chapter `docs/`/`book/` edits, and merges | FIXED | `agent_work/gates/prose-check.sh:42-56,76-77,108-114` aligns `is_prose_only` to all `README.md`/`docs/*`/`book/*` (outside operational/C4 exceptions), checks every non-prose commit before and after `first_prose`, and fails on any merge commit in the range. |
| 4 | `g2_property` passes vacuously when rapid tests don't match `Laws|Prop` | FIXED | `agent_work/gates/gate.sh:183-193` runs all tests in every rapid-importing package at `-rapid.checks=2000 -rapid.seed="$seed"` (verified by `unnamed-property` canary). |
| 5 | `canaries.sh` baseline only checks `G0`, ignores failure reason, missing canaries | FIXED | `agent_work/gates/canaries.sh:47,252,255-330` runs clean baseline on full `G0`–`G6` (`counts_for_landing=true`), requires check-log regex matches per canary, adds `vet`, `build`, `bad-image`, `dirty-tree`, `dirty-unknown`, `short-fuzz`, `bad-sha`, `partial-run`, `stage-typo`, `broken-link`, `chapter-symlink`, and `gate.sh:159` runs `site_lint.py --selftest`. |
| 6 | `detlint` misses concrete determinism violations (7 cases) | FIXED | `agent_work/gates/detlint/detlint.go:224-249,419-422,471-474,559-651,658-776`, `load.go:218-318`, and `gate.sh:101-111` switch to a 34-package pure-stdlib allowlist (blocking `reflect`, `os/user`, `path/filepath`), require immediate plain-sort after map-append, flag pointer-capable `fmt` args and `fmt` values, check float type parameters, lint+flag build-tagged/non-Go files, and compute the in-module import closure; interface/`any` limits are documented in `README-tools.txt:69-75,90-92`. |
| 7 | `paniccheck` package-wide shadow bypass; `nocomments` accepts arbitrary `//go:` | FIXED | `agent_work/gates/detlint/cmd/paniccheck/main.go:93-125,156-198` removes the shadowing escape and flags every `panic` declaration (`panic-shadow`) and every bare `panic(` call; `cmd/nocomments/main.go:74,135-140` allows only `^//go:(build|embed|generate|noinline)( |$)` with `//go:build` before `package`. |
| 8 | `build_site.py` rewrites root-absolute links to GitHub URLs | FIXED | `.github/scripts/build_site.py:250-251` leaves `path.startswith("/")` unchanged so `site_lint.py:322-324` flags `absolute-link` (verified by `root-absolute-link` selftest). |
| 9 | `kb-sync.sh` crashes without `/root/Software/kb` and runs Linux binary on host | FIXED | `agent_work/kb-sync.sh:18-26,37-50` builds and runs `kb` inside Docker `dev` and guards `git -C "$up"` in `status` (verified with `KB_REPO=/nonexistent`). |
| 10 | `launch.sh` runs in caller cwd on bad workdir / fragile `NEON-DONE`; `wait.sh` hangs 4h | FIXED | `agent_work/orch/launch.sh:14-16,26-27,31` rejects missing/bad workdir before `cd`, removes `--print-logs`, strips ANSI, and matches whole-line `NEON-DONE`; `agent_work/orch/wait.sh:5-9` exits 3 immediately when neither `.pid` nor `.exit` exists. |
| 11 | `Dockerfile` unpinned / Go version mismatch; `gofmt` omits `detlint/internal`; `astdiff` var-block anchor | FIXED | `Dockerfile:1` pins `golang:1.26.11-bookworm@sha256:dc40a13b...`, `go.mod:5` sets `toolchain go1.26.11`, `gate.sh:115` runs `gofmt -l` on `agent_work/gates/detlint` recursively, and `astdiff/main.go:251-293` uses structural child-index paths. |

---

### New findings

1. **BLOCKER — `land.sh` ignores `counts_for_landing` / `fuzz_seconds` and swallows `git diff` failures on an invalid `sha`**
   - **File & lines:** `agent_work/orch/land.sh:15, 23-27`
   - **What is wrong:**
     - Line 23 checks only `.overall`, `.in_container`, `.partial`, and `.dirty_tree`. It never checks `.counts_for_landing` (or `.fuzz_seconds >= 30`), so a weakened gate run (`FUZZ_SECONDS=0`) that `gate.sh:246-247` explicitly marked `"counts_for_landing": false` is accepted and merged/pushed to `main`.
     - Line 15 sets `set -eu` without `set -o pipefail`, and line 26 runs `later=$(git diff --name-only "$gsha" "$tip" | grep -vE ... || true)`. When `result.json` has `"sha": "unknown"` (or any nonexistent SHA), `git diff` fails with exit 128 and empty stdout, `|| true` swallows the error, `later` is empty, and `land.sh` merges and pushes an unbound gate result.
   - **Concrete reproduction:**
     1. Run `FUZZ_SECONDS=0` (or `GATE_SHA=unknown`) to produce `result.json` with `"overall":"pass","in_container":true,"partial":false,"dirty_tree":false,"counts_for_landing":false`.
     2. Evaluating line 23 (`jq -r '.overall + " " + (.in_container|tostring) + " " + (.partial|tostring) + " " + (.dirty_tree|tostring)'`) yields `"pass true false false"` (accepted).
     3. With `gsha=unknown`, `git diff --name-only unknown HEAD | grep -vE "^agent_work/" || true` prints empty stdout and exits 0, so `[ -z "$later" ]` passes.
   - **Smallest fix:** Add `set -o pipefail` (keeping `|| true` on the `grep` in line 28 so a missing review still prints the error on line 29); require `[ "$(jq -r '.counts_for_landing' <<<"$res")" = true ]`; validate `[[ "$gsha" =~ ^[0-9a-f]{40}$ ]]` and `git merge-base --is-ancestor "$gsha" "$tip"`; and capture `changed=$(git diff --name-only "$gsha" "$tip")` before filtering with `grep -vE`.

2. **BLOCKER — `prose-check.sh`, `gate.sh` (`g0_comments`), and `canaries.sh` (run by `ci.yml`) all fail on every valid tree as soon as Chunk 0b lands with Neon Go comments**
   - **File & lines:** `agent_work/gates/prose-check.sh:83-89`; `agent_work/gates/gate.sh:148-151`; `agent_work/gates/canaries.sh:30-34, 252, 295-305`; `.github/workflows/ci.yml:31-37`
   - **What is wrong:** Per PLAN 2.10 and PLAN 6 (Stage 11), Chunk 0b lands on `main` with Neon's comments in `internal/env/*.go` and `internal/sim/*.go`. Three checks added or hardened in this repair round assume the entire repo has zero Go comments before a chunk's prose commit, with no baseline against `base`/`main`:
     1. `prose-check.sh:83-89` (wired into `ci.yml:32` in commit `55a303b`) calls `tool nocomments -root /work/t/pre` (and `/work/t/head` when there is no prose commit) **without `-baseline /work/t/base`**. Every valid chunk branch from Chunk 1 onward fails Stage 12 and CI on Chunk 0b's already-landed comments.
     2. `canaries.sh:32-33,252,295-305` (run on every push/PR by `ci.yml:37`) runs `counts clean` and five `counts` checks via `gate.sh canary` without `GATE_STAGE=final`, so `gate.sh` defaults to `stage=build` and runs `nocomments -root "$root"` on a copy of the repo. The moment Chunk 0b merges to `main` with Go comments, `canaries.sh` fails `clean` (and the 5 `counts` checks) on `main`, and the `comment`/`fake-directive`/`generated-header` canaries pass vacuously on Chunk 0b's comments.
     3. `gate.sh:148-151` (`g0_comments`) runs `"$bin/nocomments" -root "$root"` with no baseline during `GATE_STAGE=build`, so every Opus build in Chunks 1–7 fails `G0/comments` on previously landed chunks' comments.
   - **Concrete reproduction:** Create a git repo in `/tmp` whose `main` (`$base`) has one commented Go file `internal/sim/sim.go` (simulating landed Chunk 0b). On a branch from `$base`, commit `internal/gossip/gossip.go` with no comments, then a `Prose-By: Gemini 4 Neon` commit commenting `internal/gossip/gossip.go`. Running `agent_work/gates/prose-check.sh "$base" HEAD` fails with exit `1`:
     `internal/sim/sim.go:3:1: comment: comment is not a machine directive: // Simulated clock from landed Chunk 0b.`
     `prose-check: (a) the tree before the first prose commit ... has comments`
     Likewise, adding any Go comment to `internal/buildcheck/buildcheck.go` causes `agent_work/gates/canaries.sh '^clean$'` and `agent_work/gates/gate.sh 01-gossip` (`GATE_STAGE=build`) to fail `G0/comments`.
   - **Smallest fix:**
     - In `prose-check.sh`: export `$base` (`export_tree "$base" base`) and pass `-baseline /work/t/base` to `tool nocomments` on lines 85 and 88.
     - In `gate.sh`: on the host side when `stage=build`, export the baseline commit (e.g. `GATE_BASE`, defaulting to `git merge-base main HEAD` when on a branch) to a temp dir mounted read-only into the container and pass `-baseline` to `nocomments` (empty baseline when `GATE_BASE` is empty/on 0a).
     - In `canaries.sh`: run `clean` and non-comment canaries with `GATE_STAGE=final` (or pass the unmutated copy as `-baseline` to `nocomments`), and run the 3 comment canaries with `-baseline` against the unmutated copy so only the newly planted comment triggers `G0/comments`.

3. **MAJOR — `canaries.sh` `coverage` canary will fail (`MISSED`) in CI once `internal/sim` is populated in Chunk 0b**
   - **File & lines:** `agent_work/gates/canaries.sh:165`; `.github/workflows/ci.yml:37`
   - **What is wrong:** `plant_coverage` appends 1 covered statement (`Double`) and 7 uncovered statements (`Untested`) to `internal/sim/zz_canary.go`. That drops coverage below 85% only while `internal/sim` is empty. Once Chunk 0b lands `internal/sim` (~2,100 lines at ≥85% coverage, e.g. 540/600 = 90%), adding 7 uncovered statements gives 541/608 = 89.0% (≥85%), so `G1/unit` passes and `canaries.sh` (`coverage`) fails on every CI run.
   - **Concrete reproduction:** In a throwaway copy, add a 50-statement fully covered file `internal/sim/existing.go` + `existing_test.go`, then apply `plant_coverage`: coverage is 51/58 = 87.9% ≥ 85%, `G1/unit` passes, and the `coverage` canary reports `MISSED`.
   - **Smallest fix:** In `plant_coverage`, generate uncovered statements proportional to the existing size of `internal/sim` (e.g., `n=$(wc -l internal/sim/*.go 2>/dev/null | tail -1 | awk '{print $1}')` and emit `max(20, n)` unreachable `if` branches in `zz_canary.go`), guaranteeing coverage drops well below 85% regardless of how large `internal/sim` grows.

4. **MAJOR — `ci.yml` skips all comment and prose checks on non-merge pushes to `main` and does not verify merge commits against `HEAD^2`**
   - **File & lines:** `.github/workflows/ci.yml:26-35`
   - **What is wrong:** `ci.yml` runs `gate.sh ci` with `GATE_STAGE: final` (skipping `g0_comments`) and runs `prose-check.sh` only when `HEAD^2` exists. Any direct non-merge push to `main` therefore gets zero comment or prose-provenance checking. On merge commits, `prose-check.sh` checks `$(git merge-base HEAD^1 HEAD^2)..HEAD^2` and never inspects `HEAD` itself, so an evil merge commit carrying extra changes not in `HEAD^2` is unchecked.
   - **Concrete reproduction:** Push a non-merge commit directly to `main` that adds comments to `internal/buildcheck/buildcheck.go` and edits `README.md` without a `Prose-By` trailer; `gate.sh` skips `g0_comments` (`GATE_STAGE=final`) and step "Prose provenance" prints `not a merge commit: no chunk range to check` and exits 0.
   - **Smallest fix:** Once `prose-check.sh` supports `-baseline /work/t/base` (New Finding 2), check `prose-check.sh HEAD~1 HEAD` in the `else` branch for non-merge commits (when `HEAD~1` exists), and on rebased `--no-ff` merge commits verify `git diff --quiet HEAD^2 HEAD` (when `git merge-base --is-ancestor HEAD^1 HEAD^2`).

5. **NIT — `detlint` `orderInsensitive` permits outer-scope loop variables (`for k = range m`), and `canaries.sh` re-runs `site_lint.py --selftest` 30+ times**
   - **File & lines:** `agent_work/gates/detlint/detlint.go:578-585`; `agent_work/gates/gate.sh:159`; `agent_work/gates/canaries.sh:255-283`
   - **What is wrong:**
     - `orderInsensitive` does not require `rs.Tok == token.DEFINE`, so `var last string; for last = range m { ks = append(ks, last) }; slices.Sort(ks); return last` leaks the nondeterministic last visited key without being flagged.
     - Running `site_lint.py --selftest` (22 fixture builds, ~6s) inside `g0_selfcontained` causes `canaries.sh` to execute the same 22 selftest fixtures on every G0 canary (30+ times, pushing total `canaries.sh` runtime past 5 minutes).
   - **Concrete reproduction:** Lint `func Bad(m map[string]int) string { var last string; var ks []string; for last = range m { ks = append(ks, last) }; slices.Sort(ks); return last }` with `detlint` (exits 0). Run `agent_work/gates/canaries.sh` and time it (~5–6 min, >3 min spent in repeated `--selftest`).
   - **Smallest fix:** Require `rs.Tok == token.DEFINE` in `orderInsensitive` (detlint.go:578), and skip `--selftest` inside `g0_selfcontained` when an env flag (only set by non-clean canaries, never counting for landing) is set, or move `--selftest` to a separate check not re-run by unrelated G0 canaries.

---

### Additional question: incremental single-chapter publishing

Confirmed by reading `.github/scripts/build_site.py:324-413`, `book/manifest.json:1-5`, `.github/workflows/pages.yml:1-42`, and testing a single-chapter tree under Docker:

- **Manifest-driven:** `build_site.py` reads `book/manifest.json` dynamically (`load_manifest`, lines 324–381) and has no hardcoded chapter count or chapter list. `pages.yml` triggers on every push to `main` and runs `python3 .github/scripts/build_site.py _site`.
- **Single chapter works before the rest of the book exists:** Adding `book/chapters/01-<slug>.md` plus one object in `book/manifest.json`'s `chapters` list (with `front_matter: []`) builds `book/<slug>.html` (with pager showing only `Contents` — no broken prev/next), lists and links it on `book/index.html` (linked from the top nav `Book` on every page), passes `site_lint.py`, and publishes on the next push to `main`. Chapter numbers only need to be positive and strictly ascending (non-contiguous numbers also work if chunks land out of order).
- **Nothing prevents single-chapter publishing, subject to three existing rules to observe at landing:**
  1. `site_lint.py:257-261` (`chapter-title`): the chapter's first Markdown heading (`# ...`) must match `title` in `book/manifest.json` exactly.
  2. `site_lint.py:262-267,336` (`chapter-unlisted`, `broken-link`): every `.md` file under `book/chapters/` must be in `book/manifest.json`, and the chapter must not link to unwritten future chapters.
  3. `prose-check.sh:24,44`: `book/manifest.json` is classified as `never prose` (rejected in a `Prose-By: Gemini 4 Neon` commit, permitted in a non-prose commit). So Neon commits `book/chapters/NN-<slug>.md` in its prose commit, and the orchestrator adds the manifest entry in a non-prose commit before running Stage 12 `site_lint.py` / `gate.sh`.

---

### Verification summary

- **Verified by running:**
  - `docker compose run --rm -T dev bash -c 'cd agent_work/gates/detlint && gofmt -l . && go vet ./... && go test -v ./...'` — all 5 tool packages pass (`detlint`, `astdiff`, `importcheck`, `nocomments`, `paniccheck`).
  - `agent_work/gates/gate.sh 0a-foundation` — all checks G0–G6 pass (`overall=pass`, `counts_for_landing=false` due to untracked `agent_work/0a-foundation/` and `agent_work/adversarial/`).
  - `agent_work/gates/canaries.sh` — first 30 canaries caught before 300s timeout; remaining 17 canaries run via regex filter (`unit-fail`..`stage-typo`) all caught (47/47 caught total).
  - `agent_work/gates/prose-check.sh a78cffc HEAD` — clean over 9 commits.
  - `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py --selftest` (22/22 PASS) and `site_lint.py /src` (0 findings).
  - `KB_REPO=/nonexistent agent_work/kb-sync.sh status` — exits 0 with `upstream: unavailable`.
  - `agent_work/orch/launch.sh` with missing workdir (exits 2 immediately) and `agent_work/orch/wait.sh` with missing `.pid`/`.exit` (exits 3 immediately).
  - Concrete reproductions in `/tmp` (cleaned up): confirmed `land.sh` line 23 accepts `FUZZ_SECONDS=0` (`counts_for_landing=false`), line 26 accepts `gsha=unknown`; confirmed `prose-check.sh` exits 1 on a valid Chunk 1 branch based on a commented `main`; confirmed a single-chapter tree (`book/chapters/01-deterministic-simulation.md` + 1 `book/manifest.json` entry) builds `book/deterministic-simulation.html`, links it in `book/index.html`, and passes `site_lint.py`.
- **Verified by reading:**
  - `PLAN.md` (2.1, 2.5, 2.9, 2.10, 4.2, 5.1, 6, 7.6, 7.9, 7.10, 9.1), `agent_work/0a-foundation/REVIEW-1.md`.
  - `git log --format=... e62416a..HEAD` and `git diff --stat e62416a..HEAD`.
  - `agent_work/gates/{gate.sh,fuzz-short.sh,prose-check.sh,canaries.sh,site_lint.py}`, `agent_work/gates/detlint/{README-tools.txt,main.go,load.go,detlint.go,internal/report/report.go,cmd/{nocomments,astdiff,paniccheck,importcheck}/main.go}`.
  - `.github/scripts/build_site.py`, `book/manifest.json`, `.github/workflows/{ci,pages}.yml`, `Dockerfile`, `go.mod`, `compose.yaml`, `agent_work/kb-sync.sh`, `agent_work/orch/{launch.sh,wait.sh,land.sh}`.
