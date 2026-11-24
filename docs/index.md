# Object Store

A leaderless, multi-region object store written in Go on the standard library alone, with `pgregory.net/rapid` restricted to test files. It uses optimistic concurrency control for conditional metadata writes and settles concurrent writes across partitions by last-writer-wins (highest hybrid logical clock, node ID breaking ties) with every discarded write counted and logged.

## Start here

The book is the main guide to the system. Start with the front matter, then read the eight chapters in order alongside the repository:

- [Preface](../book/front/preface.md) — what the project is, how the book is structured, and its stated limits
- [Topic Map](../book/front/topic-map.md) — topics mapped to Go packages and main tests

1. [Deterministic Simulation Testing](../book/chapters/01-deterministic-simulation.md)
2. [SWIM Gossip Membership](../book/chapters/02-swim-gossip.md)
3. [Reed-Solomon Erasure Coding](../book/chapters/03-reed-solomon-erasure-coding.md)
4. [Merkle-Tree Anti-Entropy](../book/chapters/04-merkle-anti-entropy.md)
5. [Optimistic Concurrency Control](../book/chapters/05-optimistic-concurrency-control.md)
6. [Sagas](../book/chapters/06-sagas.md)
7. [The Object Store and Sync Engine](../book/chapters/07-object-store-and-sync-engine.md)
8. [End-to-End Tests and Hardening](../book/chapters/08-end-to-end-and-hardening.md)

## Elsewhere on this site

- **[C4 diagrams](c4/README.md)** — system, container, and component architecture views.
- **[The README](../README.md)** — component overview, Docker test commands, seed replay, and local cluster quickstart.

## Run locally through Docker

Docker is the one environment for building and testing. The image ships Go 1.26.11, so Go does not need to be installed on the host.

Build the development image:

```sh
docker compose build dev
```

Run the test suite inside the container:

```sh
docker compose run --rm dev go test ./...
```

Run a 100-node chaos simulation from a fixed seed; the same seed always prints the same two replay digests:

```sh
docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3
```
