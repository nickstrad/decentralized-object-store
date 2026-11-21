VERDICT: FAIL

### Findings

1. **BLOCKER — `gate.sh` pipelines swallow `go list` / `find` failures and pass vacuously (Q1)**
   - **File & lines:** `agent_work/gates/gate.sh:19, 112, 116, 153, 179-180`; `agent_work/gates/fuzz-short.sh:24`
   - **What is wrong:** `gate.sh` sets `set -u` without `set -o pipefail`. Several checks pipe `go list` or `product_go_files` into `grep`/`cut`/`importcheck` or append `|| true`. If `go list` fails, stdout is empty and the downstream command exits 0 (or `|| true` hides it), causing `g0_deps`, `g2_property`, and `g5_dst` to pass vacuously. Likewise, `fuzz-short.sh` feeds `while read` from `< <(go list ...)`, which ignores `go list`'s exit status and reports `0 fuzz targets` with exit 0.
   - **Reproduction:** Corrupt `go.mod` syntax or pass an invalid pattern; `go list -deps ... | grep -v ... || true` in `g0_deps:112` sets `bad=""` and passes; `go list ... | grep rapid | cut ...` in `g2_property:153` sets `pkgs=""` and prints `NOTE: no package imports rapid` with exit 0.
   - **Smallest fix:** Enable `set -o pipefail` in `gate.sh` and `fuzz-short.sh`, capture `go list` output into a variable before filtering (`out=$(go list ...) || return 1`), and avoid `< <(go list ...)` in `fuzz-short.sh`.

