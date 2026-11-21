# Project 3: Decentralized Object Store & Local-First Sync Engine (Go)

Faithful copy of Nick's Google Doc (2026-11-20):
https://docs.google.com/document/d/1z9iL9sjn2K_k30sD2fjmDBJ-ZmZAUwtE9PlyLpFxnzk/edit

## Overview

- Goal: Build a leaderless, multi-region object storage and collaborative
  metadata sync cluster (Mini-S3 + Figma Sync) in Go from scratch, structured
  as a readable commit history that demonstrates availability-first,
  self-healing distributed architectures.
- Language & Runtime: Go (latest stable release, standard library
  networking/crypto, pluggable simulated network for chaos and partition
  testing).
- Primitives Covered (5 Core Distributed System Components):
  - Gossip Protocols (Epidemic / SWIM)
  - Erasure Coding (Reed-Solomon)
  - Merkle Trees (Hash Trees)
  - CRDTs (Conflict-Free Replicated Data Types)
  - Sagas (Compensating Distributed Workflows)

## Go Engineering & Testing Guidelines

- Zero External Engine Dependencies: Implement all 5 core primitives from
  scratch in Go (avoid importing HashiCorp memberlist, klauspost/reedsolomon,
  or external CRDT/Temporal SDKs).
- Deterministic Chaos Testing: Build a simulated multi-node cluster harness
  that can drop packets, corrupt disk shards, partition regions, and reorder
  messages to prove convergence and self-healing.
- Concurrency & Correctness Validation: Validate concurrent state transitions
  with `go test -race ./...` and property-based tests (e.g., verifying CRDT
  commutativity, associativity, and idempotency under arbitrary message
  permutations).

## Suggested Go Repository Structure

- `internal/sim/`: First-class discrete-event chaos simulator (seeded PRNG,
  virtual Clock, fault-injecting UDP/RPC network with
  partitions/reordering/duplication, and bit-rot injecting virtual Disk)
- `cmd/objnode/`: CLI entrypoint for running a storage & sync peer node
- `internal/gossip/`: SWIM membership protocol, ping/ping-req probes, and
  suspicion state machine
- `internal/erasure/`: Galois Field GF(2^8) arithmetic and Reed-Solomon
  encoder/decoder
- `internal/merkle/`: Cryptographic Merkle Tree builder, diff protocol, and
  anti-entropy repair worker
- `internal/crdt/`: State-based CRDT implementations (LWW-Element-Set,
  OR-Set, PN-Counter, LWW-Register)
- `internal/saga/`: Durable Saga orchestrator with forward steps and
  compensating rollback handlers
- `internal/storage/`: Local chunk store and HTTP/RPC object API

## Commit-by-Commit Roadmap

### Commit 0: feat(sim): build first-class discrete-event chaos simulator and fault-injecting storage

- Primitive: Deterministic Simulation Testing (DST) & Chaos Infrastructure
- Scope: Build a single-threaded, seed-driven discrete-event simulation
  engine before implementing any cluster protocols: a seeded PRNG
  (*math/rand.Rand), virtual Clock, simulated network capable of asymmetric
  regional partitions, packet drops, duplication, and reordering across
  20-100+ nodes, and a virtual chunk store capable of injecting silent disk
  bit-rot and abrupt node wipes.
- Verification: Meta-test verifying 100% deterministic replay — running the
  same multi-node chaos schedule twice with the same int64 seed yields
  bit-identical event logs and final cluster state.

### Commit 1: feat(cluster): implement SWIM Gossip Protocol for membership & failure detection

- Primitive: Gossip Protocols (Epidemic / SWIM)
- Scope: Implement peer discovery, periodic randomized ping/indirect
  ping-req failure detection, suspicion timeouts, and piggybacked membership
  dissemination over UDP/in-memory transport with zero central coordinator.
- Verification: 20-node simulated cluster tests verifying O(log N)
  convergence time on node joins, clean leaves, and abrupt node crashes.

### Commit 2: feat(storage): stripe blobs with Reed-Solomon Erasure Coding

- Primitive: Erasure Coding (Reed-Solomon)
- Scope: Implement GF(2^8) Galois Field arithmetic and Vandermonde/Cauchy
  matrix encoding to split uploaded objects into k data shards and m parity
  shards distributed across gossip-discovered peers, reconstructing objects
  even when up to m shards are missing or corrupted.
- Verification: Round-trip integrity tests deleting arbitrary combinations
  of m shards (data and parity) and verifying exact byte-for-byte
  reconstruction.

### Commit 3: feat(repair): add Merkle Tree anti-entropy sync

- Primitive: Merkle Trees (Hash Trees)
- Scope: Maintain per-bucket SHA-256 Merkle Trees over stored object/chunk
  hashes and run a background anti-entropy loop that exchanges tree roots
  and traverses only mismatched branches in O(log N) round trips to repair
  silent bit-rot or missed writes.
