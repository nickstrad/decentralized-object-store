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

## Re-run after the fix round (orchestrator, 2026-11-23, branch tip `8c22979`)

`bash agent_work/adversarial/07-e2e-opus/run-all.sh`:
`SUMMARY held=80 broken=1 setup_errors=3 wall=1706s` (`evidence/run-all.txt`, this run).

| Finding | Lines | Now |
|---|---|---|
| F1 | `a02-coordinator-paused-past-orphan-grace-loses-object`, `a02-sched-lost-reply-then-pause-past-heal-bound` | HELD (every acknowledged object kept on seeds 31337, 3, 4; both cycles terminal on 702, 9001, 31337) |
| F2 | `a05-replay-shard=*` | HELD (every printed REPLAY line reproduces its planted failure) |
| F3 | no line of its own | closed in review round 3 (the commit-after-delete oracle now names the event) |
| F4 | `a01-purge-noop-S`, `a01-purge-noop-R` | SETUP ERROR: the patch no longer applies, because the fix rewrote `Retire`. Hand-ported by the orchestrator in a scratch copy (`g.purge(m.Bucket, nil, ...)` in `Retire`): KILLED on the simulator tier by `TestE4MigrationUnderTrafficS` (`30 pre-migration shards of e4 remain outside r1 immediately after migration completed`) |
| F5 | `a04-oracle-loss-declared-early` | HELD (killed by the loss oracle through the new absence scenario) |
| N2 | `a01-replaced-never-dead` | SETUP ERROR, same cause. Hand-ported (the `markDead` of the replaced version removed from `Retire`): KILLED by `TestE4MigrationUnderTrafficS` |
| N7 | `a03-S-stale-read-split` | still the one BROKEN line; no claim broken (E2 does not promise read-your-writes during a partition) |

The three patch files are left as the adversarial seat wrote them; they describe the code
at `796e127`.
