# Chunk 0a adversarial gate results

Run from the worktree root:

```bash
bash agent_work/adversarial/0a-foundation/run-all.sh
```

Each `aNN-*.sh` also runs independently without arguments. Bash entrypoints use the shared, standard-library-only `harness.py` to create fixtures and judge actual tool results. Go builds and checks run in the dev container; the prose-history driver runs on the host because `prose-check.sh` itself invokes Docker. Fixtures, including git repositories, live under temporary `/tmp` directories and are removed on exit. Persistent writes are confined to this directory. No project source was repaired or committed.

Exit 0 means every attack held, 1 means at least one broke its claim, and 2 means setup failed. `evidence/run-all.txt` contains the complete collected output; `evidence/summary.txt` contains the final count and runtime. Per-attack logs are linked below. `evidence/target.json` records the target commit, branch, and tool-source hashes. A BROKEN result is expected to make this adversarial suite exit 1.

## Findings that need attention

- **Landing provenance:** a full run in an unversioned copy accepts `GATE_SHA=not-a-commit`, `GATE_DIRTY=false`, and `GATE_IMAGE_ID=invented-image`, and emits `counts_for_landing: true`. The gate does not establish those facts itself. Partial, explicitly dirty, and unknown-SHA controls do not count.
- **Publication containment:** chapter symlinks publish a private sentinel from each of `docs/knowledge/`, `agent_work/`, `kb/`, and `STATE.md`, with site lint green. A book asset symlink also copies private content. Manifest traversal using `..` is rejected, but normalized lexical paths do not establish physical containment.
- **G0 coverage:** full G0 accepts entropy through an unlinted helper, pointer `%v`, reflection map iteration, `context.AfterFunc`, and an amd64 assembly timestamp counter. A build-excluded declaration named `panic` masks an active builtin panic in a different file. A misspelled `GATE_STAGE=buidl` disables the comment check.
- **Prose equivalence and history:** `astdiff` ignores embedded payloads, module files, assembly, and cgo preamble changes. The history checker skips merge commits and accepts later untrailed documentation edits, comment deletion, and repetition of an existing comment. It also accepts a prose edit to embedded CLI help. Build constraints, string literals, struct tags, and embed directives are rejected by the AST tool.
- **Vacuous checks:** G2 skips an always-failing `TestInvariant`; G3 misses the valid Unicode target `FuzzÉ`; G5 misses a scenario that imports the simulator transitively. Direct invocations confirm those fixtures fail when selected. The fuzz runner also accepts a filter selecting no targets and a module with no packages.

## How to interpret the verdicts

HELD means this fixture was rejected (or a named safe control was accepted), not that the general claim is proved. BROKEN means the tested tool accepted the counterexample to the claim as worded. A G0 abort with no result is a rejection, not a harness setup failure; the clean G0 baseline must pass first.

Several boundaries matter:

- `testdata` comments and generated headers are documented exclusions in the tooling. Their BROKEN verdicts challenge C3's blanket wording; they do not demonstrate comments entering a compiled product package. The nested-module fixture is an unimported `extras/go.mod` with an extra requirement, not proof that an external dependency is reachable from the root product.
- Test-only, hidden-name, and inactive-tag entropy fixtures expose scan scope. They do not all execute in the ordinary product build. The helper, reflection, pointer formatting, context callback, and assembly fixtures also pass full G0 as active product code.
- `%v` on a scalar-key map is a safe control: fmt orders those keys. `%v` on pointers or pointer-keyed maps exposes addresses and is accepted. The two sort probes deliberately use comparators that leave all elements tied; merely seeing a sort call does not prove canonical order.
- The cgo-clock fixture is rejected, including a typecheck finding; this does not demonstrate successful cgo analysis. Separately, the AST tool accepts a changed C function body inside the preamble. G0's import check rejects `C`, so this AST miss alone is not a full-G0 bypass.
- `astdiff` explicitly documents that it ignores non-Go files. `prose-check.sh` separately rejects prose commits touching `go.mod` or assembly; the standalone misses must not be described as bypasses of that surrounding file policy. Embedded help is permitted by that policy and is tested over a real commit range.
- A direct link to private knowledge is rewritten to GitHub, rather than copying its content into the generated site. The sentinel nonpublication control holds; the link itself remains public-facing. No remote site was published.
- The stale-result probe plants an earlier-looking result and invokes the gate without a chunk argument. The old file remains untouched after exit 2. A failure after gate-side startup (invalid `go.mod`) clears it. Consumers must check both invocation status and result freshness; this is not evidence that a mid-run crash produced a new green result.

## Coverage limits

Breadth was prioritized over exhaustive combinations. Not independently exercised: init ordering across renamed files; arbitrary interface/method-value dispatch; custom iterator implementations outside the tested helper and `maps.Keys` paths; every runtime/reflect API; cgo compiler flags or exotic filename encodings; non-amd64 assembly; all allowed-directive spellings; symlinked `GATE_OUT`; forged host container markers; a SIGKILL half-way through a gate; failure during the host image-build phase; or forged coverage output. The no-coverage package control was exercised. These remain unproven rather than implicitly HELD. The assembly counter witness is specific to this amd64 dev environment.

The reported results concern this foundation tree. They are not a claim that future simulation packages, properties, fuzz targets, or DST scenarios have run. No product-level simulation exists in these fixtures beyond the deliberately planted probes.

## Actual run

<!-- The final execution summary and per-claim inventory follow. -->
