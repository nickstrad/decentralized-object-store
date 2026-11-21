#!/usr/bin/env bash
docker compose run --rm -T dev python3 agent_work/adversarial/0a-foundation/harness.py depth
rc=$?
if [ "$rc" -gt 1 ]; then exit 2; fi
exit "$rc"
