VERDICT: NITS

### Findings

1. **NIT — `in_dev` runs `docker compose build -q dev` on every call (twice on cold runs, and on every read-only `search`)**
   - **File & line:** `agent_work/kb-sync.sh:18`
   - **What is wrong:**
     - On a cold checkout (when `.kb/bin/kb` is absent or `.built-sha` does not match), both `index` (`kb-sync.sh:37-39`) and `search` (`kb-sync.sh:40-43`) call `build()` (which calls `in_dev`) and then call `in_dev` a second time for `reindex`/`search`, running `docker compose build -q dev` twice back-to-back.
     - Every warm `kb-sync.sh search` also runs `docker compose build -q dev` (~1.26 s overhead on a cached BuildKit build here) even though `.kb/bin/kb` is already compiled and does not dynamically link `libsqlite3.so` (`ldd /src/.kb/bin/kb` shows only `libm.so.6` and `libc.so.6`). Each build exports a fresh BuildKit attestation manifest, mutating the shared `objstore-dev:local` tag to a new image ID (observed changing `sha256:805490d4...` -> `sha256:cbfd79fd...`) and emitting `Image objstore-dev:local Building / Built` to stderr. Because `compose.yaml` hardcodes the global tag `objstore-dev:local`, running read-only `kb-sync.sh search` from a worktree with a different `Dockerfile` will also overwrite the shared image tag for other worktrees.
   - **Why it is non-blocking:** `docker compose build -q dev` writes progress to stderr only (verified: `kb-sync.sh search ... 2>/dev/null` produces clean stdout), propagates build errors under `set -euo pipefail`, matches `gate.sh:89`, and guarantees a clean clone or post-`Dockerfile`-edit worktree builds the image before building or running `kb`.
   - **Smallest fix (optional):** Track `image_built=0` outside the subshell so `in_dev` builds at most once per invocation (eliminating the cold-run double build), or move `docker compose build -q dev` to a helper invoked in `build()` and only when `docker image inspect objstore-dev:local` fails on warm `search`.

---

### Review questions

1. **Does the fix do what it says?** Yes.
   - `Dockerfile:4` adds `libsqlite3-dev` (`3.40.1-2+deb12u2`, ~3.1 MB installed, depending only on `libsqlite3-0` and `libc-dev`), placing `/usr/include/sqlite3.h` in `objstore-dev:local`.
   - `agent_work/kb-sync.sh index` exited 0 and printed (stdout):
     `entries: 1 scanned, 0 reindexed, 1 skipped; chunks: 0; embed calls: 0`
     (with Docker Compose `Image objstore-dev:local Building/Built` and `Container ... Creating/Created` on stderr). Because `.kb/bin/kb` and `.kb/stores/default/kb.sqlite` were already present in this worktree from the prior run, `build()` hit its `.built-sha` cache; a separate clean compile of `github.com/asg017/sqlite-vec-go-bindings/cgo` with a fresh `GOCACHE=/tmp/gc` and `kb/cmd/kb` inside `objstore-dev:local` confirmed the SQLite header fix builds cleanly from scratch.
   - `agent_work/kb-sync.sh search "gate stage on main"` exited 0 and printed 2 hits from `docs/knowledge/chunk-0a-foundation.md`:
     - `[1] docs/knowledge/chunk-0a-foundation.md › Pitfalls that cost time` (build-stage gate run on `main` refused with exit 2; use `GATE_STAGE=final`)
     - `[2] docs/knowledge/chunk-0a-foundation.md › Findings` (landing gate stages `build` and `final`)
     followed by `search_id=2` (and `search_id=3` on the stdout/stderr split check).

