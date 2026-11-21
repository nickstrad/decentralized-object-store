VERDICT: FAIL

The chunk may not land at e0409b54b04248a874b6fabf5c153164415437f3. The landing-result validation is repaired, but the comment baseline can certify itself, first-push CI can skip unauthorized prose, and the outer-variable map-range repair misses empty loop bodies. The supported direct-container gate invocation also still rejects already-landed comments. Close A–D below and rerun their positive and negative controls before landing.

| REVIEW-2 new finding | Status | Evidence |
|---|---|---|
| 1 — landing result binding | FIXED | Exercised the actual `land.sh` in Docker against temporary git histories: `counts_for_landing=false`, `sha=unknown`, nonexistent 40-hex SHA, a real foreign commit, and an injected `git diff` failure all exited 1 before merge/push. A valid result reached both operations; an arbitration PASS correctly superseded REVIEW-2 FAIL. |
| 2 — comments from earlier chunks | PARTLY FIXED | A commented `main` plus a correct, tested branch passed full build-stage G0–G6 with `counts_for_landing=true`; filtered `clean`, `landed-prose`, `comment`, `coverage` canaries passed 4/4; explicit-base prose checks passed. Newly added premature comments were rejected. However, ordinary on-main invocation self-baselines (A), and direct-container invocation has no baseline (B). |
| 3 — coverage canary scaling | FIXED | `canaries.sh '^coverage$'` caught the under-85% violation with an existing simulator function containing 600 executed `x++` statements and its passing test. The repair scales the uncovered body with existing Go lines; normal gofmt-formatted packages no longer dilute the planted violation. |
| 4 — CI prose provenance | PARTLY FIXED | Valid direct build/prose commits and a rebased `--no-ff` landing exited 0; a direct unauthorized comment and an evil merge exited 1. A first push containing the unauthorized comment followed by an operational commit exited 0 (C). |
| 5 — outer map variable / repeated site self-test | PARTLY FIXED | The reviewer's append-and-sort `for last = range m` fixture now exits 1 with `maprange`; the empty-body version exits 0 (D). Retaining repeated site self-tests is acceptable: it costs runtime but does not weaken the gate or require a landing condition. |

A. **BLOCKER — the default comment baseline can be the gated commit itself.** `agent_work/gates/gate.sh:52–57` automatically selects `git merge-base HEAD main`. On `main`, this is HEAD. In a temporary repo with already-landed simulator comments, I committed an unauthorized comment directly in `internal/buildcheck/buildcheck.go`, then ran the ordinary host entry point with `GATE_STAGE=build`, without baseline or evidence overrides. All G0–G6 passed. The result contained:

```json
{"sha":"beafc2110395f39688b812b0156d807eceab5f63",
 "comment_baseline":"beafc2110395f39688b812b0156d807eceab5f63",
 "stage":"build","dirty_tree":false,"overall":"pass",
 "counts_for_landing":true}
```

This is an honest invocation, not forged evidence. No comparison with HEAD's own comments can establish their provenance. Close by defining a trusted baseline for build runs and refusing ambiguous on-main/self-baselining invocations unless that baseline is explicitly established. Do not silently convert “no trustworthy earlier baseline” into a counted pass. Preserve the green control for a proper chunk branch based on previously landed prose. Evidence: `good-main-self-baseline.log` and `.json`.

B. **MAJOR — direct-container builds still reject correct branches.** The new baseline preparation exists only in the host branch of `gate.sh`. On the same correct branch that passed the host entry point, this command failed solely at G0/comments on the earlier simulator comment:

```sh
docker compose run --rm -T -e GATE_ONLY=G0 dev agent_work/gates/gate.sh arbitration
```

`GATE_ONLY` isolates the check; it does not change baseline selection. `gate.sh:167–176` falls back to checking all comments because `GATE_BASELINE` was never supplied. The script explicitly supports being called inside the container. Close by giving that supported entry point the same trusted-baseline behavior, or by explicitly rejecting an unsupported invocation as a setup error and routing the documented workflow through a wrapper that supplies the baseline. Do not report previously accepted prose as a new comment violation. Evidence: `good-container.log` versus `good-branch.log`.

C. **MAJOR — first-push/unknown-base fallback checks only the last commit.** `agent_work/gates/ci-prose.sh:20–22` replaces an all-zero, missing, unknown, or non-ancestor before SHA with `head^`. Reproduction in a temporary history:

1. Start from the commented base.
2. Commit an unauthorized Go comment without the prose trailer.
3. Commit only an operational `STATE.md` update.
4. Run `ci-prose.sh 0000000000000000000000000000000000000000 HEAD`.

