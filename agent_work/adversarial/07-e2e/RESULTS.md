# Results — adversarial work on the end-to-end suite and hardening

Two bodies of work exist for this chunk.

1. **This directory: the first independent harness (stopped early, does not gate).** It
   was stopped when the owner swapped the seats for this chunk's finish. Its partial output
   is kept as a record: `ATTACKS.md`, `STATE.md`, the drivers `a01`–`a09` and `evidence/`.
   Two findings were taken from it and closed in the finish (build-notes section 12): the
   nemesis `settle` step accepted any error on GET, tag and delete; `newRealCluster`
   accepted a node that failed to start. It was not re-run.

2. **`agent_work/adversarial/07-e2e-opus/`: the adversarial pass that gates this chunk.**
   Plan in its `ATTACKS.md`, results in its `RESULTS.md`, drivers `a01`–`a05`, `run-all.sh`.
   - First run: 74 held, 9 broken — two majors (F1, a product defect: a migration whose
     coordinator was stalled past the orphan grace period right after copying completed and
     lost acknowledged objects; F2: printed replay commands did not replay seeds outside
     the first shard) and three minors (F3–F5). All accepted and fixed (build-notes
     section 15; product fix `eaa0556`).
   - Re-run by the orchestrator on the fixed tree: **80 held, 1 broken**, and the one
     broken check has no stated claim behind it (a stale read during a partition, which
     nothing forbids). Three driver checks could not apply their patches to the rewritten
     code (setup errors); the two mutants behind them were ported by hand and both were
     killed.
   - After that re-run, two mutants were still known to survive (the shard threshold
     lowered to one; the check at retire time removed). Both are now killed by tests
     (build-notes section 17; `agent_work/07-e2e/FABLE-REVIEW.md`, round 4).

No blocker or major is left unaddressed.
