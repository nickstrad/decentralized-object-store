VERDICT: FAIL

The core `internal/saga` engine, append-only checksummed log, `Resume` state machine, and five-step migration workflow are well designed, clean, deterministic, and thoroughly tested (all named plan tests, properties, fuzz target, canary, and DST scenarios exist and pass). Two MAJOR issues in `internal/saga/sagasim` require repair before landing — one breaks the exported matrix runner's integration contract with Chunk 6, and one is a codec/persistence round-trip bug in the model store — along with six NITs and one integration NOTE.

## Findings

### 1. MAJOR — `sagasim.Run` hardcodes absolute `matrixBegin` and `MatrixBound` (unusable by Chunk 6 on a pre-warmed cluster)
- **File & line:** `internal/saga/sagasim/matrix.go:17-20`, `189-192`, `317-322`, `329`, `360-397`
- **What is wrong:** `sagasim.Run` is exported so Chunk 6 can implement `sagasim.Harness` with real nodes (SWIM gossip, Reed-Solomon erasure coding, Merkle, OCC) and re-run the 78-cell matrix (`TestClusterMigrationFailurePointsDST`). However, `Run` schedules `cl.Begin(matrixSaga)` at the fixed absolute virtual time `matrixBegin = 20ms`, runs `s.RunUntilConverged(matrixBegin.Add(MatrixBound), done)` with an absolute deadline of `20.02s`, and computes `Report.Elapsed = end.Sub(matrixBegin)`.
  In Chunk 6, a real cluster starts at `t = 0` and must converge SWIM membership, PUT the initial erasure-coded objects, and replicate OCC manifests before starting a migration. If `Harness.Build` advances `s` to warm up the cluster to `s.Now() = T`:
  - for `T >= 20.02s`, `RunUntilConverged` stalls immediately with `sim.ErrDeadline` before `cl.Begin` ever executes;
  - for `0 < T < 20.02s`, warmup time is stolen directly from `MatrixBound` (leaving too little headroom for `fail-mid-compensation-torn`, which already reaches 5.58 s on the lightweight model) and inflates `Report.Elapsed` by `T - 20ms`;
  - if `Harness.Build` does not advance `s`, `Begin` fires at `t = 20ms` before SWIM has converged or objects exist.
  In addition, `RunOptions` offers no way to override `Bound`, `Settle`, or `Policy` for slower real-component steps, and `lostLimit()` / `FailMidCompensation` hardcode `MatrixPolicy().MaxAttempts`.
- **Reproduction:** Implement a `sagasim.Harness` whose `Build` calls `s.RunUntil(env.Time(25 * time.Second))` (simulating cluster bootstrap) before returning `(s, cl, nil)`, then execute `sagasim.Run(h, sagasim.Cells()[0], sagasim.RunOptions{})(1)`. `s.Err()` is `sim.ErrDeadline` without `cl.Begin` ever running.
- **Smallest fix:**
  1. Compute `start := s.Now().Add(matrixBegin)`, schedule `"begin"` at `start`, call `s.RunUntilConverged(start.Add(bound), done)`, and report `Elapsed: end.Sub(start)`.
  2. Add optional zero-defaulted `Bound time.Duration`, `Settle time.Duration`, and `Policy saga.Policy` fields to `RunOptions` (defaulting to `MatrixBound`, `MatrixSettle`, `MatrixPolicy()`), and use the effective `Policy.MaxAttempts` in `lostLimit()` and `FailMidCompensation`.
  3. Guard `if s == nil { s = sim.New(sim.Config{Seed: seed}) }` before `s.Fail(err)` on line 371, and gate `hook`/`injStep` on `p.Saga == matrixSaga` / `c.Key.Saga == matrixSaga` so other sagas on a multi-node cluster are untouched.

