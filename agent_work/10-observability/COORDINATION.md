# Stage C resumption coordination

Base f711468; target feat/observability. Frozen metrics interfaces/catalogue stay unchanged.
Permissions: branch-local builds, tests, commits. No push, main merge or publication.

| Unit | Claim / worktree | Expected output | Checkpoint |
|---|---|---|---|
| O1 | o1_components / 10-obs-resume-components | component sources + codec hooks | building |
| O2 | o2_runtime / 10-obs-resume-runtime | runtime collector, HTTP metrics, objnode config | integrated; exact-dependency checks/review await O1 |
| O3/O4 | integrated on feat/observability | Prometheus, CSV, DuckDB queries/smoke | implementation done; fresh cross-company confirm PASS, real-node smoke pending |
| A2 | root / 10-obs-resume-proof | real Docker proof + CI hook | prep 7954bdb + 8f25539 on target; live awaits O1/O2 |

Fresh cross-company A2 prep review: Sonnet NITS, closed/recorded in A2-REVIEW.md.
CLI exit 0 and expected review output both verified; reviewer worktree removed.
Review report/log/exit: /root/runs/objstore-obs-review-proof.{txt,log,exit}.
Each remaining logical unit gets its own fresh independent reviewer after build commits.
Native agents report commits; their retained worktree remains until integration is verified.

Acceptance: required build/vet/test fast lane; focused race tests; real objnode OTel proof;
CSV exact readback + real objnode CSV segments queried by pinned DuckDB in Docker.
No READY claim: Neon's second final pass owns phase readiness.

Adapter/HTTP fresh confirm verdict PASS; reports archived under reviews/. CSV precision fixture rerun on integrated tree PASS. Completed adapter and review worktrees removed. Original interrupted worktrees remain untouched.

Live proof checkpoint: A2 PASS (1→11 HTTP health counter, gauge, boot label count 0); real node CSV smoke PASS (three sequences/all ten components/positive health/gauge), pinned precision fixture PASS. Product revision 88e9b08. O1/O2 independent reviews running; host full lane and Docker focused race in progress.
