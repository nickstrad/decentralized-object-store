#!/usr/bin/env python3
"""Write one stage brief for a Wave B chunk into agent_work/<slug>/ of its worktree.

Usage: mkstage.py <slug> <stage> [k=v ...]
  stage: review | rereview | adversarial | prose-comments | prose-chapter | prose-status
  k=v:   base=<commit the reviewed range starts at>  n=<review number>
Prints the path of the brief it wrote. Operational script; briefs are not reader-facing.
"""
import sys, os, textwrap
C = {
 "01-gossip":  dict(num=1, pkg="internal/gossip", pkgs="`internal/gossip`", plan="Chunk 1", name="SWIM gossip membership",
    chap="02-swim-gossip", title="SWIM Gossip Membership", c4="docs/c4/04-component-gossip.md",
    papers="Das, Gupta, Motivala — SWIM (2002); the Lifeguard paper (Dadgar et al.) as the extension this project does NOT build; van Renesse et al. on gossip-style failure detection; Demers et al. on epidemic algorithms"),
 "02-erasure": dict(num=2, pkg="internal/erasure internal/storage", pkgs="`internal/erasure` and `internal/storage`", plan="Chunk 2", name="Reed-Solomon erasure coding and the chunk store",
    chap="03-reed-solomon-erasure-coding", title="Reed-Solomon Erasure Coding", c4="docs/c4/05-component-erasure.md and docs/c4/09-component-storage.md",
    papers="Reed and Solomon (1960); Plank's tutorial on Reed-Solomon coding for fault tolerance in RAID-like systems and its correction note; Blömer et al. on Cauchy Reed-Solomon; the Backblaze and Ceph erasure-coding write-ups; Thaler et al. on rendezvous hashing"),
 "03-merkle":  dict(num=3, pkg="internal/merkle", pkgs="`internal/merkle`", plan="Chunk 3", name="Merkle-tree anti-entropy",
    chap="04-merkle-anti-entropy", title="Merkle-Tree Anti-Entropy", c4="docs/c4/06-component-merkle.md",
    papers="Merkle (1987) on tree signatures; the Dynamo paper (DeCandia et al.) on anti-entropy with Merkle trees; Cassandra and Riak repair write-ups; RFC 6962 on leaf/inner domain separation"),
 "04-occ":     dict(num=4, pkg="internal/occ", pkgs="`internal/occ`", plan="Chunk 4", name="optimistic concurrency control for metadata",
    chap="05-optimistic-concurrency-control", title="Optimistic Concurrency Control", c4="docs/c4/07-component-occ.md",
    papers="Kung and Robinson (1981) on optimistic concurrency control; Parker et al. on version vectors; Kulkarni et al. on hybrid logical clocks; RFC 9110 on ETags and conditional requests; the Dynamo paper on siblings and Shapiro et al. on CRDTs as the two roads NOT taken (the design is last-writer-wins with every discard counted and logged — no chapter may describe siblings as the design); Kleppmann on the lost-update cost of last-writer-wins"),
 "05-saga":    dict(num=5, pkg="internal/saga", pkgs="`internal/saga`", plan="Chunk 5", name="the Saga engine and bucket migration",
    chap="06-sagas", title="Sagas", c4="docs/c4/08-component-saga.md",
    papers="Garcia-Molina and Salem (1987), Sagas; Gray on compensating transactions; Helland, Life Beyond Distributed Transactions; write-ups on idempotency keys and durable workflow engines"),
 "06-integration": dict(num=6, pkg="internal/node internal/storage cmd/objnode", pkgs="`internal/node`, the HTTP object API in `internal/storage`, and `cmd/objnode`", plan="Chunk 6", name="integration: the object store and sync engine",
    chap="07-object-store-and-sync-engine", title="The Object Store and Sync Engine", c4="docs/c4/02-container.md, docs/c4/03-container-runtime.md and every component diagram's planned relationships",
    papers="DeCandia et al., Dynamo (the leaderless design this project follows in shape); Lakshman and Malik, Cassandra; the S3 conditional-requests documentation and RFC 9110; Kleppmann on the lost-update cost of last-writer-wins; the FoundationDB and TigerBeetle write-ups on running one protocol core under a simulator and a real runtime")
}
T = {}
T["review"] = """# Brief — {plan} REVIEW {n}: {name}

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch `chunk/{slug}`).
READ-ONLY review: do not edit code, do not commit, no git write commands. The ONLY file you
create is `agent_work/{slug}/REVIEW-{n}.md`. Write it before you stop.

## What you are reviewing
`git diff {base}..HEAD -- internal cmd agent_work/gates` (an Opus builder's commits; `git
log {base}..HEAD`). Product Go is comment-free ON PURPOSE (another seat writes comments
later): not a finding. Read:
- the builder's brief `agent_work/{slug}/objstore-p2-{slug}-build.md` and rules
  `agent_work/{slug}/objstore-p2-builder-rules.md`;
- PLAN.md: section 2 (testing architecture), the item "{plan}" in 4.2 (deliverable, the
  named tests, the adversarial list), the 2.6 error rows and the section 7 decisions the
  item cites;
- `agent_work/{slug}/build-notes.md` (claims, measurements, uncovered branches, requests);
- the simulator contract the code builds on: `agent_work/0b-simulator/build-notes.md`
  ("Semantics Wave B builders must know") and `agent_work/0b-simulator/frozen-api.txt`;
- the code and tests in {pkgs}.
The orchestrator's gate result for this tree is `agent_work/{slug}/gates/result.json`.
You may run tests through Docker only: `docker compose run --rm -T dev go test ./{pkg0}/...`
(also `-race`, `DST_SEEDS=50`, `-rapid.checks=2000` on packages that import rapid).

## Questions (number your findings)
1. Correctness against the plan item: is every part of the Deliverable built, and built
   right? Name protocol or algorithm errors concretely (a state, a message order, an input)
   with a reproduction — a test snippet or a seed where possible.
2. The headline claims: for each claim in the build notes, does the named test really prove
   it? Find tests that cannot fail, assert nothing, use a bound so loose it proves nothing,
   or silently skip (zero iterations, empty tables). Do the canaries fail for the planted
   reason? Does every test named in the plan item exist and test what its name says?
3. Determinism and simulator use: any path where two runs of one seed can differ; any
   misuse of the simulator contract (assuming callbacks run synchronously, ignoring
   `ErrTimeout` ambiguity, relying on unsynced disk data surviving a crash, sharing memory
   across nodes, drawing from the wrong stream).
4. Failure handling: PLAN 2.6 rows — every sentinel produced and tested; malformed or
   hostile input is an error, never a panic or unbounded work; resource bounds (memory,
   message size, retries).
5. Interfaces for the integration chunk (Chunk 6 wires all five protocols into one node on
   the simulator AND on a real UDP/TCP/file runtime): are the small interfaces this chunk
   declares (`Membership`, `Repairer`, step interfaces, …) implementable by the other
   chunks as planned? Anything that will not survive a real runtime?
6. Rules: standard library only; no comments; no goroutine/channel/mutex/wall clock/global
   rand/float in protocol code; panics only for violated invariants, each allowlisted; the
   frozen simulator packages untouched.

## Output: agent_work/{slug}/REVIEW-{n}.md
First line exactly one of `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`. Then numbered
findings, each with severity (BLOCKER / MAJOR / NIT / NOTE), file and line, what is wrong, a
concrete reproduction and the smallest fix. FAIL needs at least one BLOCKER or MAJOR: wrong
behaviour, a headline claim its test does not prove, a determinism leak, an interface the
integration cannot use. Do not pad with style remarks. End with what you verified by
running versus by reading.
"""
T["rereview"] = """# Brief — {plan} REVIEW {n}: {name}, after a repair round

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch `chunk/{slug}`).
READ-ONLY: the ONLY file you create is `agent_work/{slug}/REVIEW-{n}.md`.

Earlier reviews are `agent_work/{slug}/REVIEW-*.md`; adversarial results, if any, are
`agent_work/adversarial/{slug}/RESULTS.md` and `ATTACKS.md`. The repair brief is the newest
`agent_work/{slug}/objstore-p2-{slug}-repair*.md`; the repair is `git log {base}..HEAD`,
diff `git diff {base}..HEAD -- internal cmd agent_work/gates`; its account is the newest
round section of `agent_work/{slug}/build-notes.md`. Product Go is comment-free on purpose.

Decide: (1) for each item of the repair brief — FIXED, PARTLY FIXED, NOT FIXED, or
CORRECTLY NARROWED (the claim is restated in the build notes in a form the tests prove);
check code and tests, not notes; does each regression test fail on the old code
(`git show {base}:<file>`)? (2) any new BLOCKER or MAJOR introduced (wrong behaviour, a
determinism leak, a test weakened to pass, an edit to the frozen simulator packages).
Run tests through Docker only (`docker compose run --rm -T dev go test ./{pkg0}/...`, also
`-race`, `DST_SEEDS=50`, `-rapid.checks=2000` where rapid is imported).

Output `agent_work/{slug}/REVIEW-{n}.md`: first line exactly `VERDICT: PASS`,
`VERDICT: NITS` or `VERDICT: FAIL`; the item table with one line of evidence each; new
findings numbered with severity (BLOCKER / MAJOR / NIT / NOTE), file and line,
reproduction, smallest fix; what you ran versus read. FAIL needs a BLOCKER or MAJOR.
"""
T["adversarial"] = """# Brief — {plan} ADVERSARIAL: break {name}

Seat: Astra (gpt-6-astra), adversarial tester (fresh). You are in a git worktree (branch
`chunk/{slug}`). You write ONLY under `agent_work/adversarial/{slug}/`. Do not edit anything
else, do not commit, do not run git write commands.

## Target
{pkgs}: reviewed code of a Go project (standard library only) tested by deterministic
simulation. Read the plan item "{plan}" in PLAN.md section 4.2 — its "Adversarial (Astra)"
bullet is your attack list and is binding — then the builder's claims in
`agent_work/{slug}/build-notes.md` (the claims table: each row is a claim the project will
publish in its book), then the code and tests. The simulator the tests run on is described
in `agent_work/0b-simulator/build-notes.md` and `frozen-api.txt`; the earlier harness
`agent_work/adversarial/0b-simulator/` shows a working layout (a nested module whose path
sits under the product's import path, with a `replace` to `../../..`, so `internal/`
packages can be imported). Everything runs through Docker from the worktree root:
`docker compose run --rm -T dev <cmd>`. Not the host Go.

## What to attack
Number the claims C1..Cn yourself from the claims table and the plan item, and attack each
several ways; every item of the plan's adversarial bullet must appear as at least one
attack. Prefer attacks through the package's exported API on the simulator with your own
seeds, schedules, sizes and hostile inputs; outside libraries are allowed ONLY inside your
nested module (for example an independent reference implementation as a differential
oracle). Statistical claims: state your tolerance and sample size.

## What to deliver, under agent_work/adversarial/{slug}/
- Harness code in ONE nested Go module of your own; bash drivers `aNN-<slug>.sh`, each
  runnable by someone else with no arguments from the worktree root, cleaning up after
  itself, printing exactly one line per attack:
  `HELD <claim> <attack-name>: <one sentence>` or
  `BROKEN <claim> <attack-name>: <one sentence with the smallest reproduction, incl. seed>`.
  Exit 0 when everything HELD, 1 when anything is BROKEN, 2 on a setup error. Decide from
  what the code actually did, never from what you expect. A driver must not score a
  correct rejection (an error returned for an invalid input) as BROKEN.
- `run-all.sh`: runs every driver, collects lines into `evidence/run-all.txt`, prints
  `SUMMARY held=N broken=M setup_errors=K ...`. Write evidence only to files that are NOT
  rewritten differently on each run unless they are the run's result (keep the tree quiet).
- `ATTACKS.md`: the numbered claims, the attacks by claim (what you tried, what happened
  when YOU ran it, tolerances), exact minimal reproductions for every BROKEN, and what you
  could not test and why.
Breadth first, then depth on whatever cracks. Total `run-all.sh` runtime under about 20
minutes on 4 shared cores. Run it yourself before you stop and report real output.
"""
T["prose-comments"] = """# Brief — {plan} PROSE, part 1: code comments for {name}

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/{slug}`). You ADD COMMENTS to Go files. You change NO code. Do NOT run any git write
command: leave your edits in the working tree; they are committed for you under your
byline. Other writers may be working under `book/`, `README.md` and `docs/` at the same
time — do not touch those.

## Files
Every `.go` file under {pkgs} (sub-packages and test files included), plus any
`cmd/*/help.txt` this chunk created. The code was written with no comments on purpose.
Do not touch files outside these packages (comments elsewhere already exist).

## Source material (read first)
`agent_work/{slug}/build-notes.md` (the builder's account of intent, design choices,
claims, every round section, narrowed claims), `agent_work/{slug}/REVIEW-*.md`,
`agent_work/adversarial/{slug}/RESULTS.md`, and the plan item "{plan}" in PLAN.md 4.2 with
the section 7 decisions it cites. Existing commented packages (`internal/sim`,
`internal/env`) show the house style. Where a claim was narrowed, comments state the
narrowed form.

## What to write
- A package comment above the `package` clause of each package's main file: what it is,
  how it fits, the one or two rules a user must know.
- A doc comment on every exported identifier, starting with its name: what it does, what
  the caller may rely on, what it returns on failure. One to four lines is typical.
- Inside functions: only where the code alone does not say WHY — an invariant, an ordering
  that matters, a protocol rule and where it comes from, a subtle boundary.
- Unexported helpers: one line when the name is not enough.
- Test files: one or two lines above each test function on what it proves; for a canary,
  what bug it plants.
Plain, concrete English; short sentences; no hype. Concise above all.

## Hard rules (checked mechanically; a violation discards the whole pass)
1. Comments only. Every line you add or change in a `.go` file is a FULL-LINE `//` comment
   (or a blank line next to one). No end-of-line comments after code, no `/* */`, no edits
   to code, strings, identifiers, imports, build tags or directives; do not move or reflow
   code; no comment starting with `//go:` or `// Code generated`.
2. After editing, run and fix until all pass:
   - `docker compose run --rm -T dev gofmt -l .` prints nothing;
   - `docker compose run --rm -T dev go build ./...` and `go vet ./...` succeed;
   - `git diff -U0 -- '*.go' | grep '^[+-]' | grep -v '^+++\\|^---' | grep -vE '^[+-][[:space:]]*(//.*)?$'`
     prints NOTHING;
   - `docker compose run --rm -T dev go test ./{pkg0}/...` passes.
3. Self-contained: never mention this machine, absolute paths, agents, models, reviews,
   briefs, chunk numbers or how the text was produced; no file under `agent_work/`.
4. Accuracy over coverage: never state a guarantee the tests do not show.
"""
T["prose-chapter"] = """# Brief — {plan} PROSE, part 2: book chapter "{title}"

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/{slug}`). You write ONE chapter file and its diagram sources:
- `book/chapters/{chap}.md`
- `book/chapters/diagrams/{chapnum}-*.mmd`
Touch nothing else (another writer is adding comments to the Go files at the same time —
read the code, do not edit it). Do NOT run any git write command. Do not edit
`book/manifest.json`.

## The book and this chapter
The book is the project's learning artifact: a concise study book that teaches each topic
and shows where it lives in this repository, published chapter by chapter as each building
block lands. Read the published chapters under `book/chapters/` first and match their
shape, tone and length. The reader is a working engineer who wants to LEARN the topic and
then find it in the code. Conciseness is the top constraint: about 1,800–2,800 words.

First line of the file, exactly: `# {title}`

## Chapter shape (six parts, your own headings, as in the earlier chapters)
1. The idea, concisely: what it is, why it exists, the core mechanism.
2. How it maps to this project: which packages and files implement it, the design choices
   made here and what each costs, how it is tested on the simulator (earlier chapter).
3. Three to five short code excerpts that carry the idea (each under ~20 lines, copied
   exactly, with the file path). No long listings.
4. One or two diagrams where they genuinely help. Each appears twice: a ```mermaid block
   and, directly under it, a terminal rendering generated from a `.mmd` source saved as
   `book/chapters/diagrams/{chapnum}-<name>.mmd`: put this marker and an empty fenced
   block in the chapter, then run the renderer, which fills it in:
       <!-- draw-visual: diagrams/{chapnum}-<name>.mmd -->
       ```text
       ```
       python3 /root/.claude/skills/draw-visual/scripts/render.py --update book/chapters/{chap}.md
       DRAW_VISUAL_MAX_WIDTH=80 python3 /root/.claude/skills/draw-visual/scripts/render.py --check book/chapters/{chap}.md
   Keep sources simple (`graph TD`, or `sequenceDiagram` for message exchanges; at most
   about eight nodes; short labels; no subgraphs) so the text fits 80 columns; look at the
   rendering and simplify if garbled. The tool path is for you only; never in the chapter.
5. "What the tests show, and what they do not": the verified evidence, stated exactly, with
   every narrowed claim in its narrowed form and why. See Evidence.
6. "Go deeper in the repo": exact pointers — files, the named tests, fuzz targets, the
   adversarial harness `agent_work/adversarial/{slug}/`, one or two commands to run
   (through `docker compose run --rm dev ...`; check each against the code). Then "Sources
   and further reading": {papers} — one line each on what to take from it. Cite only works
   you are sure exist; no invented URLs.

## Evidence (verified; use these and nothing stronger)
`agent_work/{slug}/build-notes.md` (claims table, measurements, every round section),
`agent_work/{slug}/REVIEW-*.md`, `agent_work/adversarial/{slug}/RESULTS.md`. Every number
in the chapter comes from those files. Narrowed claims go in narrowed form. Say what is
proven only on the simulator and what waits for the integrated node. Building blocks not
yet in `book/chapters/` are "coming chapters", without links; published chapters may be
linked relatively (`01-deterministic-simulation.md`).

## Rules
- Plain, concrete English; short sentences; no hype. Teach, do not sell.
- Self-contained: never mention this machine, absolute paths, agents, models, reviewers,
  briefs, chunk numbers, or how the text was produced. Repository-relative paths; relative
  links only to files that exist (from `book/chapters/`: `../../{pkg0}/<file>.go`).
- When done run `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src`.
  It will report ONE expected finding, `chapter-unlisted` for this chapter. Fix anything else.
"""
T["prose-status"] = """# Brief — {plan} PROSE, part 3: README and site front page status

Seat: Gemini 4 Neon, prose writer (fresh). Worktree: the current directory. Edit exactly
two files, `README.md` and `docs/index.md`. Do NOT run any git write command.

What changed: {name} has landed in {pkgs}, with its book chapter
`book/chapters/{chap}.md` ("{title}"). Read `agent_work/{slug}/build-notes.md` (first
sections) for what it does, and `book/manifest.json` plus `book/chapters/` for which
chapters exist. Read the plan's chunk list (PLAN.md 4.2 headings) for what is still planned.

Edits, in the style and length already there: in `README.md` the status paragraph and the
status table (this building block becomes Built, with its scope in a few words; mark as
"Planned (next)" nothing unless you can tell from the table which block is not Built and
comes first), and the "Where to read" book bullet (list the published chapters with links
`book/chapters/<file>.md`); in `docs/index.md` the status paragraph and the Book bullet
(links as `../book/chapters/<file>.md`). Do not invent commands; add none unless
`agent_work/{slug}/build-notes.md` records one a reader can run through
`docker compose run --rm dev ...`. Change nothing else. No mention of this machine, agents,
models or briefs. Then run
`docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` — the only
acceptable finding is `chapter-unlisted` for a chapter not yet in the manifest.
"""
def main():
    slug, stage = sys.argv[1], sys.argv[2]
    kv = dict(a.split("=", 1) for a in sys.argv[3:])
    c = dict(C[slug]); c.update(slug=slug, pkg0=c["pkg"].split()[0], chapnum=c["chap"][:2],
                                 base=kv.get("base", "main"), n=kv.get("n", "1"))
    name = {"review": f"review{c['n']}", "rereview": f"review{c['n']}"}.get(stage, stage)
    path = f"agent_work/{slug}/objstore-p2-{slug}-{name}.md"
    os.makedirs(os.path.dirname(path), exist_ok=True)
    open(path, "w").write(T[stage].format(**c))
    print(path)
main()