### 2. MAJOR — `sagasim.Store` fails to reload or copy buckets with more than 32 objects (`maxFields = 64`)
- **File & line:** `internal/saga/sagasim/store.go:45`, `115`, `134-152`, `166-216`
- **What is wrong:** `encodeObjs` flattens each `KV` into 2 fields (`Key`, `Val`) and calls `encodeFields`, while `decodeObjs` calls `decodeFields`, which rejects `n > maxFields` (`64`) with `ErrMalformed`. Neither `opPut` nor `encodeObjs` enforces this cap, so a store persists state with > 32 objects (`> 64` fields) without error, and then permanently fails `decodeState` on restart (`loaded` stays `false`, refusing all requests with `env.ErrIO`) and fails `opDump`/`copyFrom` with `ErrMalformed`.
  `TestMigrationUnderNemesisDST` currently uses `MatrixObjects = 8` + `nemesisWrites = 24` = **32 objects** (64 fields), sitting on the exact failure cliff with zero headroom.
- **Reproduction:** Call `sagasim.Build(1, sagasim.Options{..., Objects: 33, ...})` and either run a migration (`opCopy` fails with `sagasim: objects: sagasim: malformed message`) or crash and restart store `"a"` (`Store.load` fails `decodeState`, increments `loadErrs`, and never sets `loaded = true`). Same failure if `NemesisParams.Writes` is raised from 24 to 25 and 25 writes succeed before a copy or restart.
- **Smallest fix:** Encode/decode object lists with a dedicated length-prefixed `(count, key, val, ...)` codec in `encodeObjs`/`decodeObjs` (bounded by a generous `maxObjects` or byte size rather than the RPC `maxFields = 64` cap), and add a round-trip test in `sagasim_test.go` with > 32 objects.

### 3. NIT — `Engine.Begin` joins a spurious `env.ErrNotFound` when `Append` or `Sync` fails on `sim.Disk`
- **File & line:** `internal/saga/engine.go:250-257`
- **What is wrong:** After `Begin` verifies `Read(name)` returned `env.ErrNotFound`, a failure in `g.write(r, rec)` runs cleanup:
  ```go
  derr := g.e.Disk.Delete(name)
  if derr == nil {
      derr = g.e.Disk.Sync(name)
  }
  return errors.Join(fmt.Errorf("saga: begin %s: %w", id, err), derr)
  ```
  On `sim.Disk` (and a POSIX file disk):
  - if `Append` failed before creating the blob, `Delete(name)` returns `env.ErrNotFound` (`internal/sim/disk.go:148`);
  - if `Append` succeeded (`exists=true, dExists=false`) and `Sync` failed, `Delete(name)` clears `exists` and the following `Sync(name)` returns `env.ErrNotFound` (`internal/sim/disk.go:121`, `!b.exists && !b.dExists`).
  In both cases `errors.Join` attaches `env.ErrNotFound` to the returned error, so `errors.Is(err, env.ErrNotFound)` is unexpectedly true. `TestEngineEdges` missed this because `memDisk` in `helpers_test.go` never returns `env.ErrNotFound` from `Delete` or `Sync`.
- **Reproduction:** Wrap a `sim.Disk` handle so `Append` or `Sync` returns `env.ErrIO`, call `eng.Begin("w", testDef, nil)`: `errors.Is(err, env.ErrNotFound)` returns `true`.
- **Smallest fix:** Ignore `env.ErrNotFound` from the cleanup `Delete`/`Sync` (`if errors.Is(derr, env.ErrNotFound) { derr = nil }`).

### 4. NIT — `Resume` accepts `RecAbort` without a preceding `RecIntent` (`!s.Open`)
- **File & line:** `internal/saga/resume.go:98-102`
- **What is wrong:** `case RecAbort` validates `s.State`, `s.Step`, and `r.Step`, but does not check `!s.Open`. A log such as `[RecBegin, RecAbort(0)]` or `[RecBegin, RecIntent(0,1), RecDone(0,1), RecAbort(1)]` — aborting a step for which no intent was ever logged — is accepted instead of returning `ErrLogCorrupt`. The engine can never write `RecAbort` when `!s.Open`, so this leaves a small gap in the state machine's "accepts exactly the legal sequences" claim (`build-notes.md:243`).
- **Reproduction:**
  ```go
  _, err := saga.Resume([]saga.Record{
      {Kind: saga.RecBegin, Def: "test", Time: 5},
      {Kind: saga.RecAbort, Step: 0, Data: []byte("cause")},
  }, 4, 2, 3)
  // returns err == nil, StateCompensating instead of ErrLogCorrupt
  ```
