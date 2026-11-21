#!/usr/bin/env bash
# Run from the worktree root; fixtures are created and removed under /tmp.
docker compose run --rm -T dev python3 agent_work/adversarial/0a-foundation/harness.py provenance
rc=$?
if [ "$rc" -gt 1 ]; then exit 2; fi
exit "$rc"
