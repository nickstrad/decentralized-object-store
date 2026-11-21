# Brief — Chunk 0a PROSE: the README and the site front page

Seat: Gemini 4 Neon, prose writer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0a-foundation (branch `chunk/0a-foundation`).
You write exactly TWO files and commit them in ONE commit:
- `README.md` (replace the current planning-phase text)
- `docs/index.md` (new: the front page of the project's GitHub Pages site)
Touch nothing else. Commit with `git add README.md docs/index.md` and a message whose
subject is `docs: write the README and the site front page for the foundation chunk` and
whose LAST line is exactly the trailer `Prose-By: Gemini 4 Neon` (after a blank line).
Do not push.

## What this project is (verified facts; do not go beyond them)
A learning project: a leaderless, multi-region object store in Go, standard library only,
built testing-first. Planned building blocks, each landing as its own chunk with a book
chapter: a deterministic simulator (next to land), SWIM gossip membership, Reed-Solomon
erasure coding (4 data + 2 parity shards across 3 regions), Merkle-tree anti-entropy,
optimistic concurrency control for metadata (ETags, conditional writes, version vectors;
concurrent versions are kept as siblings, never silently overwritten), Sagas for a
multi-step bucket migration, then integration, an end-to-end suite and the finished book.
The full plan is `PLAN.md`; the original specification is `docs/project-source.md`.

What exists TODAY (this chunk — tooling only, no storage code yet):
- One environment: Docker. `docker compose build dev` builds the image (Go 1.26.11);
  `docker compose run --rm dev <command>` runs anything in it, e.g.
  `docker compose run --rm dev go test ./...`. Nothing needs Go installed on the host.
- The landing gates: `agent_work/gates/gate.sh <name>` runs seven gates inside the
  container — G0 hygiene (gofmt, vet, build, a determinism lint, a panic allowlist, a
  dependency check, a comment check, self-containment and site lint), G1 unit tests with a
  coverage floor, G2 property tests (pgregory.net/rapid, 2,000 checks, seeded from the
  commit), G3 a 30-second fuzz run per fuzz target, G4 the race detector, G5 simulation
  seed sweeps (500 seeds), G6 proof the run happened in the project image — and writes
  `agent_work/<name>/gates/result.json`.
- `agent_work/gates/canaries.sh` tests the gates themselves: it plants one violation at a
  time in a throwaway copy and requires the owning gate to go red for the right reason.
- The only dependency is `pgregory.net/rapid`, used by tests only; product code imports
  the standard library and nothing else, and a gate checks it.
- The book: `book/manifest.json` is the table of contents, chapters live in
  `book/chapters/`. The site publishes each chapter when its chunk lands; the book has no
  chapters yet — the first, on deterministic simulation testing, arrives with the simulator.
- Architecture diagrams (C4 model) in `docs/c4/`; they show the TARGET design until each
  part is built.
- Site: https://nickstrad.github.io/decentralized-object-store/ — front page, Book, the
  C4 diagrams, the plan.

Read for accuracy before writing: `PLAN.md` section 1 and the table in 2.9, `compose.yaml`,
the header comments of `agent_work/gates/gate.sh` and `canaries.sh`, `docs/c4/README.md`,
`.github/scripts/build_site.py` (docstring: how pages and links are built).

## README.md — for someone who has just opened the repository
Short (about 60–100 lines). Sections: what this is (two or three sentences, including that
it is built to be learned from and that conflicts are handled with optimistic concurrency
control, not CRDTs); what is here now versus what is coming (honest: tooling only so far —
a short status list of the building blocks, each marked "planned" except the foundation);
how to work through Docker (the exact commands above, in code blocks, nothing invented);
where to read (the book on the site, the diagrams, the plan). No badges, no marketing.

## docs/index.md — the site front page
Shorter (about 30–50 lines). First heading `# Decentralized Object Store & Local-First Sync
Engine`. What the project is; how the site is organised (the Book grows one chapter per
landed chunk — say plainly that chapters are published as they land and none has landed
yet; the diagrams show the target architecture; the plan); how to run it locally through
Docker (two commands). Link with RELATIVE links to repository files, exactly these forms:
`../book/manifest.json` for the book, `c4/README.md` for the diagrams, `../PLAN.md` for the
plan, `../README.md` for the README (the site builder rewrites them to site pages). No
other links to files that might not exist.

## Rules
- Plain, concrete English; short sentences; no hype words; say what is not built yet.
- Self-contained: never mention this machine, absolute paths, agents, models, briefs, or
  how the text was produced. Refer to no file under `agent_work/` other than the two gate
  scripts named above.
- Every command you quote must be one listed in this brief.
- When done: `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src`
  must print no findings — run it and fix what it reports. Then commit as described.