- **Smallest fix:** Add `|| !s.Open` to the `if` condition at `internal/saga/resume.go:99` and add a corresponding row to `TestStateMachineTable`.

### 5. NIT — `Engine.call` aliases `r.snap.Input` and `r.snap.Outputs[i]` directly into `Call`
- **File & line:** `internal/saga/engine.go:387-395`
- **What is wrong:** `Engine.call` hands `r.snap.Input` directly and only shallow-clones `r.snap.Outputs` (`slices.Clone`). A step implementation that mutates `c.Input` or `c.Outputs[i]` in place corrupts the engine's in-memory `Snapshot`, making retries/later steps in the same incarnation diverge from what a restarted incarnation recovers from the durable log (`Engine.status` already deep-clones `Outputs` at lines 605–608).
- **Reproduction:** A step whose `Do` flips `c.Input[0] ^= 1` and returns a retryable error causes attempt 2 in the same incarnation to see the mutated bytes, while recovery after a crash sees the original durable `RecBegin.Data`.
- **Smallest fix:** Pass `bytes.Clone(r.snap.Input)` and deep-clone each slice element of `r.snap.Outputs` in `Engine.call`.

### 6. NIT — `MigrationInput.Validate()` accepts fields over `maxInputField` (1024 bytes) that `DecodeMigrationInput` rejects
- **File & line:** `internal/saga/migration.go:23`, `34-39`, `49-56`
- **What is wrong:** `MigrationInput.Validate()` checks non-empty fields and `From != To`, but not `len <= maxInputField`. A `MigrationInput` with a 1025-byte `Bucket`, `From`, or `To` passes `Validate()`, encodes with `Encode()`, and then fails permanently inside `migStep.Do` when `DecodeMigrationInput` rejects `n > maxInputField`.
- **Reproduction:** `in := saga.MigrationInput{Bucket: strings.Repeat("b", 1025), From: "a", To: "b"}` has `in.Validate() == nil`, while `saga.DecodeMigrationInput(in.Encode())` returns `env.ErrInvalidArgument`.
- **Smallest fix:** Check `len(m.Bucket) > maxInputField || len(m.From) > maxInputField || len(m.To) > maxInputField` in `MigrationInput.Validate()`.

### 7. NIT — `TestPropTerminalState` never reaches retry exhaustion or post-pivot `ErrPermanent`, and `scenario.slow` is unused
- **File & line:** `internal/saga/prop_test.go:19`, `42`, `54`, `57-61`, `94-110`
- **What is wrong:** `drawScenario` draws `sc.fails[i] <= 2` and at most 4 crashes (`hooks <= 2`, `timed <= 2`), so a step can reach at most 7 attempts against `propMaxAttempts = 10`; pre-pivot retry exhaustion can never occur in `TestPropTerminalState`. Likewise `sc.perm[i]` is gated on `i <= sc.pivot`, so post-pivot `ErrPermanent` under random crashes is never drawn. Separately, `scenario.slow []bool` is allocated (line 54) and never drawn or read. (Unit tests and DST matrix/nemesis tests do cover both behaviors, so no engine behavior goes untested.)
- **Reproduction:** By inspection of `drawScenario` (`2 fails + 4 crashes + 1 = 7 < 10`) and `sc.perm[i] = i <= sc.pivot && ...`.
- **Smallest fix:** Remove the unused `slow` field; optionally draw a bounded number of post-pivot `ErrPermanent` responses in `drawScenario` (with `sc.expect()` still asserting `StateCompleted`).

### 8. NIT — `sagasim.Run` does not assert non-crash fault injection fired, and `NemesisRun`/`DuelRun` panic on nil report pointers
- **File & line:** `internal/saga/sagasim/matrix.go:409-418`, `internal/saga/sagasim/scenarios.go:53`, `151`
- **What is wrong:**
  1. `cellState.check` verifies `if st.cell.Kind.crash() && !st.fired`, but does not check `!st.cell.Kind.crash() && !st.failFired`, `st.cell.Torn && !st.cell.Kind.crash() && !st.tornDone`, or `st.cell.Double && st.crashes < 2`. If a Chunk 6 `Harness` accidentally omits `o.Wrap`, post-pivot failure cells expecting `StateCompleted` pass silently without injecting any fault.
  2. `NemesisRun(p, nil)` and `DuelRun(p, nil)` unconditionally dereference `*res = ...` (unlike `Run`, which checks `if o.Report != nil`).