2. **BLOCKER — `counts_for_landing` can be `true` on weakened, dirty, or comment-unchecked runs (Q1, Q5)**
   - **File & lines:** `agent_work/gates/gate.sh:119-122, 215-218`; `.github/workflows/ci.yml:23-26`
   - **What is wrong:**
     - `counts_for_landing` does not check `FUZZ_SECONDS` (running `FUZZ_SECONDS=0` or `1` still sets `counts_for_landing: true`).
     - `GATE_DIRTY` defaults to `false` (`${GATE_DIRTY:-false}`), so invoking `gate.sh` directly inside the container on a dirty tree with `GATE_SHA` and `GATE_IMAGE_ID` set marks `counts_for_landing: true`.
     - Any `GATE_STAGE` other than `build` (including `GATE_STAGE=final`, which `.github/workflows/ci.yml:25` uses) skips `nocomments` with `return 0`, and `prose-check.sh` is never invoked by `gate.sh` or `ci.yml`. Thus CI never checks comments or prose provenance on PRs or `main`.
   - **Reproduction:** Run `FUZZ_SECONDS=0 GATE_STAGE=final agent_work/gates/gate.sh 0a-foundation` on a tree containing an arbitrary comment in `internal/buildcheck/buildcheck.go`; G0–G6 pass and `result.json` reports `"counts_for_landing": true`.
   - **Smallest fix:** Require `[ "${FUZZ_SECONDS:-30}" -ge 30 ]`, default `GATE_DIRTY` to `true` when unset (so only the host wrapper's verified clean check sets `false`), restrict `GATE_STAGE` to `build|final`, and run `prose-check.sh` in `ci.yml` (with `fetch-depth: 0` on checkout).

3. **BLOCKER — `prose-check.sh` misses non-prose edits to reader-facing docs before the first prose commit, misses `docs/*.md`/`book/*.md` after it, and skips merge commits (Q4)**
   - **File & lines:** `agent_work/gates/prose-check.sh:28-41, 60, 66-80, 97`
   - **What is wrong:**
     - Check (c) uses `is_prose_only`, which only lists `README.md|docs/index.md|book/chapters/*|cmd/*/help.txt`. It omits `docs/c4/*.md`, other `docs/*.md`, and non-chapter `book/*.md` (e.g., `book/preface.md`), all of which `is_reader_facing` treats as Neon-owned prose.
     - Commits *before* `first_prose` (or any commit when no prose commit exists in `base..head`) are never checked for reader-facing markdown changes.
     - `git rev-list --no-merges` skips merge commits entirely, so an evil merge commit can introduce Go comments or prose without the trailer.
   - **Reproduction (breaks PLAN 2.10 and passes):**
     1. Commit 1 (no trailer): add `book/chapters/01-sim.md` and rewrite `README.md` (no Go comments).
     2. Commit 2 (`Prose-By: Gemini 4 Neon`): add one Go comment in `internal/sim/sim.go`.
     3. Commit 3 (no trailer): rewrite `docs/c4/01-context.md` and `book/preface.md`.
     `agent_work/gates/prose-check.sh <base> HEAD` exits 0.
   - **Smallest fix:** Align `is_prose_only` with `is_reader_facing` (minus orchestrator-owned C4 structural files if desired, or require trailer on all reader-facing files), enforce `is_prose_only` on commits *before* `first_prose` as well (and when `first_prose` is empty), and fail or explicitly verify merge commits (e.g., fail if a merge commit introduces diff relative to its parents in prose/Go comments).

4. **MAJOR — `g2_property` passes vacuously when rapid test names do not match `Laws|Prop` (Q1)**
   - **File & lines:** `agent_work/gates/gate.sh:156-158`
   - **What is wrong:** `go test ... "$p" -run 'Laws|Prop' -rapid.checks=2000` exits 0 with `[no tests to run]` if a rapid-importing package has property tests named e.g. `TestFieldAxioms` or `TestRoundTrip`.
   - **Reproduction:** Rename `TestPropRapidIsWired` in `internal/buildcheck/rapid_test.go` to `TestRapidIsWired` and make it fail; `G2` prints `[no tests to run]` and passes.
   - **Smallest fix:** Capture `go test -v` (or `-json`) output for each rapid-importing package and fail if zero tests actually executed, or run without `-run` for rapid packages (or require a `TestProp`/`Laws` match count > 0).

5. **MAJOR — `canaries.sh` only checks clean baseline on `G0`, never verifies the failure reason, and has no canaries for several checks (Q2)**
   - **File & lines:** `agent_work/gates/canaries.sh:40-48, 156-161`
   - **What is wrong:**
     - Line 157 runs `run_gate clean G0`, not `G0-G6`. If `G1`–`G5` are already broken on the clean tree, the `unit-fail`, `coverage`, `property`, `fuzz`, `race`, and `dst-seeds` canaries all report `CAUGHT` vacuously.
     - `canary()` only checks `status == fail` for `$gate/$chk`, not that the log contains the expected rule/message (e.g. a compile error in a planted Go file would still make `G1/unit` or `G0/detlint` fail and report `CAUGHT`).
     - All 8 required canaries from PLAN 4.2 are present (`gofmt`, `time-now`, `second-require`, `rapid-in-product`, `panic`, `comment`, `doc-path`, `outside-container`). Checks with NO canary: `G0/vet`, `G0/build`, `G6` missing `GATE_IMAGE_ID` inside container, `GATE_DIRTY=true` -> `counts_for_landing=false`, `prose-check.sh`, and `site_lint.py` manifest/broken-link rules (its `--selftest` is never invoked by `gate.sh` or `canaries.sh`).
   - **Reproduction:** Break `TestPropRapidIsWired` on the clean tree; `canaries.sh` still passes `clean` (only ran `G0`) and marks `property` as `CAUGHT` for the wrong reason.
   - **Smallest fix:** Run the clean baseline on all gates `G0,G1,G2,G3,G4,G5,G6` (with small `FUZZ_SECONDS`), have `canary()` grep the check log for an expected pattern, and add canaries for `dirty_tree` and `site_lint.py --selftest`.

6. **MAJOR — `detlint` misses concrete determinism violations required by PLAN 2.1/2.5 (Q3)**
   - **File & lines:** `agent_work/gates/detlint/detlint.go:203-206, 217-230, 232-238, 433-438, 488-576, 580-594`; `agent_work/gates/detlint/load.go:23, 162`; `agent_work/gates/gate.sh:81-89`
   - **What is wrong & concrete unflagged Go snippets:**
     1. **Map order used before sort / unreachable sort (`orderInsensitive`, lines 550-569):**
        ```go
        func Bad(m map[string]int) []string {
            var ks []string
            for k := range m { ks = append(ks, k) }
            out := append([]string(nil), ks...) // nondeterministic order escapes!
            slices.Sort(ks)                     // satisfies orderInsensitive
            return out
        }
        ```
        (Or `if false { slices.Sort(ks) }`.)
     2. **Map iteration via `reflect.Value.MapKeys()` / `MapRange()` (lines 204, 433-438):**
        ```go
        for _, k := range reflect.ValueOf(m).MapKeys() { _ = k }
        ```
     3. **Host filesystem / OS identity outside `os` (`bannedImport`, lines 217-230):**
        ```go
        import ("os/user"; "path/filepath")
        func Bad() { _, _ = user.Current(); _, _ = filepath.Glob("*") }
        ```
     4. **Pointer / func address formatting outside `*f` or with non-constant format (`lintPtrFmt`, lines 580-594):**
        ```go
        func Bad(x *int) string { return fmt.Sprint(x) }
        func Bad2(x *int) string { f := "%p"; return fmt.Sprintf(f, x) }
        ```
     5. **Generic float constraints (`isFloat`, line 232 does not use `underIs`):**
        ```go
        func Bad[T ~float64](a, b T) bool { return a < b }
        ```
     6. **Build-tagged files silently skipped (`load.go:162`):** any file in `internal/sim` with `//go:build e2efault` (used in PLAN 2.7) is skipped by `build.Default.MatchFile`.
     7. **Transitive imports of sim-reachable packages not checked (`gate.sh:81-89`):** `detlint_dirs` hardcodes `env`, `sim`, `gossip`, `erasure`, `merkle`, `occ`, `saga`; helper packages they import (or `internal/storage` chunk store used in Chunk 2 DST) are never linted.
   - **Allowlist check:** `detlint`'s allowlist (`path<TAB>Func<TAB>rule<TAB>justification`) uses exact string matching with stale-entry errors, so it cannot silence a whole package by accident. (Note `"-"` covers all package-level code in one file.)
   - **Smallest fix:** In `orderInsensitive`, require the sort call to be the immediately following statement in the same block; ban `reflect.Value.MapKeys`/`MapRange`, `os/user`, and filesystem-touching `path/filepath` functions (`Abs`, `EvalSymlinks`, `Glob`, `Walk`, `WalkDir`); use `underIs` in `isFloat`; expand `detlint_dirs` (or walk imports of sim-reachable packages) via `go list -deps`.

7. **MAJOR — `paniccheck` can be silenced for an entire package by a build-tagged file; `nocomments` allows arbitrary `//go:` text (Q3)**
   - **File & lines:** `agent_work/gates/detlint/cmd/paniccheck/main.go:138-152`; `agent_work/gates/detlint/cmd/nocomments/main.go:139-141`
   - **What is wrong:**
     - `paniccheck` parses all non-test `.go` files in a directory ignoring build tags. If any file (even `//go:build never`) declares `func panic() {}`, `shadowPkg[pkg]` becomes `true` and `paniccheck` skips the entire package.
     - `nocomments` accepts any comment starting with `//go:`, not only valid Go directives.
   - **Reproduction:** Add `//go:build never\n\npackage a\nfunc panic() {}` to `internal/a/`; `paniccheck` ignores all real `panic(...)` calls in `internal/a/`. Add `//go: this is a normal comment` to any Go file; `nocomments` exits 0.
   - **Smallest fix:** In `paniccheck`, respect build tags (or only apply `declaresPanic` per buildable set / disallow shadowing `panic` altogether). In `nocomments`, match an explicit allowlist of Go directives (`//go:build`, `//go:embed`, `//go:generate`, `//go:noinline`, etc., matching PLAN 2.10).

8. **MAJOR — `build_site.py` rewrites root-absolute links that exist on the host/container filesystem to GitHub URLs, defeating `site_lint.py`'s `absolute-link` check (Q6)**
   - **File & lines:** `.github/scripts/build_site.py:215-224`
   - **What is wrong:** `posixpath.join(src_dir, "/etc/passwd")` returns `"/etc/passwd"`, and `self.root / "/etc/passwd"` evaluates to `Path("/etc/passwd")`. Since `not target.startswith("..")` is `True` and `Path("/etc/passwd").exists()` is `True`, `build_site.py` rewrites `href="/etc/passwd"` (or `href="/tmp"`, `href="/bin"`) to `https://github.com/.../blob/main//etc/passwd`, which `site_lint.py` skips as an external link instead of flagging `absolute-link`.
   - **Reproduction:** Add `[bad](/etc/passwd)` to `book/chapters/01-alpha.md` and run `site_lint.py`; `absolute-link` does not fire.
   - **Smallest fix:** In `rewrite_links`, return `match.group(0)` immediately when `path.startswith("/")` so root-absolute links are never resolved against `self.root` and `site_lint.py` catches them as `absolute-link`.
   - **Never-publish check:** Confirmed `build_site.py` does not publish `docs/knowledge/`, `agent_work/`, `STATE.md`, or `kb/` into `_site`.

9. **MAJOR — `kb-sync.sh` breaks on a clean clone without `/root/Software/kb` and breaks on non-Linux hosts without Go (Q7)**
   - **File & lines:** `agent_work/kb-sync.sh:9, 11, 20-26, 36-40`
   - **What is wrong:**
     - `status` runs `git -C "$up" rev-parse HEAD` under `set -e` with `up=${KB_REPO:-/root/Software/kb}`. On any clean clone without `/root/Software/kb`, `kb-sync.sh status` crashes before reporting entry count or index freshness.
     - `index` calls `build()` (which builds a Linux binary inside Docker when host `go` is absent) and then executes `"$bin" reindex --all` on the **host**, which fails with `Exec format error` on macOS. Conversely, when host `go` is present, it bypasses Docker (violating PLAN 7.6's single-environment rule and failing if host lacks cgo/gcc or has a different Go version).
   - **Reproduction:** On a clean clone without `/root/Software/kb`, run `agent_work/kb-sync.sh status` -> exits nonzero at line 40. On macOS without Go, run `agent_work/kb-sync.sh index` -> Linux binary fails on host at line 38.
   - **Smallest fix:** In `status`, guard `git -C "$up"` with `[ -d "$up/.git" ]` and print `upstream: unavailable` otherwise. In `index` and `build`, run both the build and `kb reindex --all` inside `docker compose run --rm -T dev` (or only execute on host when built on host).
   - **Root-module isolation:** Confirmed root `go.mod` and `internal/` do not import `kb/`.

10. **MAJOR — `launch.sh` can launch an agent in the caller's cwd (e.g. main checkout) for 15 seconds on a bad workdir; `wait.sh` hangs 4 hours on a missing `.pid` file (Q8)**
    - **File & lines:** `agent_work/orch/launch.sh:11, 21-22, 26-34`; `agent_work/orch/wait.sh:5-11`
    - **What is wrong:**
      - `launch.sh` uses `set -u` (not `set -e`). If `$3` (`<workdir>`) does not exist, `wd=$(cd "$3" && pwd -P)` fails and sets `wd=""`. Line 26 `cd ""` succeeds in bash as a no-op (staying in the caller's directory, e.g., main checkout) and launches the agent there for 15 seconds before line 33 kills it.
      - In `launch.sh:21-22`, `tail -n 5 '$base.out' | grep -q NEON-DONE` can misclassify a completed run as exit 70 if `opencode --print-logs` emits >5 log lines after `NEON-DONE`, or falsely match `NEON-DONE` inside a prose sentence.
      - In `wait.sh:7,10`, if `$base.pid` is missing (or `$1` is empty/typo'd), `pid=""`, so `[ -n "$pid" ]` is false and `wait.sh` loops for the full 4-hour deadline instead of failing immediately.
    - **Reproduction:** Run `agent_work/orch/launch.sh test opus /nonexistent /tmp/brief` from main checkout -> `wd=""`, `cd ""` stays in main checkout, `claude` runs in main checkout for 15s. Run `agent_work/orch/wait.sh nonexistent 60` -> sleeps 60s and exits 4 instead of failing immediately.
    - **Smallest fix:** In `launch.sh`, check `[ -d "$3" ] || exit 2` and `[ -n "$wd" ] || exit 2` before `cd`, and check `grep -qx NEON-DONE "$base.out"` (or last non-log line). In `wait.sh`, add `set -u`, require `$1`, and fail with exit 3 if neither `$base.exit` nor `$base.pid` exists.

11. **NIT — `Dockerfile` base image / `apt` packages unpinned and Go patch version differs from `PLAN.md` / `go.mod`; `g0_gofmt` omits `detlint/internal` (Q1, Q5)**
    - **File & lines:** `Dockerfile:1-5`; `go.mod:3`; `agent_work/gates/gate.sh:93`; `agent_work/gates/detlint/cmd/astdiff/main.go:188-201`
    - **What is wrong:**
      - `Dockerfile:1` uses `golang:1.26.11-bookworm` without `@sha256:...` digest, `go.mod` specifies `go 1.26.0` without `toolchain`, whereas PLAN 7.6 specifies `toolchain go1.26.8` pinned identically in `Dockerfile`.
      - `gate.sh:93` passes `agent_work/gates/detlint/*.go agent_work/gates/detlint/cmd` to `gofmt -l`, omitting `agent_work/gates/detlint/internal`.
      - `astdiff` (`anchorOf`) assigns the same anchor `2*i+1` to all directives inside a grouped `var (...)` block, so moving `//go:embed` from one `ValueSpec` to another inside the same `var` block is not detected.
    - **Smallest fix:** Pin `Dockerfile` `FROM` by digest (and align Go version with `go.mod`/PLAN), pass `agent_work/gates/detlint` as a whole directory to `gofmt -l`, and anchor `astdiff` directives to the enclosing spec/node or token index.

---

### Verification summary
- **Verified by running:**
  - `git log --oneline a78cffc..HEAD` and `git diff --stat a78cffc..HEAD` (confirmed 6 commits and file set).
  - `grep` over `internal/` confirming no imports of `kb/`.
- **Verified by reading:**
  - `PLAN.md` (sections 2.1, 2.5, 2.9, 2.10, 3, 4.2, 7.6, 7.9, 7.10, 9.1).
  - `agent_work/gates/gate.sh`, `fuzz-short.sh`, `canaries.sh`, `prose-check.sh`, `detlint-allow.txt`, `panic-allowlist.txt`.
  - `agent_work/gates/detlint/` (`go.mod`, `main.go`, `load.go`, `detlint.go`, `detlint_test.go`, `internal/report/report.go`, `cmd/{nocomments,astdiff,paniccheck,importcheck}/main.go` and tests).
  - `Dockerfile`, `compose.yaml`, `.dockerignore`, `.gitignore`, `.github/workflows/{ci,pages}.yml`, `go.mod`, `internal/buildcheck/{buildcheck.go,rapid_test.go}`.
  - `.github/scripts/build_site.py`, `agent_work/gates/site_lint.py`, `book/manifest.json`.
  - `agent_work/kb-sync.sh`, `agent_work/kb-vendor.sha`, `.kb/{kb.toml,.gitignore,stores/default/config.toml}`, `docs/knowledge/README.md`.
  - `agent_work/orch/{launch.sh,wait.sh}`.
  - Note: Did not execute `gate.sh` or `canaries.sh` under Docker during this review.
