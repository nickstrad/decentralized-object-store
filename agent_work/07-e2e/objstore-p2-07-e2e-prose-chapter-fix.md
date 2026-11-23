# Brief — Chunk 7 PROSE: corrections to the chapter "End-to-End Tests and Hardening"

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree; it is your current
directory. You edit ONE file, `book/chapters/08-end-to-end-and-hardening.md` (a draft that
exists, not yet committed), and its diagram sources `book/chapters/diagrams/08-*.mmd` if a
correction touches a diagram. Touch nothing else. Do NOT run any git write command.

The draft was checked line by line against the code and the notes. Its excerpts, links,
named tests, commands and the migration guarantee are right. The statements below are
wrong or stronger than the evidence. Fix each one, checking the evidence yourself; change
nothing else of substance. The original brief,
`agent_work/07-e2e/objstore-p2-07-e2e-prose-chapter.md`, still binds (rules, shape).

## Corrections (line numbers are the draft's)

1. Line 5, "...so every check fails when its guarantee breaks." Too strong: the mutation
   work found checks that survived. Say instead that the suite itself is attacked to find
   checks that still pass when their guarantee is broken, and each one found is repaired.
2. Line 64 and the diagram edge to `RequireDeterministic`: "Every run passes through
   `simtest.RequireDeterministic`" is false for the fuzz target (`test/e2e/e7_test.go`:
   `FuzzClusterNemesis` ends with `s.Verify()`; build-notes section 12 lists what bypasses
   it). Say: every scenario run on the simulator tier passes through it; fuzz inputs end
   with `s.Verify()`. Correct the diagram label to match and re-render.
3. Line 71, the six oracles "run at every Tier S step": only the ledger, deletion, loss and
   spread oracles fire on every event; `Lineage` is checked at every heal and at the end;
   `Converged`, `checkLiveReadableOrLost` and `checkOrphans` after a heal or after the
   grace periods (build-notes section 3).
4. Line 252, E7 row: "301 seeds, 1,204 heals" → the notes say 300 seeds and 1,200 heals for
   the faults-per-seed and convergence figures; 301 seeds belongs only to the availability
   and `dest_*`/`moved_*` figures. Give each figure with its own seed count.
5. Line 247, E2 row: the detection, convergence and discard figures are from 125 seeds; 126
   seeds belongs only to the later successful-GETs figure.
6. Line 135: `opVerifyShards` is defined and dispatched in `internal/node/meta.go`; name
   that file with the other two.
7. Line 251, E6 "Not shown": it is SHARD catch-up on the real-process tier that is checked
   only against the shards a node held before (manifests cannot be read over HTTP).
8. Line 79: wire code 96 is not found (HTTP 404); 97 is corrupt, not a 404. And
   `ErrUnavailable` was moved ahead of the per-shard causes in the table, not "first".
9. Lines 80 and 139: there is no `Verify` method; write "the verify step" without code font
   (the methods are `Copy`, `Root`, `Flip`, `Retire`).
10. Line 80: the adversarial run paused the coordinator for 70 s starting 100 ms after the
    migration started, during the copy — not "right after destination shards landed".
11. Line 73: "Each cycle injects 2–5 faults" is true of the E7 scenario's random plan only;
    the fuzz target builds its plan from its input (up to 3 cycles and 8 operations).
12. Line 149: the sweep floors apply only from 10 seeds up; `checkE7AvailabilityFloor`
    belongs to the availability check, not to the seed-102 repair. Reword so neither is
    misattributed.
13. Line 161: "from 351 s to 73–83 s" → "from 351 s to 73 s".
14. Line 256: "nine DST packages" followed by eight names — either "eight", or name the
    ninth (`internal/saga/sagasim`) and say its tests do not sweep seeds.
15. Line 65: `Pdeathsig` and the named test prevent leaked PROCESSES (temporary
    directories can remain); say "leaked processes".
16. Line 257: "C7" and "C10" are labels a reader has never seen; describe the two findings
    in words (the nemesis settle step accepted any error; the real cluster accepted a node
    that failed to start).
17. Lines 242 and 263, "4-core": describe the limit without describing a machine ("on a
    small machine" or the write concurrency itself).

## One update

18. Line 265 (and wherever else it appears) says two mutation tests are still missing.
    They now exist: read build-notes section 17. State it as closed: the threshold and the
    retire-time re-check each have tests on three seeds
    (`TestMigrationVerifyBelowKCompensates`, `TestMigrationVerifyBelowQuorumWaitsForRebuild`,
    `TestMigrationRetireBelowQuorumWaitsForRebuild`,
    `TestMigrationRetireBelowKRecopiesAndNeverGoesLive`), and lowering the threshold or
    removing the re-check now fails them. One sentence; add them to "Go deeper" if the
    migration tests are listed there. You may add ONE sentence, where the chapter talks
    about the real-process tier, that tests which bind real ports reserve them and retry
    the whole cluster on fresh ports when a reserved port is taken (section 17, R3-3).

## Length

The draft is 3,443 words by `wc -w`; the brief asked for 1,800–2,800. Bring it to at most
3,000 by cutting repetition and compressed lists a reader cannot use — never by dropping a
narrowed claim, a limit, the two product defects, the guarantee or a "not shown" entry.

## Check

- If a `.mmd` changed: `python3 /root/.claude/skills/draw-visual/scripts/render.py --update book/chapters/08-end-to-end-and-hardening.md`
  then `DRAW_VISUAL_MAX_WIDTH=80 python3 /root/.claude/skills/draw-visual/scripts/render.py --check book/chapters/08-end-to-end-and-hardening.md`.
- `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` — only the
  expected `chapter-unlisted` finding.
- Final message: one line per correction (1–18): done, and the new wording in a few words.
