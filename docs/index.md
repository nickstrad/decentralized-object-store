# Decentralized Object Store & Local-First Sync Engine

A leaderless, multi-region object store written in Go on the standard library
alone. It is a learning project, built testing-first. Metadata conflicts are
handled with optimistic concurrency control rather than CRDTs: concurrent
versions stay as siblings and are never silently overwritten.

The foundation tooling and deterministic simulator are built today. No storage
or protocol code exists yet. Planned building blocks are SWIM gossip membership
(next to land), Reed-Solomon erasure coding, Merkle-tree anti-entropy,
optimistic concurrency control for metadata, Sagas for bucket migration, and an
end-to-end test suite.

## How this site is organised

- **[The Book](../book/manifest.json)** grows one chapter per landed chunk.
  Chapter 1,
  [Deterministic Simulation Testing](../book/chapters/01-deterministic-simulation.md),
  is published with the simulator.
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
