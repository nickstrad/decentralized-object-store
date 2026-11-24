# Review 1 — prose cleanup and rebrand (Fable reviewing Neon)

Verdict: **NITS**

Scope reviewed: the whole working-tree diff of the prose pass (README, front page, preface,
five chapters, ten C4 files, model.json strings, three Go comments) against the brief
`objstore-cleanup-prose.md`.

Checked and clean:

- `check-prose.py HEAD`: no structural finding. Every fenced block (code excerpts, `sh`
  commands, Mermaid, terminal renderings) is byte-identical; model.json keeps its keys,
  list shapes and frozen values; five process-only note strings were removed from notes
  arrays, nothing else.
- Numbers: none changed or removed in the README, the front page, the preface or any
  chapter. In the C4 files every removed number is a date, a plan section, a chunk number
  or a relationship number inside a deleted "differences from the target design" sentence.
  Read each deleted sentence: the facts that mattered still stand in the relationship
  tables (spot-checked: Merkle relationship 7 names Clock, Rand and Transport; OCC
  relationship 2 names Disk and the state blob; OCC relationships 21 and 22 carry the
  routing flip and the quota code).
- Rebrand complete in titles and names; chapter 7's title, file and slug untouched as
  instructed. Study framing gone from README, front page and preface.
- Go: three comment edits, no code change.

Nits (one fix round):

1. `README.md` — the section "What is built" with a constant "Status: Built" column is a
   build-progress table. Present it as what the repository contains: no status column, a
   heading that is not about build progress. Row scope text stays as it is.
2. `docs/c4/README.md` — the inventory table's constant "State: built" column, same point.
3. `docs/c4/model.json` — source `plan` now reads "Object Store architecture notes" while
   its URL still points at PLAN.md. A reader-facing model should not point at the plan.
   Point the source at the book (`https://nickstrad.github.io/object-store/book/`) and
   word its title and notes to match. The `id` stays (214 references).
4. `docs/c4/model.json` — the bundle title and the nine diagram titles still end in
   "as built"; the Markdown pages dropped that label. Trim it from the ten titles. The
   `revision` value is an identifier and stays.

Left as they are, for the owner to judge (recorded in the done marker):

- The chapters' "Adversarial testing" bullets report what independent test harnesses
  found, run by run, with counts. They name no agent, seat or review step, and rewording
  them would touch numbers and claims, which this pass may not do.
- Test file names and identifiers that carry process-era words (`secondeyes_*_test.go`,
  `TestArbitration…`, `reviewerShape`, `review2_test.go`) are code, not prose.

NITS CLOSED (2026-11-24): fresh prose-seat fix round closed all four; re-checked with check-prose.py (its one flagged value is the instructed source URL), site lint clean, README table rows and scope cells unchanged.
