# OCC check harness state

Goal/spec: `../../04-occ/objstore-p2-04-occ-adversarial.md`.
Status: COMPLETE — final replay: 59 HELD, 2 BROKEN, 0 setup errors, 51 seconds, exit 1.
Branch/worktree: existing `chunk/04-occ`, current directory. No commits authorized or made.
Done: one nested module, two independent drivers, one existing-regression driver,
34-claim inventory, two minimized recount counterexamples and evidence.
Last completed full replay: 59 HELD, 2 BROKEN, 0 setup errors.
Next: builder/orchestrator reviews the two reproductions in ATTACKS.md; no harness work remains.
Blockers: none. Product fixes belong to the builder/orchestrator and are outside this brief.
Resume: run `bash agent_work/adversarial/04-occ/run-all.sh` from the worktree root;
expect exit 1 for the two documented findings. There are no detached harness runs.
