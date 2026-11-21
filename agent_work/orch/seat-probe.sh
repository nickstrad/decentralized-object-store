#!/bin/bash
# Are the non-Claude seats reachable right now? One tiny call per route; never prints secrets.
# Usage: agent_work/orch/seat-probe.sh [neon|astra|all]
# Neon: direct route (google/gemini-4-neon) is PRIMARY, OpenRouter is FALLBACK until 2026-12-01.
# Both CLIs hang or return nothing when stdin is left open, so every call closes it; the Neon probe
# asks an arithmetic question because the direct route returned an empty reply to "say OK" (2026-11-21).
# Prints one line per route: "<SEAT> <route> LIVE" or "<SEAT> <route> DOWN <reason>". Exit 0 when
# every requested seat has at least one live route. A 429/quota on one Neon route is not a failure.
export PATH="$HOME/.opencode/bin:$PATH"
what=${1:-all}; rc=0
if [ "$what" = neon ] || [ "$what" = all ]; then
  set -a; source /root/Software/.secrets/yt-transcribe.env; source /root/Software/.secrets/gemini-neon.env; set +a
  live=0
  for m in google/gemini-4-neon openrouter/google/gemini-4-neon; do
    out=$(cd /tmp && timeout 120 opencode run -m "$m" "What is 2+3? Reply with the digit only." </dev/null 2>&1 | tail -3)
    if echo "$out" | grep -q "^5"; then echo "NEON $m LIVE"; live=1
    else echo "NEON $m DOWN $(echo "$out" | tr '\n' ' ' | cut -c1-160)"; fi
  done
  [ $live = 1 ] || rc=1
fi
if [ "$what" = astra ] || [ "$what" = all ]; then
  out=$(cd /tmp && timeout 180 codex exec --skip-git-repo-check -m gpt-6-astra "Reply with exactly: OK" </dev/null 2>&1 | tail -4)
  if echo "$out" | grep -q "^OK"; then echo "ASTRA gpt-6-astra LIVE"
  else echo "ASTRA gpt-6-astra DOWN $(echo "$out" | tr '\n' ' ' | cut -c1-160)"; rc=1; fi
fi
exit $rc
