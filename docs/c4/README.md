# C4 diagrams

Architecture diagrams of Object Store across three C4 levels: system context, container, and component. Sync runs between nodes only; there is no client-side replica.

| Part | Diagrams |
| --- | --- |
| DST harness (`internal/sim`, `internal/sim/simtest`, `cmd/objsim`) | 1, 3 |
| Environment interfaces (`internal/env`) | 3 |
| Erasure coding (`internal/erasure`) | 2, 5 |
| Storage engine (`internal/storage`): shard format, chunk store, striping, manifest, placement, shard request protocol, put and get paths | 2, 3, 9 |
| Gossip membership (`internal/gossip`): SWIM node (join and leave, probe, indirect probe, suspicion, dissemination, full-state sync), Membership view and events, wire codec | 2, 3, 4 |
| Merkle anti-entropy (`internal/merkle`): hash tree, diff protocol, wire codec, anti-entropy worker with scrub, `Repairer` and `Membership` interfaces | 2, 3, 6 |
| OCC metadata (`internal/occ`): hybrid logical clock, version vectors, versions with ETags and preconditions, last-writer-wins document merge, discard record, canonical encoding, store, quota check-and-set and recount, typed metadata document, replicating node, `Membership` interface | 2, 3, 7 |
| Saga engine (`internal/saga`): orchestrator, log, recovery, retry policy, bucket-migration workflow, step interfaces; model cluster (`internal/saga/sagasim`) | 2, 3, 8 |
| Node core (`internal/node`): the one node that the simulator and the real runtime both run. It builds the five protocol packages into a node and implements the interfaces they declare: `Membership` from the gossip view, `Repairer` over the metadata keys, `MigrationOps` on the real components. Object and bucket operations, shard inventory with repair and collection, loss declaration | 2, 3, and as the neighbour in 4, 6, 7, 8, 9 |
| Node runtime (`internal/noderuntime`): UDP and TCP transports, frame codec, file disk, wall clock, the node loop, `Runtime` as the object service | 2, 3 |
| HTTP object API (`internal/storage/httpapi`): handler, status table, limits, server, `Service` interface | 1, 2, 3 |
| `cmd/objnode` | 1, 3 |
| End-to-end suite (`test/e2e`): the same scenarios on the simulator and on real `objnode` processes over HTTP. Test code, drawn as part of relationship 21 of diagram 3 | 3 |

## Diagrams

| # | Level | File | Shows |
| --- | --- | --- | --- |
| 1 | System Context | [01-system-context.md](01-system-context.md) | Object Store, its client and operator, peer nodes, and the DST harness as a test system |
| 2 | Container | [02-container.md](02-container.md) | Domain view: object API, node runtime, node core, storage engine, erasure coding, gossip membership, Merkle anti-entropy, OCC metadata, Saga engine |
| 3 | Container | [03-container-runtime.md](03-container-runtime.md) | Runtime and environment view: `cmd/objnode`, the object API, `internal/noderuntime`, `internal/node`, `internal/env`, node disk, peers, and the DST harness (`internal/sim`) |
| 4 | Component | [04-component-gossip.md](04-component-gossip.md) | `internal/gossip`: join and leave, SWIM probe, indirect probe, suspicion state machine, dissemination, full-state sync, Membership view, wire codec |
| 5 | Component | [05-component-erasure.md](05-component-erasure.md) | `internal/erasure`: GF(2^8) arithmetic, matrix inversion, Cauchy coding matrix, Encode, Reconstruct, Verify |
| 6 | Component | [06-component-merkle.md](06-component-merkle.md) | `internal/merkle`: tree builder, diff protocol, wire codec, anti-entropy worker, Repairer and Membership interfaces |
| 7 | Component | [07-component-occ.md](07-component-occ.md) | `internal/occ`: hybrid logical clock, version vectors, versions and preconditions, last-writer-wins document merge, discard record, canonical encoding, store, quota check-and-set, typed metadata document, replicating node, Membership interface |
| 8 | Component | [08-component-saga.md](08-component-saga.md) | `internal/saga`: orchestrator, log, recovery, bucket-migration workflow, step interfaces, model cluster |
| 9 | Component | [09-component-storage.md](09-component-storage.md) | `internal/storage`: shard format, chunk store, striping, manifest, placement, shard request protocol, put and get paths |

