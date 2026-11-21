# Adversarial handoff — chunk 0b

Goal: execute the [adversarial brief](../../0b-simulator/objstore-p2-0b-adversarial.md)
against the deterministic simulator, preserving every product file.

Status: complete; executable attacks and captured results delivered, with failures
left for the builder/orchestrator to address. No commit was requested or made.

Target commit: `d8f7a0ab4283fb66cfc06b186eb72bb5ac867300` on `chunk/0b-simulator`.
Results and exact reproductions: [ATTACKS.md](ATTACKS.md).
Latest executed totals: [evidence/run-all.txt](evidence/run-all.txt).

Done: one nested Go module; custom protocol; replay across processes/runtime settings;
statistical network tests; topology/release tests; disk/budget sequences; PRNG tests;
loop/panic/overflow probes; actual simtest replay; copied production CLI failure probe;
seven standalone drivers and aggregate runner. Initial full run: 49 held / 6 broken.
Expanded final campaign: 52 held / 8 broken, no setup errors.

In progress: none after handoff. No detached agents or background chains are needed.
All changes are beneath this directory. Root STATE.md was read but was not edited:
the brief explicitly forbids edits elsewhere and git takes precedence over its stale
chunk status.

Next steps for the builder/orchestrator:

1. Triage the eight failed attacks (seven underlying issues; the two read-counter
   overflow probes share a cause). Exact API sequences are in ATTACKS.md.
2. Decide whether the nemesis-independence claim should be narrowed to fixed inventories
   or the design should change. This is a scope decision, not same-seed nondeterminism.
3. Repair product issues in an authorized builder task, then rerun this suite and
   preserve new evidence alongside the findings. No repairs are part of this brief.

Resume from this worktree root using:
`bash agent_work/adversarial/0b-simulator/run-all.sh`.
Exit 1 is expected on the tested target. Exit 2 means setup failed; inspect
`evidence/c*.stderr` and the reporting subprocess artifacts. Each driver is synchronous
and cleans its own temporary binaries/copy. This task does not launch a background run.
