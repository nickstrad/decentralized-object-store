# Component observability

Protocol snapshots are owner-loop operations. Each component's private metric state is nil
when disabled. The finite series list includes zeros and allocates independent point/label
slices on every snapshot; snapshots never enumerate object maps or perform I/O. These
metrics are excluded from EncodeState and protocol persistence.

A node registers one source each, in order: node, gossip, storage, erasure, Merkle, OCC,
saga. OCC Node combines Store transactions with replication counts, so do not also register
the Store. Region-scoped storage nodes and migration copiers share their owner's storage
metric state, disk inventory index and erasure meter. Short-lived codecs obtained through
storage.NewCodec share the lifetime meter; replacing or retiring them cannot reset counters.
Inventory repairs use the measured stripe entry points. Inventory Merkle comparisons and
incrementally maintained tree-entry totals are folded into the node's one Merkle source.

Erasure input bytes for Encode are the padded data shards; output bytes include all data
and parity shards. Reconstruction input bytes are the present shards, output bytes are only
the shards actually restored. ReconstructData ignores missing parity shards for restored
counts and output bytes. Failed operations count an error and contribute no successful-byte
or restored-shard totals. Snapshot in-flight is zero because synchronous owner-confined
codec calls cannot overlap an owner-loop snapshot.

Storage's local inventory is initialized once at construction from canonical shard files;
successful put/delete transitions maintain its totals thereafter. The index is shared by
all local disk facades and is never scanned during snapshot collection.

OCC discard telemetry counts only the authoritative persisted counter's committed increase.
Failed persistence rolls back the loss, repeated merges do not count it again, and opening
a store replays audit outbox records without creating new telemetry losses. Persisted
discard totals remain visible separately as a gauge.

Useful focused validation:
go test ./internal/{erasure,storage,node,gossip,merkle,occ,saga} -run 'TestMetrics|TestMetered' -count=1

Disabled metrics must bypass asynchronous callback wrappers before constructing the
closure. A nil check inside a retained callback still allocates the wrapper on every
operation. Storage requests and node Put/Get therefore pass the original callback directly
when metric state is nil. `TestMetricsDisabledRequestAllocations` retains transport callbacks
and compares allocations with direct transport calls; node's
`TestMetricsDisabledObjectCallbackAllocations` compares public Put/Get with their underlying
operation paths using a retaining fake clock. These tests isolate metrics overhead without
network traffic or timing thresholds. Compiler escape analysis also distinguishes retained
callbacks from the nonescaping synchronous erasure closures and node metric defers.