The Container level has two views of the same containers, because one picture of all
relationships does not stay readable.

## Format

- [model.json](model.json) is the C4 model: every element, relationship and diagram.
  It is a `c4-sdk` bundle, `schemaVersion: 1`. Every element, relationship and diagram has status `existing`. The `plannedNotes` field is part of the schema; here it holds architecture notes.
- Each diagram file has a Mermaid rendering, a terminal (Unicode) rendering, an
  element table and a numbered relationship table. Edge numbers in the pictures
  match the table rows.
- [diagrams/](diagrams/) holds the Mermaid sources for the terminal renderings made
  by the `draw-visual` tool. Diagram 1 has one source. Diagrams 2 to 9 are split
  into two to six parts, because the text renderer draws a dense graph with
  crossing arrows that cannot be read. Each part is a simple fan-in, fan-out or
  chain, at most 80 columns wide, and each numbered relationship appears in exactly
  one part.

## Validation

| Check | Tool | Result |
| --- | --- | --- |
| `model.json` decodes | `decodeArchitecture` from `c4-sdk@0.1.0` (`c4-sdk/core`) | `ok: true`, 0 errors, 0 warnings |
| Every diagram builds a scene | `buildScene` from `c4-sdk/core`, all 9 diagrams | 9 of 9 succeed |
| Terminal sources render | `render.py --update`, all 37 sources | all render |
| Terminal embeds are current and fit | `render.py --check` with `DRAW_VISUAL_MAX_WIDTH=80` | 37 embeds pass |
| Mermaid blocks, edge by edge | a script compares each block's numbered edges with the model's edges, the terminal sources and the relationship table | 9 of 9 agree |

To repeat the checks after a change:

```sh
# Model: in a scratch folder with `npm install c4-sdk@0.1.0`
node -e "import('c4-sdk/core').then(m => { const r = m.decodeArchitecture(JSON.parse(require('fs').readFileSync(process.argv[1], 'utf8'))); console.log(r.ok, r.diagnostics) })" docs/c4/model.json

# Terminal embeds: render.py is in the draw-visual skill's scripts folder
render.py --update docs/c4/0*.md
DRAW_VISUAL_MAX_WIDTH=80 render.py --check docs/c4/0*.md
```

## Model notes

1. **Neighbour elements.** `c4-sdk` 0.1.0 accepts a container in another
   container's component view only when the element carries the `external` tag, and
   the tag belongs to the element, not to the view. Tagging the real containers
   would draw them as external in the two Container views. The model therefore has
   four neighbour elements, `n-node`, `n-storage`, `n-erasure` and `n-env`, tagged
   `external` and `neighbour`: no protocol package calls another, so the only
   neighbours of a component view are the node core, the environment interfaces and,
   for storage and erasure, each other. Each one stands for the container with the
   matching `c-` ID, is used only in component views, and links back to that
   container's views.
2. **Technology on in-process relationships.** Relationships between Go packages in
   the one `objnode` process carry the technology `Go, in-process`. The DST harness
   and node runtime relationships to `internal/env` carry `Go interface implementation`.
3. **Interface relationships.** A protocol package that declares an interface and has
   the node core implement it is drawn with an arrow from the package to the node core,
   technology `Go interface, in-process`: the arrow points to the supplier. The Go
   import runs the other way, from `internal/node` to the package.
4. **Terminal renderings.** The rendered text diagrams were checked in an 80-column
   terminal; no PNG snapshots are stored.

## Tool: c4-sdk

`c4-sdk` (`github.com/nickstrad/c4-sdk`, published as `c4-sdk@0.1.0`) is a React
and TypeScript library that renders all seven C4 diagram types from a validated
JSON bundle. This set is authored as one such bundle, `model.json`, with a Mermaid
rendering for GitHub and Pages and a `draw-visual` text rendering for terminals
beside each diagram.
