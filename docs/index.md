# Decentralized Object Store & Local-First Sync Engine

A leaderless, multi-region object store written in Go on the standard library
alone. It is a learning project, built testing-first. Metadata conflicts are
handled with optimistic concurrency control rather than CRDTs: conditional writes
reject stale updates, while concurrent writes are resolved by last-writer-wins
(highest hybrid logical clock, node ID breaking ties) so all replicas converge;
the losing write is discarded, and every discard is counted and logged.

The foundation tooling, deterministic simulator, SWIM gossip membership,
Reed-Solomon erasure coding with the local chunk store, Merkle-tree
anti-entropy, and the Saga engine with bucket migration are built today. Planned
building blocks are optimistic concurrency control for metadata, full-cluster
integration, and an end-to-end test suite.

## How this site is organised

- **[The Book](../book/manifest.json)** grows one chapter per landed chunk.
  Published chapters: Chapter 1,
  [Deterministic Simulation Testing](../book/chapters/01-deterministic-simulation.md),
  Chapter 2, [SWIM Gossip Membership](../book/chapters/02-swim-gossip.md),
  Chapter 3,
  [Reed-Solomon Erasure Coding](../book/chapters/03-reed-solomon-erasure-coding.md),
  Chapter 4,
  [Merkle-Tree Anti-Entropy](../book/chapters/04-merkle-anti-entropy.md),
  and Chapter 6, [Sagas](../book/chapters/06-sagas.md).
- **[C4 diagrams](c4/README.md)** describe the target architecture across
  system, container, and component views. They show the planned design until
  each part is built.
- **[The Plan](../PLAN.md)** lays out the testing strategy, the building blocks,
  and the landing gates.
- **[The README](../README.md)** lists what is built versus planned and gives
  the Docker and gate commands.

## Run locally through Docker

Docker is the one environment for building and testing. The image ships Go
1.26.11, so Go does not need to be installed on the host.

Build the development image:

```sh
docker compose build dev
```

Run the test suite inside the container:

```sh
docker compose run --rm dev go test ./...
```

Run a 100-node chaos simulation from a fixed seed; the same seed always prints
the same two replay digests:

```sh
docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3
```
