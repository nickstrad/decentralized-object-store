# Decentralized Object Store & Local-First Sync Engine

A leaderless, multi-region object store written in Go on the standard library
alone. It is a learning project, built testing-first. Metadata conflicts are
handled with optimistic concurrency control rather than CRDTs: concurrent
versions stay as siblings and are never silently overwritten.

Only the foundation tooling exists today. No storage or protocol code has been
built yet. Planned building blocks are a deterministic simulator (next to land),
SWIM gossip membership, Reed-Solomon erasure coding, Merkle-tree anti-entropy,
optimistic concurrency control for metadata, Sagas for bucket migration, and an
end-to-end test suite.

## How this site is organised

- **[The Book](../book/manifest.json)** grows one chapter per landed chunk.
  Chapters are published as they land, and none has landed yet. The first
  chapter, on deterministic simulation testing, arrives with the simulator.
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