- Verification: Fault-injection tests silently flipping bytes on a replica's
  disk or dropping writes during a partition, confirming automatic detection
  and minimal-bandwidth delta repair.

### Commit 4: feat(sync): add CRDT state layer for multi-region active-active metadata

- Primitive: CRDTs (Conflict-Free Replicated Data Types)
- Scope: Implement state-based CRDTs (PN-Counter for storage quotas, OR-Set
  for bucket tags/ACL memberships, LWW-Register for object metadata)
  disseminated over gossip so multiple regions accept concurrent writes
  offline or across partitions with zero consensus latency.
- Verification: Property-based tests delivering state updates in random,
  duplicated, and partitioned orders across replicas and asserting identical
  converged state.

### Commit 5: feat(workflows): orchestrate cross-region bucket migrations with Sagas

- Primitive: Sagas (Compensating Distributed Workflows)
- Scope: Build a persistent Saga execution engine to coordinate multi-step
  cluster workflows (e.g., migrating a tenant bucket between regions:
  allocate target -> copy shards -> verify Merkle root -> flip CRDT routing
  pointer -> delete source) with automatic compensating rollback actions on
  partial failure.
- Verification: Failure-injection tests failing each step of the migration
  workflow and verifying clean rollback without orphaned chunks or
  split-brain metadata.

## First-Class Deterministic Simulation, Property Tests & Fuzzing

### Core DST & Fuzzing Architecture

- Single-Threaded Discrete-Event Cluster Simulation: Simulate dozens of
  nodes, network links, and disk failures in a single deterministic Go
  process driven by -seed=<int64>, turning multi-hour distributed chaos
  tests into millisecond reproducible runs.
- Continuous Nemesis & Healing Cycles: Alternate between high-chaos phases
  (regional partitions, packet drops, bit-rot, node crashes) and healing
  phases, asserting that availability holds during chaos and full
  convergence holds after healing.
- Mathematical Law Fuzzing (testing.F): Use Go's native fuzzer to
  exhaustively verify algebraic invariants for Galois Field / Reed-Solomon
  coding and CRDT semilattice laws.

### Component-Specific Simulation, Property & Fuzz Suites

- SWIM Gossip Convergence & False-Positive DST:
  - Convergence Property: Simulate 20-100 nodes under 5-10% packet loss,
    node joins, clean leaves, and abrupt crashes; assert 100% of live nodes
    converge to identical membership views within O(log N) protocol periods
    after partitions heal.
  - Asymmetry Resilience: Inject single-link asymmetric drops to verify
    indirect ping-req probes prevent false-positive node evictions.
- Reed-Solomon Erasure Coding Algebraic Fuzzing (FuzzErasureCoding):
  - Round-Trip Property: Fuzz arbitrary payload sizes (0 bytes, 1 byte,
    prime lengths, multi-MB), valid (k, m) shard configurations, and all
    combinations of up to m missing or corrupted shards; assert exact
    byte-for-byte reconstruction.
  - Failure Boundary Property: Assert clean, panic-free error returns
    whenever more than m shards are lost or corrupted.
- Merkle Tree Anti-Entropy DST & Fuzzing (FuzzMerkleDiff):
  - Completeness & Efficiency Properties: Fuzz arbitrary key/hash maps with
    insertions, deletions, and silent bit-rot mutations; assert the Merkle
    diff protocol identifies 100% of divergent keys (zero missed diffs)
    while exchanging O(d log N) hashes rather than full-table scans.
  - End-to-End Self-Healing DST: Silently flip random bytes on simulated
    replica disks during ongoing traffic and verify background anti-entropy
    detects and repairs all corrupted chunks.
- CRDT Semilattice Law Property Tests (FuzzCRDTLaws) & Partition DST:
  - Mathematical Invariants: For PN-Counter, OR-Set, and LWW-Register, fuzz
    arbitrary replica states A, B, and C and assert Commutativity
    (Merge(A, B) == Merge(B, A)), Associativity (Merge(Merge(A, B), C) ==
    Merge(A, Merge(B, C))), and Idempotency (Merge(A, A) == A).
  - Multi-Region Convergence DST: Deliver concurrent mutations across
    partitioned, reordering, and packet-duplicating simulated networks;
    after partition heal, assert bit-identical converged metadata across
    all replicas.
- Saga Workflow Crash & Compensation DST:
  - Exhaustive Failure-Point Simulation: Inject failures and orchestrator
    crashes at every step boundary of a cross-region bucket migration
    (before action, after action but before durable log write, and
    mid-compensation).
  - Terminal State Invariant: Assert idempotent compensation and clean
    terminal state — every migration either completes with a verified
    Merkle root and updated CRDT pointer, or rolls back completely with
    zero orphaned shards and zero split-brain metadata.