- **Reproduction:** `sagasim.NemesisRun(sagasim.NemesisParams{}, nil)(1)` panics at `scenarios.go:53`; a `Harness` that ignores `BuildOptions.Wrap` passes `delete-source-fail-before-action-untorn` in `sagasim.Run`.
- **Smallest fix:** Add the three fault-firing checks to `cellState.check`, and guard `if res == nil` with a local report inside the returned closures of `NemesisRun` and `DuelRun`.

### 9. NOTE — `MigrationOps` idempotency key scope in `StepDeleteSource` (for Chunk 6 and prose)
- **File & line:** `internal/saga/migration.go:189-256`
- **Detail:** `finish` (step 4, `delete-source`) invokes `ops.Discard`, `ops.Unclaim`, `ops.Copy`, and `ops.Root` with `c.Key = {Saga: id, Step: 4}`, whereas step 0 (`Claim`/`Unclaim`) passes `Step: 0` and step 1 (`Copy`/`Discard`) passes `Step: 1`. `sagasim.Ops` handles this by keying bucket ownership/registration on `c.Key.Saga`. Chunk 6's real `MigrationOps` implementation must likewise key ownership/claim/discard/seal/retire checks on `c.Key.Saga` (not `c.Key.String()`), while ensuring any idempotency cache for `Copy` distinguishes step 1 from step 4's catch-up copy (e.g., via `c.Key`).

## Verification

### Verified by running (inside `dev` Docker container)
- `docker compose run --rm -T dev go test -v -cover ./internal/saga/...` — PASS (`internal/saga` 95.2%, `internal/saga/sagasim` 87.6%; all named plan tests, properties, fuzz seed corpus, canary, and DST tests ran and passed).
- `docker compose run --rm -T dev go test -race -count=1 ./internal/saga/...` — PASS (`internal/saga` 5.5s, `internal/saga/sagasim` 1.3s).
- `docker compose run --rm -T dev go test -count=1 ./internal/saga -run 'Prop' -rapid.checks=2000` — PASS (1.4s).
- `docker compose run --rm -T -e DST_SEEDS=50 dev go test -count=1 ./internal/saga/...` — PASS (`internal/saga` 6.1s, `internal/saga/sagasim` 0.06s).
- `docker compose run --rm -T dev go list -f '{{.ImportPath}}: {{.Imports}}' ./internal/saga ./internal/saga/sagasim` — confirmed standard library + internal imports only (no `rapid` or external packages in product code).
- `git diff --stat main..HEAD` and `git diff main..HEAD -- internal/env internal/sim go.mod go.sum agent_work/gates` — confirmed frozen simulator packages, `go.mod`, `go.sum`, and `agent_work/gates/` are untouched.
- Pattern scan across `internal/saga/...` — confirmed zero comments, zero `panic(`, zero goroutines/channels/mutexes/wall-clock/global-rand/floats.

### Verified by reading
- Full line-by-line review of all product and test files in `internal/saga/` and `internal/saga/sagasim/`, `internal/saga/testdata/seeds.txt`, `agent_work/05-saga/build-notes.md`, `agent_work/05-saga/gates/result.json`, `agent_work/05-saga/objstore-p2-05-saga-build.md`, `agent_work/05-saga/objstore-p2-builder-rules.md`, `agent_work/0b-simulator/build-notes.md`, `agent_work/0b-simulator/frozen-api.txt`, `internal/sim/disk.go`, `internal/sim/sim.go`, and `PLAN.md` sections 2, 4.2, 5.1, 7.1, 7.8, and 9.1.
- Gate result in `agent_work/05-saga/gates/result.json` (G0–G6 `overall=pass`, `counts_for_landing=true` at `cac1008aa54dba532eac6e28dc7fb5b3215584cf`); did not re-run 30 s fuzzing or 500-seed sweep beyond the 50-seed sweep above.
