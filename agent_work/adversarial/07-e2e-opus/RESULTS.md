# Chunk 7 adversarial pass (Opus) — results as accepted by the orchestrator

Run by the adversarial seat itself (its third full run, 2026-11-23 06:40–07:08 UTC, branch tip
`796e127`): `SUMMARY held=74 broken=9 setup_errors=0 wall=1662s` (`evidence/run-all.txt`).
The orchestrator did not re-run the whole set before the fix round; it reproduced F1
independently (scratch copy of the same tip, seeds 31337, 3 and 4 all fail with the loss
declaration) and re-runs `run-all.sh` after the fixes.

| Finding | Grade | Orchestrator's decision |
|---|---|---|
| F1 a migration whose coordinator stalls longer than the orphan grace mid-copy completes and loses acknowledged objects | MAJOR, product | ACCEPTED. Gates the landing. To be fixed in the product, not stated as a limit: the coordinator returns, and acknowledged data is dropped without a fault on the data path |
| F2 the printed REPLAY command does not replay a seed outside shard 0 | MAJOR, suite | ACCEPTED. Gates the landing |
| F3 migration manifest swaps bypass the oracle's commit sink | MINOR | ACCEPTED, same fix round |
| F4 a purge that does nothing passes the simulator tier | MINOR | ACCEPTED, same fix round |
| F5 the loss-declaration oracle is never exercised by a scenario | MINOR | ACCEPTED, same fix round |
| N1–N7 | notes | No action. N7 (a stale read while split survives) breaks no stated claim: E2 does not promise read-your-writes during a partition |

The three coverage holes of the first review (the reason for this pass) all held under the
seat's own mutations: 10 of 11 mutations of the fuzzed migrating bucket killed (the eleventh
is F4), the forgotten ambiguous start killed, and every partition-time mutation killed on
the tier that can see it. Fix round: `agent_work/07-e2e/objstore-p2-07-e2e-advfix.md`.
