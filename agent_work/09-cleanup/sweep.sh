#!/bin/bash
# Grep sweep over the reader-facing files after the rename and prose cleanup.
# Usage: agent_work/09-cleanup/sweep.sh          (run at the repository root)
# Reader-facing: README.md, docs/index.md, book/**, docs/c4/** (incl. model.json), comments
# and help text in product Go (cmd/, internal/, test/), compose.yaml, Dockerfile.
# Part 1 must print nothing: the old name and the study framing. Part 2 prints every
# remaining line that matches a build-machinery word, for a reader to judge; technical
# uses (chunk store, saga orchestrator, check labels, literal paths) are expected there.
# Exit 0 when part 1 is empty, 1 otherwise.
cd "$(dirname "$0")/../.." || exit 2
md=(README.md docs/index.md book docs/c4)
src=(cmd internal test compose.yaml Dockerfile)
echo "== part 1: old name and study framing (must be empty)"
p1='decentralized-object|Decentralized Object Store|decentralized object|local-first|learning project|testing project|learning artifact'
hits=$(git grep -nIiE "$p1" -- "${md[@]}" "${src[@]}"); [ -z "$hits" ] || echo "$hits"
echo "== part 2: build-machinery words (each remaining line judged by a reader)"
p2='\bagents?\b|\bseats?\b|orchestrat|\bchunk [0-9]|\bgates?\b|landing|\blanded\b|\bOpus\b|\bNeon\b|\bAstra\b|\bFable\b|\bClaude\b|Gemini|\bowner\b|\bruling|PLAN\.md|project-source|the build\b|\bOQ-[0-9]+|\bWave [AB]\b|\bphase [0-9]|arbitrat|\breview|adversarial|kickoff|\bbrief\b|\bCRDT'
git grep -nIiE "$p2" -- "${md[@]}" | cut -c1-220
git grep -nIiE "//.*($p2)" -- 'cmd/*.go' 'internal/*.go' 'test/*.go' | cut -c1-220
git grep -nIiE "$p2" -- 'cmd/*/help.txt' compose.yaml Dockerfile | cut -c1-220
[ -z "$hits" ]
