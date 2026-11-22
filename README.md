# Decentralized Object Store & Local-First Sync Engine

A leaderless, multi-region object store written in Go on the standard library
alone. It is a learning project, built testing-first so that every claim about
fault tolerance and consistency is backed by deterministic simulation, property
tests, and fuzzing. Metadata conflicts are handled with optimistic concurrency
control rather than CRDTs: conditional writes reject stale updates, while
concurrent writes are resolved by last-writer-wins (highest hybrid logical
clock, node ID breaking ties) so all replicas converge; the losing write is
discarded, and every discard is counted and logged.

Product code imports only the Go standard library. The sole external dependency
is `pgregory.net/rapid`, restricted to test files and checked by a gate.

## Current status

The foundation tooling, deterministic simulator, SWIM gossip membership,
Reed-Solomon erasure coding with the local chunk store, and the Saga engine with
bucket migration are built, along with chapters 1, 2, 3, and 6 of the book. Each
remaining building block lands in its own chunk together with a book chapter.

| Building block | Status | Scope |
| --- | --- | --- |
| Foundation | Built | Docker environment (Go 1.26.11), landing gates (G0–G6), gate canaries, book scaffold, target C4 diagrams, site |
| Deterministic simulator | Built | Environment interfaces (`internal/env`), single-threaded discrete-event simulator (`internal/sim`), sweep harness (`internal/sim/simtest`), and `cmd/objsim` |
| SWIM gossip membership | Built | Direct and indirect probing, suspicion with incarnation refutation, piggybacked gossip, clean leave, bounded tombstones, and push-pull partition-heal sync (`internal/gossip`) |
| Reed-Solomon erasure coding | Built | GF(2^8) Cauchy codec (`internal/erasure`, 4+2 default), checksummed chunk store, striping, manifests, region-aware placement, and striped PUT/GET node (`internal/storage`) |
| Merkle-tree anti-entropy | Planned | Tree diffing and background repair |
| OCC metadata | Planned | ETags, conditional writes, version vectors to detect concurrency, and last-writer-wins with every discarded write counted and logged |
| Sagas | Built | Compensating saga engine, checksummed log, crash recovery, and five-step bucket migration (`internal/saga`), plus model cluster and failure-point matrix runner (`internal/saga/sagasim`) |
| Integration, E2E, and book | Planned | Full-cluster integration, end-to-end suite, and finished book |

## Working through Docker

Docker is the one environment for building, testing, and running the gates.
Nothing needs Go installed on the host.

Build the development image (Go 1.26.11):

```sh
docker compose build dev
```

Run tests or any command inside the container:

```sh
docker compose run --rm dev go test ./...
docker compose run --rm dev <command>
```

Run a 100-node chaos simulation from a fixed seed; the same seed always prints
the same two replay digests:

```sh
docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3
```

### Landing gates and canaries

Run the seven landing gates from the repository root on the host. The script
builds the image and re-runs itself inside the container. On a checkout of
`main`, run the `final` stage (`<name>` only labels the output under
`agent_work/<name>/gates/result.json`):

```sh
GATE_STAGE=final agent_work/gates/gate.sh <name>
```

The default `build` stage (`agent_work/gates/gate.sh <name>`) is for a chunk
branch before its comments are written: it requires product Go to add no
comments beyond the commit the branch started from. It stops on `main` because
a commit cannot be checked against itself; `GATE_STAGE=final` runs G0 through
G6 without that comment check.

The script runs G0 through G6 in order and records a machine-readable result:

- **G0 (hygiene):** gofmt, vet, build, a determinism lint, a panic allowlist, a
  dependency check, a comment check, self-containment, and site lint.
- **G1 (unit):** unit tests with a coverage floor.
- **G2 (property):** `pgregory.net/rapid` property tests (2,000 checks, seeded
  from the commit).
- **G3 (fuzz):** a 30-second fuzz run per fuzz target.
- **G4 (race):** the race detector.
- **G5 (simulation):** simulation seed sweeps (500 seeds).
- **G6 (environment):** proof that the run happened inside the project image.

To test the gates themselves, run the canary suite:

```sh
agent_work/gates/canaries.sh
```

It plants one violation at a time in a throwaway copy of the tree and requires
the owning gate to go red for the right reason.

## Where to read

- **Site:** [nickstrad.github.io/decentralized-object-store](https://nickstrad.github.io/decentralized-object-store/)
  publishes the front page, the Book, the C4 diagrams, and the plan.
- **The Book:** [book/manifest.json](book/manifest.json) is the table of
  contents; chapters live in `book/chapters/`. Published chapters: Chapter 1,
  [Deterministic Simulation Testing](book/chapters/01-deterministic-simulation.md),
  Chapter 2, [SWIM Gossip Membership](book/chapters/02-swim-gossip.md),
  Chapter 3,
  [Reed-Solomon Erasure Coding](book/chapters/03-reed-solomon-erasure-coding.md),
  and Chapter 6, [Sagas](book/chapters/06-sagas.md).
- **C4 diagrams:** [docs/c4/README.md](docs/c4/README.md) holds the C4
  architecture diagrams. They show the target design until each part is built.
- **Plan and specification:** [PLAN.md](PLAN.md) is the full build and testing
  plan; [docs/project-source.md](docs/project-source.md) is the original
  specification.
