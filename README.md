# Decentralized Object Store & Local-First Sync Engine

A leaderless, multi-region object storage and collaborative metadata sync
cluster in Go, built from scratch around five distributed-system
primitives: SWIM gossip, Reed-Solomon erasure coding, Merkle-tree
anti-entropy, CRDTs, and Sagas — designed testing-first, with
deterministic simulation testing, property tests, and fuzzing guiding
the build.

Status: planning phase. The full plan (PLAN.md) is being prepared; the
architecture write-ups will be published on GitHub Pages as each
primitive lands. Spec: docs/project-source.md.
