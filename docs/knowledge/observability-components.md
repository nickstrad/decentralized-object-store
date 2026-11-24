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

Region-scoped storage nodes and migration copiers use `NewNodeSharingMetrics` with the
same disk and owner loop as their owner. This constructor shares the existing inventory
index and codec meter before metric initialization, so constructing a facade performs no
inventory List/Read and allocates no discarded metric map or codec counter state. The
primary storage node still inventories the disk once on construction; env.Disk exposes
names but no blob sizes, so this startup inventory reads blob bodies. The regression
`TestMetricsSharedStorageConstructionDoesNotScan` exercises both actual facade paths and
checks disk calls, shared local gauges and erasure lifetime totals.

Storage write bytes count bytes accepted by Disk.Write, including a write followed by a
failed Sync. An identical idempotent Put only Syncs and adds no write bytes. Local shard
and byte gauges describe the readable written disk layer, so accepted Write/Delete updates
them before Sync; operation outcome counters still report a Sync failure. A restart rebuilds
the inventory from the surviving disk state. `TestMetricsStorageWrittenLayerAndIdempotentBytes`
covers these distinctions.

Node HEAD operations use the finite `get` category. A public operation called from an
asynchronous completion callback is a separate operation: the wrapper records completion
before invoking that callback. Synchronous public methods use the depth guard to avoid
counting their own nested implementation calls. Storage's private codec field is created
only as a MeteredCodec by the two constructors; the internal type assertions depend on
that invariant, so any future codec injection must preserve it or change those assertions.

Saga recoveries count successfully loaded known saga logs once per engine incarnation,
including terminal logs. Terminal reloads do not increment fresh starts or completions;
only nonterminal reloads enter the active gauge and resume work. The existing
`TestMetricsSagaRetryCompensationRecovery` pins the terminal reload behavior. This is a
log-load counter rather than a count of only interrupted executions.
