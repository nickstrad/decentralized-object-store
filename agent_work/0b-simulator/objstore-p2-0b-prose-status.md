# Brief — Chunk 0b PROSE, part 3: bring the README and the site front page up to date

Seat: Gemini 4 Neon, prose writer (fresh). Worktree: the current directory. Edit exactly
two files, `README.md` and `docs/index.md`. Do NOT run any git write command; leave the
edits in the working tree.

What changed: the deterministic simulator has landed, with the book's first chapter.
- Built now: `internal/env` (the Clock, Rand, Transport, Disk interfaces protocol code is
  written against), `internal/sim` (single-threaded discrete-event simulator: one seed,
  virtual clock, fault-injecting network and disk, a nemesis working inside a fault budget,
  event log and replay digests), `internal/sim/simtest` (seed sweeps, determinism check,
  replay command), `cmd/objsim` (run a scenario from a seed).
- Book chapter 1, "Deterministic Simulation Testing", is published
  (`book/chapters/01-deterministic-simulation.md`).
- Still planned, in this order: SWIM gossip membership, Reed-Solomon erasure coding, OCC
  metadata, Merkle-tree anti-entropy, Sagas, then integration, the end-to-end suite and
  the finished book. No storage code exists yet.
- New commands a reader can run (verified):
  `docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3`
  (runs a 100-node chaos simulation and prints two digests; the same seed always prints
  the same digests) and
  `docker compose run --rm dev go test ./internal/sim -run '^TestReplaySameProcess$' -seed=3 -count=1`.

Edits:
- `README.md`: first paragraph of "Current status" and the status table (simulator row
  becomes Built, with its scope in a few words; gossip becomes "Planned (next)"); add the
  objsim command to the Docker section with one sentence; in "Where to read", the book now
  has its first chapter — link it as `book/chapters/01-deterministic-simulation.md`.
- `docs/index.md`: the paragraph saying only foundation tooling exists, and the Book
  bullet ("none has landed yet") — say the simulator is built and chapter 1 is published,
  linking it as `../book/chapters/01-deterministic-simulation.md`; add the objsim command
  to "Run locally".
Change nothing else; keep the tone and length. Plain short sentences. No mention of this
machine, agents, models or briefs. Then run
`docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src` — it must
report no findings.
