#!/usr/bin/env bash
# compose.yaml fixes a shared name; isolate this audit from other checkouts.
export COMPOSE_PROJECT_NAME=objstore-walk
set -eu
cd "$(dirname "$0")/../../.."
if [ -n "$(docker ps -aq --filter label=com.docker.compose.project=$COMPOSE_PROJECT_NAME)" ]; then
  echo 'Refusing to overlap an existing audit run' >&2
  exit 2
fi
trap 'docker compose --profile cluster down > agent_work/adversarial/08-book/claims-cleanup.log 2>&1' EXIT
docker compose run --rm -T dev sh -c 'gofmt -w agent_work/adversarial/08-book/claims_test.go && go test -v -count=1 ./agent_work/adversarial/08-book' > agent_work/adversarial/08-book/claims-run.log 2>&1
