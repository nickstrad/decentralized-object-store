# Decentralized Object Store & Local-First Sync Engine

A leaderless, multi-region object store written in Go on the standard library alone, with `pgregory.net/rapid` restricted to test files and enforced by a gate. It is a learning project built testing-first: every claim about durability, repair, and convergence is backed by deterministic simulation testing, property tests, and fuzzing. Metadata updates use optimistic concurrency control for conditional writes, while concurrent writes across partitions settle by last-writer-wins (highest hybrid logical clock, node ID breaking ties) so all replicas converge; every discarded write is counted and logged.

## Read the book

Read the book at [nickstrad.github.io/decentralized-object-store](https://nickstrad.github.io/decentralized-object-store/) or in this repository: each chapter explains one mechanism, maps it to packages and code excerpts, and states what the tests prove and do not.

- [Preface](book/front/preface.md) — scope, reading order, and stated limits
- [Topic Map](book/front/topic-map.md) — topics, packages, and main test targets

1. [Deterministic Simulation Testing](book/chapters/01-deterministic-simulation.md)
2. [SWIM Gossip Membership](book/chapters/02-swim-gossip.md)
3. [Reed-Solomon Erasure Coding](book/chapters/03-reed-solomon-erasure-coding.md)
4. [Merkle-Tree Anti-Entropy](book/chapters/04-merkle-anti-entropy.md)
5. [Optimistic Concurrency Control](book/chapters/05-optimistic-concurrency-control.md)
6. [Sagas](book/chapters/06-sagas.md)
7. [The Object Store and Sync Engine](book/chapters/07-object-store-and-sync-engine.md)
8. [End-to-End Tests and Hardening](book/chapters/08-end-to-end-and-hardening.md)

## What is built

| Building block | Status | Scope |
| --- | --- | --- |
| Foundation | Built | Docker Go 1.26.11 image, landing gates (G0–G7, G5R), canaries, site build |
| Deterministic simulator | Built | `internal/env`, event loop and nemesis (`internal/sim`, `simtest`), `cmd/objsim` |
| SWIM gossip membership | Built | Direct/indirect probing, suspicion, refutation, tombstones, push-pull sync (`internal/gossip`) |
| Reed-Solomon erasure coding | Built | GF(2^8) Cauchy codec (`internal/erasure`, `4+2` default), chunk store, striping, placement (`internal/storage`) |
| Merkle-tree anti-entropy | Built | Fanout-16 SHA-256 trie, bounded diff protocol, scrubber and repair worker (`internal/merkle`) |
| OCC metadata | Built | ETags, `If-Match`/`If-None-Match`, version vectors, last-writer-wins with logged discards, quotas (`internal/occ`) |
| Sagas | Built | Checksummed log, crash recovery, five-step bucket migration (`internal/saga`, `sagasim`) |
| Integration | Built | Wired node (`internal/node`), real runtime (`internal/noderuntime`), object API (`httpapi`), `cmd/objnode` |
| E2E suite and hardening | Built | Simulated and real-process scenarios (E1–E9), continuous oracles, nemesis, `FuzzClusterNemesis` (`test/e2e`) |
| Finished book | Built | Preface, topic map, eight chapters, as-built C4 diagrams, published site (`book/`, `docs/`) |

## Run the tests through Docker

Host prerequisites are Docker with Compose v2, `git`, `curl`, and a POSIX shell; Go runs inside the container. Build the image and run the whole test suite (about 7 minutes on a 4-core machine):

```sh
docker compose build dev
docker compose run --rm dev go test ./...
```

Run the fast lane (G0 hygiene, G1 `go test -short` with coverage floors, and G6 container check; about 3 minutes):

```sh
agent_work/gates/ci-fast.sh
```

Run the full landing gate on demand (G0–G7 including real processes; also needs `jq` and `sha256sum` on the host; about 2.5 hours on a 4-core machine; `demo` labels the output directory):

```text
GATE_STAGE=final agent_work/gates/gate.sh demo
```

Checks run in order:

- **G0 (hygiene):** gate tools, `gofmt`, `go vet`, `go build`, determinism lint, panic allowlist, stdlib-only dependency check, comment check (skipped in `final`), self-containment, and site lint.
- **G5R (real-process end-to-end):** runs first; runs end-to-end scenarios on real `-race` `objnode` processes (`E2E_TIER_R=1`).
- **G1 (unit):** `go test -count=1 -cover ./...` with an 85% statement coverage floor on core packages.
- **G2 (property):** `pgregory.net/rapid` property tests at 2,000 checks per property, seeded from the commit.
- **G3 (fuzz):** 30-second run for each of the 15 fuzz targets.
- **G4 (race):** `go test -race ./...` plus `-race -count=3` on `internal/node`, `internal/noderuntime`, `internal/storage/...`, `cmd/objnode`, and `test/e2e`.
- **G5 (simulation):** 500-seed sweep from the commit seed, with replay meta-tests and canaries.
- **G6 (environment):** verifies execution inside the project `dev` image.
- **G7 (cluster smoke):** host-side check: starts the three-node cluster, puts/gets a 1.5 MB object across nodes, verifies 412s, and tears it down.

## Replay a seed

Every failing single-seed simulator test prints a `REPLAY:` line with the exact test filter and seed so the failure reproduces byte for byte (aggregate sweep checks rerun the sweep). Replay a pinned single-seed nemesis run or the 100-node chaos CLI (same two SHA-256 digests per seed):

```sh
docker compose run --rm dev go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$/^shard=1$/^seed=1$' -seed=1 -count=1
docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3
```

## Run a local cluster

`compose.yaml` defines a three-node `cluster` profile (`node-a1`, `node-b1`, `node-c1` across regions `a`, `b`, and `c` with `2+1` erasure coding on ephemeral loopback ports). `compose.yaml` fixes the project name, so a second checkout on the same machine shares containers with the first; if you keep two checkouts, set `COMPOSE_PROJECT_NAME` to a different value in one of them. Start it, PUT an object through `node-a1`, GET it back from `node-c1`, and stop it:

```sh
docker compose --profile cluster up -d node-a1 node-b1 node-c1
A=$(docker compose --profile cluster port node-a1 8080)
C=$(docker compose --profile cluster port node-c1 8080)
i=0; until curl -sf "http://$A/_health" | grep -q '"Ready":true,"LiveMembers":3'; do
  i=$((i+1)); [ "$i" -lt 60 ] || { curl -s "http://$A/_health" || true; docker compose --profile cluster logs --tail 20; exit 1; }; sleep 1
done
curl -sf -X PUT "http://$A/demo"
echo
curl -sf -X PUT --data-binary "hello from region a" "http://$A/demo/hello.txt"
echo
i=0; until curl -sf "http://$C/demo/hello.txt"; do
  i=$((i+1)); [ "$i" -lt 60 ] || { docker compose --profile cluster logs --tail 20 node-c1; exit 1; }; sleep 1
done
echo
docker compose --profile cluster down
```

## Where things are

- **Book:** [book/manifest.json](book/manifest.json) (front matter in `book/front/`, chapters in `book/chapters/`).
- **C4 diagrams:** [docs/c4/README.md](docs/c4/README.md) (as-built system, container, and component views).
- **Plan:** [PLAN.md](PLAN.md) (build plan, testing strategy, and landing gates).
- **Specification:** [docs/project-source.md](docs/project-source.md) (original specification).