2. **Does adding `libsqlite3-dev` change anything about the product build or the gates?** No.
   - `CGO_ENABLED=1` and `gcc`/`libc-dev` were already in `Dockerfile` (required for `-race` in G4). `libsqlite3-dev` adds C headers/dev libraries that Go builds do not touch unless a package imports `"C"`.
   - Root `go.mod` is unchanged (`require pgregory.net/rapid v1.3.0`), and `G0/deps` (`agent_work/gates/gate.sh:187-207` and `agent_work/gates/detlint/cmd/importcheck/main.go:62-70`) still forbids `import "C"`, C/header/assembly files in the product tree, and any imports outside the standard library and this module (plus `pgregory.net/rapid` in tests).
   - `GATE_ONLY=G0,G6 agent_work/gates/gate.sh review-check` passed all 10 checks (`G0` `tools`, `gofmt`, `vet`, `build`, `detlint`, `panics`, `deps`, `comments`, `selfcontained`; `G6` `docker`) inside `objstore-dev:local` (`sha256:805490d4bdd8033c5e5d214896de860dccf0f6e458a2f6419f6a60f9bd74240b`, `go1.26.11 linux/amd64`, `overall=pass`, `counts_for_landing=false` as expected for a partial `GATE_ONLY` run).

3. **Anything wrong with building the image inside `in_dev`?**
   - No correctness blocker (stderr-only progress, proper error propagation, guarantees the image exists and matches `Dockerfile` on a clean clone). See Finding 1 (NIT) for the double-build on cold runs, ~1.26 s warm-search overhead, and shared `objstore-dev:local` tag churn across worktrees.

---

### What I ran

- `git diff main..HEAD -- Dockerfile agent_work/kb-sync.sh` and `git show 14fe3f4` — inspected commit `14fe3f4` and read `Dockerfile`, `agent_work/kb-sync.sh`, `compose.yaml`, `go.mod`, `kb/go.mod`, `.dockerignore`, `.kb/.gitignore`, `.kb/stores/default/config.toml`, `agent_work/gates/gate.sh`, `agent_work/gates/detlint/cmd/importcheck/main.go`, `.github/workflows/ci.yml`.
- `agent_work/kb-sync.sh index` — exit 0; stdout: `entries: 1 scanned, 0 reindexed, 1 skipped; chunks: 0; embed calls: 0`.
- `agent_work/kb-sync.sh search "gate stage on main"` — exit 0; 2 hits from `docs/knowledge/chunk-0a-foundation.md` (`search_id=2`); also verified stdout vs stderr separation (`2>/dev/null` and `>/dev/null`).
- `agent_work/kb-sync.sh status` — exit 0 (`vendored` == `upstream` `48e64f67...`, `docs/knowledge entries: 1`, `index: fresh`).
- `GATE_ONLY=G0,G6 agent_work/gates/gate.sh review-check` — exit 0; all 10 checks (`G0` x9, `G6` x1) passed; inspected `agent_work/review-check/gates/{result.json,G0-deps.log,G6-docker.log}`, then removed `agent_work/review-check/`.
- `docker compose run --rm -T dev bash -c 'cd kb && CGO_ENABLED=1 go build -buildvcs=false -tags fts5 -o /tmp/kb-review-check ./cmd/kb && GOCACHE=/tmp/gc CGO_ENABLED=1 go build -buildvcs=false -tags fts5 github.com/asg017/sqlite-vec-go-bindings/cgo'` — exit 0; verified `/usr/include/sqlite3.h` (616,357 bytes) and clean cgo compile from fresh cache.
- `docker compose run --rm -T dev bash -c 'dpkg -s libsqlite3-dev ...; ldd /src/.kb/bin/kb'` — verified `libsqlite3-dev` dependencies and that `.kb/bin/kb` links only `libm.so.6` and `libc.so.6`.
- `time docker compose build -q dev` and `docker compose build --progress=plain dev` + `docker image inspect objstore-dev:local --format '{{.Id}}'` — measured ~1.26 s cached build time and observed fresh attestation manifest ID (`sha256:cbfd79fd...`).

NITS CLOSED — nit 1 fixed in the following commit: the image is built once, in build(), only when the kb binary is compiled (orchestrator, 2026-11-21).
