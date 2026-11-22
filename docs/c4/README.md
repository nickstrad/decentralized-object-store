# C4 diagrams

**As built.** Every part of the system has landed, and every diagram was checked against the code when its part landed. The one element still drawn as planned is the Sync client, which is not to be built (OQ-7).

| Part | State | Diagrams |
| --- | --- | --- |
| DST harness (`internal/sim`, `internal/sim/simtest`, `cmd/objsim`) | built | 1, 3 |
| Environment interfaces (`internal/env`) | built | 3 |
| Erasure coding (`internal/erasure`) | built | 2, 5 |
| Storage engine (`internal/storage`): shard format, chunk store, striping, manifest, placement, shard request protocol, put and get paths | built | 2, 3, 9 |
| Gossip membership (`internal/gossip`): SWIM node (join and leave, probe, indirect probe, suspicion, dissemination, full-state sync), Membership view and events, wire codec | built | 2, 3, 4 |
| Merkle anti-entropy (`internal/merkle`): hash tree, diff protocol, wire codec, anti-entropy worker with scrub, `Repairer` and `Membership` interfaces | built | 2, 3, 6 |
| OCC metadata (`internal/occ`): hybrid logical clock, version vectors, versions with ETags and preconditions, last-writer-wins document merge, discard record, canonical encoding, store, quota check-and-set and recount, typed metadata document, replicating node, `Membership` interface | built | 2, 3, 7 |
| Saga engine (`internal/saga`): orchestrator, log, recovery, retry policy, bucket-migration workflow, step interfaces; model cluster (`internal/saga/sagasim`) | built | 2, 3, 8 |
| Node core (`internal/node`): the one node that the simulator and the real runtime both run. It builds the five protocol packages into a node and implements the interfaces they declare: `Membership` from the gossip view, `Repairer` over the metadata keys, `MigrationOps` on the real components. Object and bucket operations, shard inventory with repair and collection, loss declaration | built | 2, 3, and as the neighbour in 4, 6, 7, 8, 9 |
| Node runtime (`internal/noderuntime`): UDP and TCP transports, frame codec, file disk, wall clock, the node loop, `Runtime` as the object service | built | 2, 3 |
| HTTP object API (`internal/storage/httpapi`): handler, status table, limits, server, `Service` interface | built | 1, 2, 3 |
| `cmd/objnode` | built | 1, 3 |
| Sync client and `/v1/sync` | not to be built (OQ-7) | 1, 2 |

These diagrams started as the target architecture that [PLAN.md](../../PLAN.md) defines.
As each part landed, its diagrams were checked against the code and updated in the same
landing. Where the build differs from the plan, the diagram shows the build and a
"Differences from the target design" section says what changed.

## Diagrams

| # | Level | File | Shows |
| --- | --- | --- | --- |
| 1 | System Context | [01-system-context.md](01-system-context.md) | The object store and sync system, its clients and operator, peer nodes, and the DST harness as a test system |
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
  It is a `c4-sdk` bundle, `schemaVersion: 1`. Built elements and relationships have status `existing`, the rest `planned`; a diagram showing both is `mixed`.
- Each diagram file has a Mermaid rendering, a terminal (Unicode) rendering, an
  element table and a numbered relationship table. Edge numbers in the pictures
  match the table rows.
- [diagrams/](diagrams/) holds the Mermaid sources for the terminal renderings made
  by the `draw-visual` tool. Diagram 1 has one source. Diagrams 2 to 9 are split
  into two to six parts, because the text renderer draws a dense graph with
  crossing arrows that cannot be read. Each part is a simple fan-in, fan-out or
  chain, at most 80 columns wide, and each numbered relationship appears in exactly
  one part.
- Component diagrams contain the parts that PLAN.md names. A part that the build added
  is marked as such in the element table.

## Validation

Checked on 2026-11-22:

| Check | Tool | Result |
| --- | --- | --- |
| `model.json` decodes | `decodeArchitecture` from `c4-sdk@0.1.0` (`c4-sdk/core`) | `ok: true`, 0 errors, 0 warnings |
| Every diagram builds a scene | `buildScene` from `c4-sdk/core`, all 9 diagrams | 9 of 9 succeed |
| Terminal sources render | `render.py --update`, all 37 sources | all render |
| Terminal embeds are current and fit | `render.py --check` with `DRAW_VISUAL_MAX_WIDTH=80` | 37 embeds pass |
| Mermaid blocks, edge by edge | a script compares each block's numbered edges with the model's edges, the terminal sources and the relationship table | 9 of 9 agree |

The nine Mermaid blocks were last run through `mermaid.parse` (Mermaid 11) at the previous refresh of this set. The blocks changed for Chunk 6 use the same statements as before and were not re-parsed.

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
   `external` and `neighbour`. There were eight until Chunk 6: as built, no protocol
   package calls another, so the only neighbours of a component view are the node
   core, the environment interfaces and, for storage and erasure, each other.
   Each one stands for the container with the matching `c-` ID, is used only in
   component views, and links back to that container's views. This is a workaround
   for a limit in the tool. Nick decides whether to keep it or to change `c4-sdk`.
2. **Planned key entry.** Every legend has a `Planned` entry, because `c4-sdk` draws
   planned elements and relationships with dashed lines. Only the Sync client and its
   relationships are still planned.
3. **Technology on in-process relationships.** Relationships between Go packages in
   the one `objnode` process carry the technology `Go, in-process`. The DST harness
   and node runtime relationships to `internal/env` carry `Go interface implementation`.
4. **Interface relationships.** A protocol package that declares an interface and has
   the node core implement it is drawn with an arrow from the package to the node core,
   technology `Go interface, in-process`: the arrow points to the supplier. The Go
   import runs the other way, from `internal/node` to the package.

## Open items on this set

1. **Buy-in.** The set waits for Nick's buy-in together with the plan sign-off.
2. **Publishing.** The Pages C4 section must load `model.json` with `c4-sdk` and
   render the Mermaid blocks; that belongs to the Pages scaffold in Chunk 0a.
3. **Terminal renderings were checked as text only.** The rendered text was read
   in a terminal at 80 columns. No PNG snapshots were made.

## Tool: c4-sdk

The C4 ruling of 2026-11-21 names `c4-sdk` as the tool for these diagrams. Nick
first called it "c4-ask" in chat and clarified on 2026-11-21 that he meant `c4-sdk`;
there is no separate tool of that other name. An earlier design run searched for
the other name and recorded it as not found. That finding is closed.

`c4-sdk` (`github.com/nickstrad/c4-sdk`, published as `c4-sdk@0.1.0`) is Nick's
React and TypeScript library. It renders all seven C4 diagram types from a
validated JSON bundle. This set is authored as one such bundle, `model.json`, with
a Mermaid rendering for GitHub and Pages and a `draw-visual` text rendering for
terminals beside each diagram.
