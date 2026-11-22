# Chunk 2 adversarial handoff

Goal: execute [the brief](../../02-erasure/objstore-p2-02-erasure-adversarial.md).
Status: COMPLETE; final Docker run returned 41 held, 1 broken, 0 setup errors in 25 seconds.

Base checkout: `265dc9c6359dedbbebc9f7ab29eaaaa8f452db6a`, branch `chunk/02-erasure`.
No commits, git write commands or changes outside this adversarial directory.

Done: one nested module, five executable attack drivers, aggregate runner, independent
polynomial/Cauchy/elimination oracle, simulator fault campaigns, complete claims mapping,
minimal reproduction and evidence. See [ATTACKS.md](ATTACKS.md) and
[evidence/run-all.txt](evidence/run-all.txt).

Finding: C30's literal outstanding-stripe bound fails. Window=1 with one paused holder
leaves 2/8/64 outstanding stripes at acknowledgment. Minimal tested input is 5 bytes,
K=4,M=2,StripeSize=4, seed=220211, fixed 1-us network latency. This concerns the resource
bound, not data correctness; wording may be narrowed to stripes awaiting quorum.

Next: orchestrator/builder adjudicates C30 and existing REVIEW-1 repairs, then reruns
`agent_work/adversarial/02-erasure/run-all.sh` from this worktree's root. An isolated
reproduction is `bash agent_work/adversarial/02-erasure/driver.sh node window-pending-stripes`.
No background runs remain. All execution is foreground Docker; there is nothing detached
to resume. Root STATE.md is intentionally untouched under the brief's write restriction.
