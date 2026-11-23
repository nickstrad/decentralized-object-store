# Topic Map

Use this table to move between each topic in the book, its Go packages, and the main simulation, property, real-runtime, and fuzz tests that verify it.

| Topic | Chapter | Package(s) | Main tests |
| --- | --- | --- | --- |
| Deterministic simulation | [Deterministic Simulation Testing](../chapters/01-deterministic-simulation.md) | `internal/env`, `internal/sim`, `internal/sim/simtest`, `internal/sim/simtoy`, `cmd/objsim` | `TestReplaySameProcess`, `TestReplayAcrossProcesses`, `TestPropNemesisRespectsBudget`, `FuzzSimSchedule` |
| SWIM gossip membership | [SWIM Gossip Membership](../chapters/02-swim-gossip.md) | `internal/gossip` | `TestSWIMConvergenceDST`, `TestSWIMAsymmetricLinkDST`, `TestSWIMLongPartitionHealDST`, `FuzzGossipWire` |
| Reed-Solomon erasure coding and the chunk store | [Reed-Solomon Erasure Coding](../chapters/03-reed-solomon-erasure-coding.md) | `internal/erasure`, `internal/storage` | `TestStripedPutGetDST`, `TestPropChunkStoreModel`, `FuzzErasureCoding`, `FuzzShardDecode` |
| Merkle-tree anti-entropy | [Merkle-Tree Anti-Entropy](../chapters/04-merkle-anti-entropy.md) | `internal/merkle` | `TestBitRotRepairDST`, `TestLyingPeerDST`, `FuzzMerkleDiff`, `FuzzMerkleWire` |
| Optimistic concurrency control and last-writer-wins | [Optimistic Concurrency Control](../chapters/05-optimistic-concurrency-control.md) | `internal/occ` | `TestMultiRegionConvergenceDST`, `TestOfflineRegionDST`, `TestQuotaOvershootUnderPartitionDST`, `FuzzOCCConditionalWrite` |
| Sagas and bucket migration | [Sagas](../chapters/06-sagas.md) | `internal/saga`, `internal/saga/sagasim` | `TestMigrationFailurePointsDST`, `TestMigrationUnderNemesisDST`, `TestDuelingMigrationsDST`, `FuzzSagaLogRecover` |
| The integrated node, real runtime and object API | [The Object Store and Sync Engine](../chapters/07-object-store-and-sync-engine.md) | `internal/node`, `internal/noderuntime`, `internal/storage/httpapi`, `cmd/objnode` | `TestClusterPartitionHealDST`, `TestConformanceOnLoopback`, `TestFileDiskCrashLeavesOldOrNewBytes`, `FuzzObjectAPI` |
| End-to-end scenarios, nemesis cycles and fuzz hardening | [End-to-End Tests and Hardening](../chapters/08-end-to-end-and-hardening.md) | `test/e2e`, `internal/nodetest` | `TestE2PartitionAndHealS`, `TestE7NemesisHealingCyclesS`, `TestE9ShutdownUnderLoadR`, `FuzzClusterNemesis` |
