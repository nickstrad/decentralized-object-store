# Adversarial Chunk 1 state

Goal/spec: execute `../../01-gossip/objstore-p2-01-gossip-adversarial.md` against the Chunk 1 claims and PLAN.md 4.2 attack list.

Status: harness implemented; final rerun and report being completed.

Product revision inspected: `6bccb38250e177622c7438e240e871f0944197b6`.
No product edits, git writes, commits or changes to root STATE.md are authorized by this brief.
All task artifacts live in this directory, in one nested Go module.

Done: exported-API attacks, simulator schedules, N=20/50/100/200 scaling, upstream test-only canary adapter. Initial executions found retention-related limits on monotonicity, stale resurrection and seedless partition recovery; other executed attacks held.

In progress: minimize the long-partition case to two nodes and record final evidence.
Next: run `bash agent_work/adversarial/01-gossip/run-all.sh`; finalize ATTACKS.md with actual counts.
Blockers: none. No publication or product fixes are part of this task.
Resume: current worktree/branch, no detached runs; invoke the driver above from the worktree root. Each Docker invocation cleans its temporary build directory. Exit 1 is expected while the recorded broken claims remain.