It exits 0, checking only step 3; the comment is now in that check's baseline. Supplying the actual base checks both commits and exits 1 on step 2. A root-only first push also exits 0 with “has no parent, nothing to check.” Close by checking the entire newly introduced history from an explicit trusted bootstrap boundary (with an explicit root policy), or failing closed when the boundary cannot be established. Unknown/force-push history must not silently become a one-commit check. Evidence: `history-first-push.log`, `history-full-push.log`, `history-root-first-push.log`.

D. **MAJOR — empty map loops still expose the nondeterministic last key.** `agent_work/gates/detlint/detlint.go:564–573` returns true for an empty body before checking `rs.Tok`. This product function passes the actual lint:

```go
func Last(m map[string]int) string {
    var last string
    for last = range m {
    }
    return last
}
```

The empty body does not erase the loop's assignment side effect. Close by rejecting assignment to an outer iteration variable before the empty-body exemption, with a regression for this exact shape. Preserve safe empty loops with locally declared or absent iteration variables. This is the same semantic defect as New Finding 5, not a runtime nit. Evidence: `map.log` (empty RC=0, append-and-sort RC=1) and `empty-loop-g0.log` (every G0 check passes with this function in `internal/sim`; the partial run correctly does not count for landing).

A and C are unsafe paths in mechanisms added by e0409b5. B is the unclosed container path of the original baseline defect; D is an unclosed variant of the original outer-variable defect, rather than a regression newly created by the repair. I found no further independent BLOCKER or MAJOR in the changed code.

The nine adversarial breaks grouped as N1–N7 are acceptable **as narrowed**, not as proof that every violating tree is rejected:

| Narrowing | Decision |
|---|---|
| N1 — two test-only entropy attacks | Accept the product-code lint boundary. Future replay/seed tests remain responsible for test-side effects; this foundation review does not establish that every possible test-side nondeterminism is detected. |
| N2 — standalone helper-subpackage lint | Accept: the bare tool checks supplied directories; the gate computes the in-module dependency closure. |
| N3 — testdata comments | Accept the fixture-comment exclusion as the stated boundary of the comment rule. |
| N4 — two embedded-help attacks | Accept: changing designated CLI help is authorized prose under OQ-8; arbitrary embedded payload edits are not thereby authorized. |
| N5 — deleting a comment with code | Accept the narrowed repair operation: removing obsolete prose does not introduce unauthorized prose. It does not permit adding or rewriting comments. |
| N6 — stale result after a usage error | Accept: the repaired consumer requires a counted result bound to a real ancestor and rejects subsequent non-exempt changes. The stale file's existence alone is insufficient. |
| N7 — developer-only fuzz filter | Accept: the landing gate unsets `FUZZ_ONLY`; the standalone developer filter is outside the landing guarantee. |

Those scope decisions do not excuse A–D, and the final blanket sentence in `RESULTS.md` cannot be used as landing evidence in light of these counterexamples.

Verified by running:

- The current tree's full `gate.sh 0a-foundation`, with output redirected to container `/tmp`: every G0–G6 check passed; `counts_for_landing=false` because the worktree has untracked review/adversarial files.
- Full build-stage gates on the tested valid branch and the self-baselining main fixture; targeted G0 on a branch with a premature comment; the direct-container positive-tree counterexample.
- Four filtered canaries on the tested commented-base fixture, and coverage alone on the 600-statement simulator fixture. No additional full canary campaign was run.
- Explicit-base prose checks, including rejection of a builder comment present before a later legitimate prose commit (`preprose-reject.log`); direct-push and merge CI checks; the first-push controls; and landing-consumer rejection cases described above.
- `docker compose run --rm -T dev bash -c 'cd agent_work/gates/detlint && go test ./...'`: all five tool packages passed. The separate map-range counterexample still passed the lint.

Verified by reading: both review reports; `git show e0409b5` and repair history; PLAN 2.9–2.10, Chunk 0, 5.1–5.3 and 6; the affected scripts, CI configuration, comment checker and map-range logic; adversarial `ATTACKS.md` and `RESULTS.md`. The nine historical adversarial cases were assessed from their recorded evidence and current control flow, not rerun as a full suite.

Fixtures, scripts and raw logs are under `/tmp/obj-arbitration.vOshP5/`. `good.py` provides the tested branch/canary controls; `history.py` provides isolated provenance controls; `land.py` exercises actual landing validation while a git wrapper intercepts merge/push (and injects the requested diff failure). No real merge or push was performed. Initial `run.py` controls with an untested gossip constant and a gate-result file accidentally included in a prose commit were corrected in `good.py`/`history.py`; their expected fixture failures are not findings. This report is the only file I created or changed in the requested worktree; no source, state, or git write was made there.
