# Independent check handoff

Scope: ../../07-e2e/objstore-p2-07-e2e-adversarial.md. Only this directory may be
written; no git writes or commits. Product HEAD fd6dc9612520010a9610873410c58495b7d51637.

Status: finalized nine-driver suite running. Do not edit executing scripts.

Confirmed target harness defects: C7 silently accepts unexpected GET/tag/delete errors;
C10 constructor accepts a failed real node launch. Four focused checks fail intentionally
until those defects are fixed. ATTACKS.md contains exact reproductions.

Initial workload evidence: 18 simulator scenarios, 78 matrix cells, 9 custom schedules,
30-wave soak, real fault cases, ENOSPC/restart and cleanup ran. Initial harness issues
and corrections are documented; input and late-restart corrections have passed focused
confirmation. Final real workloads have one PUT slot and reject partial fixtures.

Next: wait for final run; read evidence/final-run.txt and final-run.stderr, inspect every
BROKEN/setup error, update ATTACKS.md with exact totals and measurements, verify no tracked
product diff, then hand off. Leave real findings failing. evidence/run-all.txt is the
final aggregate. Tool execution owns the run (not detached); if unavailable, rerun
bash agent_work/adversarial/07-e2e/run-all.sh from the worktree root.
