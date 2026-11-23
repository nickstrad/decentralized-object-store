#!/bin/bash
# The fast lane: what push-triggered CI runs, and a quick local check before a push.
#
# Usage: agent_work/gates/ci-fast.sh
#
# Runs, inside the dev container, through gate.sh:
#   G0  build, gofmt, vet, determinism lint, panic allowlist, dependency and
#       self-containment checks
#   G1  every unit test once with the coverage floors. Simulation and end-to-end tests run
#       their pinned regression seeds plus the default sweep of three fixed seeds
#       (seeds 1-3), which is the fixed-seed smoke.
#   G6  proof that the run happened inside the dev container
# Target: a few minutes. Nothing here has a time budget or a seed count to grow.
#
# Deliberately NOT here (owner ruling 2026-11-22: CI runs quick tests only; long runs are
# on-demand events, not the cost of every push). All of it stays runnable by hand:
#   full landing gate (G0-G7: 2,000-check properties, fuzz, race, 500-seed sweep, cluster):
#       GATE_STAGE=final agent_work/gates/gate.sh <chunk>
#   one gate of it, for example the 500-seed sweep or the race run:
#       GATE_ONLY=G5 agent_work/gates/gate.sh <chunk>
#   fuzz every target for a chosen budget:
#       docker compose run --rm -T -e FUZZ_SECONDS=300 dev agent_work/gates/fuzz-short.sh
#   a seed sweep of any size:
#       docker compose run --rm -T -e DST_SEEDS=5000 -e DST_SEED_BASE=1 dev go test ./...
#   the gate canaries (the tester's own tests; hours for the full set):
#       agent_work/gates/canaries.sh            (CANARY_FAST=1 for the G0/G6 subset)
#
# The result is written to agent_work/ci/gates/result.json and is marked partial: a
# fast-lane run never counts for landing. The landing authority is the full gate.
#
# Exit status: as gate.sh (0 green, 1 a check is red, 2 usage or setup error).
set -u
here=$(cd "$(dirname "$0")" && pwd -P)
unset DST_SEED DST_SEEDS DST_SEED_BASE
GATE_ONLY=G0,G1,G6 GATE_STAGE=final exec "$here/gate.sh" ci
