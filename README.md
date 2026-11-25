# Object Store

A leaderless, multi-region object store written in Go on the standard library alone, with `pgregory.net/rapid` restricted to test files and enforced by a check. Every claim about durability, repair, and convergence is backed by deterministic simulation testing, property tests, and fuzzing. Metadata updates use optimistic concurrency control for conditional writes, while concurrent writes across partitions settle by last-writer-wins (highest hybrid logical clock, node ID breaking ties) so all replicas converge; every discarded write is counted and logged.

## Read the book

Read the book at [nickstrad.github.io/object-store](https://nickstrad.github.io/object-store/) or in this repository: each chapter explains one mechanism, maps it to packages and code excerpts, and states what the tests prove and do not.

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

## What is in the repository

| Part | Scope |
| --- | --- |
| Environment and checks | Docker image with Go 1.26.11, automated tests, and documentation site build |
| Deterministic simulator | `internal/env`, event loop and nemesis (`internal/sim`, `simtest`), `cmd/objsim` |
| SWIM gossip membership | Direct/indirect probing, suspicion, refutation, tombstones, push-pull sync (`internal/gossip`) |
| Reed-Solomon erasure coding | GF(2^8) Cauchy codec (`internal/erasure`, `4+2` default), chunk store, striping, placement (`internal/storage`) |
| Merkle-tree anti-entropy | Fanout-16 SHA-256 trie, bounded diff protocol, scrubber and repair worker (`internal/merkle`) |
| OCC metadata | ETags, `If-Match`/`If-None-Match`, version vectors, last-writer-wins with logged discards, quotas (`internal/occ`) |
| Sagas | Checksummed log, crash recovery, five-step bucket migration (`internal/saga`, `sagasim`) |
| Node, runtime, and API | Wired node (`internal/node`), real runtime (`internal/noderuntime`), object API (`httpapi`), `cmd/objnode` |
| Observability | Framework-free metric catalogue (`internal/metrics`), wired component/runtime/HTTP snapshots, off-by-default runner and `otel`/`prometheus`/`csv` adapters (`internal/telemetry`), `objnode` flags, pinned Docker Collector/Prometheus stack (`compose.observability.yaml`), and DuckDB-over-CSV queries (`deploy/observability/`) |
| End-to-end tests | Simulated clusters and real node processes, with network and storage failures injected during tests (`test/e2e`) |
| Book | Preface, topic map, eight chapters, C4 diagrams, published site (`book/`, `docs/`) |

## Run the tests through Docker

Use Docker with Compose v2 on Linux or Docker Desktop on macOS, along with `make`, `git`, `curl`, and a POSIX shell. The same commands use Docker's default platform, including ARM64 on Apple Silicon. Go runs inside the container. Run `make dev` to build the dev image or `make test` to build it and run the whole test suite (about 7 minutes on a 4-core machine).

```sh
make test
```

Run `make test-cluster` to check cross-node object reads, writes, and conditional requests in a three-node Compose cluster. Run `make test-cluster-csv` to keep a separate set of metrics for each node. Both targets use `jq` on the host.

Run the same quick checks as CI (about 3 minutes). They check formatting and builds, run `go test -short` with minimum coverage requirements, and verify that the checks ran in the dev container.

```sh
agent_work/gates/ci-fast.sh
```

Run the full verification suite on demand (about 2.5 hours on a 4-core machine). It requires `jq` and `sha256sum` on the host. The `demo` argument names the output directory.

```text
GATE_STAGE=final agent_work/gates/gate.sh demo
```

The suite runs end-to-end scenarios against real `objnode` processes, checks unit test coverage, runs property tests, fuzzes inputs, and checks for data races. It also sweeps 500 simulation seeds and starts a three-node cluster to verify cross-node reads and writes and HTTP 412 responses for failed preconditions.

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

## Observability

Metrics are defined as plain standard-library value types in `internal/metrics` (83 catalogue descriptors across all 10 components) and collected on demand without framework lock-in. Telemetry is off by default and configured in `objnode`'s JSON config or via `-observability`, `-observability-adapter`, `-observability-interval`, `-observability-timeout`, and `-observability-settings`. Three stdlib-only adapters are built in: OTLP/HTTP JSON (`otel`, with a pinned Docker Compose Collector → Prometheus stack in `compose.observability.yaml`), direct Prometheus HTTP scraping (`prometheus`), and atomic local CSV segments (`csv`, paired with offline DuckDB SQL queries in `deploy/observability/queries.sql` and `query-csv.sh`). See [docs/observability.md](docs/observability.md) for the full catalogue, flag reference, adapter settings, DuckDB analysis setup, and adapter extension guide.

`make test-csv` runs a CSV-enabled node and prints the `make query-csv` command for that run. `make test-csv-fixture` checks DuckDB queries against controlled data, and `make test-otel` checks that real node metrics reach Prometheus through the Collector.

## Where things are

- **Book:** [book/manifest.json](book/manifest.json) (front matter in `book/front/`, chapters in `book/chapters/`).
- **C4 diagrams:** [docs/c4/README.md](docs/c4/README.md) (system, container, and component views).
- **Observability:** [docs/observability.md](docs/observability.md) (metric catalogue, `objnode` flags, `otel`/`prometheus`/`csv` adapters, DuckDB-over-CSV analysis, Collector → Prometheus setup, and adapter extension guide).
