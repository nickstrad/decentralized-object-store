# Brief — Chunk 6 PROSE: small revision of the chapter draft after a late fix

Seat: Gemini 4 Neon, prose writer (fresh). You are in a git worktree (branch
`chunk/06-integration`). The chapter `book/chapters/07-object-store-and-sync-engine.md`
was drafted by your seat under `agent_work/06-integration/objstore-p2-06-integration-prose-chapter.md`
(read that brief: all its rules still bind — shape, length, self-contained, exact code
excerpts, diagram rendering). After the draft, the code changed in one small round:
read section 15 of `agent_work/06-integration/build-notes.md` and
`git diff 8170771..HEAD -- internal/node/inventory.go`.

Your task: make the chapter true of the code at HEAD, changing as little as possible.
1. Every sentence about absence records, loss declaration, rebuild and their limits must
   match sections 14 and 15: a holder records absence only when its own read of the shard
   answers not found or corrupt (a disk read error is not evidence); a rebuild that
   finishes after the holder's own absence record arrived stores nothing; a holder whose
   disk keeps failing reads blocks a loss declaration like an unreachable holder does.
2. Every code excerpt copied from `internal/node/inventory.go` must match the file at
   HEAD exactly; re-copy any that changed.
3. If the chapter cites test names or counts for the loss-declaration evidence, add the
   second independent pass in one or two plain sentences: twenty further scenarios from an
   independent review now run as regression tests (`internal/node/secondeyes_*_test.go`),
   three of which exposed the defects above before they were fixed.
4. If a diagram source changes, re-run the renderer as the original brief describes.
Touch only that chapter file and its `book/chapters/diagrams/07-*.mmd` sources. Do NOT run
any git write command. Another writer is adding comments to the Go files at the same
time — read the code, do not edit it. Stay inside 1,800–2,800 words.
