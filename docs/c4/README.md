# C4 diagrams

**TARGET architecture — designed 2026-11-21, awaiting Nick's buy-in.**

These diagrams describe the architecture that [PLAN.md](../../PLAN.md) defines. No
product code exists yet. As chunks land, each diagram is updated to the as-built
architecture in the same landing.

## Diagrams

| # | Level | File | Shows |
| --- | --- | --- | --- |
| 1 | System Context | [01-system-context.md](01-system-context.md) | The object store and sync system, its clients and operator, peer nodes, and the DST harness as a test system |
| 2 | Container | [02-container.md](02-container.md) | Domain view: object API, storage engine, erasure coding, gossip membership, Merkle anti-entropy, CRDT metadata, Saga engine |
| 3 | Container | [03-container-runtime.md](03-container-runtime.md) | Runtime and environment view: `cmd/objnode`, `internal/node`, `internal/env`, node disk, peers, and the DST harness (`internal/sim`) |
| 4 | Component | [04-component-gossip.md](04-component-gossip.md) | `internal/gossip`: SWIM probe, indirect probe, suspicion state machine, dissemination, full-state sync, Membership view |
| 5 | Component | [05-component-erasure.md](05-component-erasure.md) | `internal/erasure`: GF(2^8) arithmetic, matrix inversion, Cauchy coding matrix, Encode, Reconstruct, Verify |
| 6 | Component | [06-component-merkle.md](06-component-merkle.md) | `internal/merkle`: tree builder, diff protocol, anti-entropy worker, Repairer interface |
| 7 | Component | [07-component-crdt.md](07-component-crdt.md) | `internal/crdt`: four CRDT types, hybrid logical clock, canonical encoding, metadata document, replicator |
| 8 | Component | [08-component-saga.md](08-component-saga.md) | `internal/saga`: orchestrator, log, recovery, bucket-migration workflow, step interfaces |
| 9 | Component | [09-component-storage.md](09-component-storage.md) | `internal/storage`: shard format, chunk store, striping, manifest, placement, put and get paths |

The Container level has two views of the same containers, because one picture of all
relationships does not stay readable.

## Format

- [model.json](model.json) is the C4 model: every element, relationship and diagram.
  It is a `c4-sdk` bundle, `schemaVersion: 1`. All items have status `planned`.
- Each diagram file has a Mermaid rendering, a terminal (Unicode) rendering, an
  element table and a numbered relationship table. Edge numbers in the pictures
  match the table rows.
- [diagrams/](diagrams/) holds the Mermaid sources for the terminal renderings made
  by the `draw-visual` tool. Diagram 1 has one source. Diagrams 2 to 9 are split
  into two to four parts, because the text renderer draws a dense graph with
  crossing arrows that cannot be read. Each part is a simple fan-in, fan-out or
  chain, at most 80 columns wide, and each numbered relationship appears in exactly
  one part.
- Component diagrams contain only parts that PLAN.md names.

## Validation

Checked on 2026-11-21:

| Check | Tool | Result |
| --- | --- | --- |
| `model.json` decodes | `decodeArchitecture` from `c4-sdk@0.1.0` (`c4-sdk/core`) | `ok: true`, 0 errors, 0 warnings |
| Every diagram builds a scene | `buildScene` from `c4-sdk/core`, all 9 diagrams | 9 of 9 succeed |
| Mermaid blocks parse | `mermaid.parse`, Mermaid 11, all 9 blocks and all 25 sources | all parse |
| Terminal embeds are current and fit | `render.py --check` with `DRAW_VISUAL_MAX_WIDTH=80` | 25 embeds pass |

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
   eight neighbour elements, `n-api`, `n-storage`, `n-erasure`, `n-gossip`,
   `n-merkle`, `n-crdt`, `n-saga` and `n-env`, tagged `external` and `neighbour`.
   Each one stands for the container with the matching `c-` ID, is used only in
   component views, and links back to that container's views. This is a workaround
   for a limit in the tool. Nick decides whether to keep it or to change `c4-sdk`.
2. **Planned key entry.** Every legend has a `Planned` entry, because `c4-sdk` draws
   planned elements and relationships with dashed lines.
3. **Technology on in-process relationships.** Relationships between Go packages in
   the one `objnode` process carry the technology `Go, in-process`. The DST harness
   relationship to `internal/env` carries `Go interface implementation`.

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
