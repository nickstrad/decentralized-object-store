# Brief — Chunk 6, small fix: entity-tag lists with commas inside quotes

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/06-integration`).
Binding rules: `agent_work/06-integration/objstore-p2-builder-rules.md` (no comments in Go,
Docker only, test first, commit here, never push). Stage only the files you change, by
explicit path. Do not edit `agent_work/gates/`, `agent_work/orch/` or `agent_work/adversarial/`
(another process is editing gate scripts in this worktree at the same time).

The independent harness (`agent_work/adversarial/06-integration/`, read `ATTACKS.md` and the
four BROKEN lines in `evidence/run-all.txt`) found one defect: a VALID `If-Match` /
`If-None-Match` list whose entity-tags contain a comma inside the quotes (RFC 9110
section 8.8.3: an entity-tag is `[ W/ ] DQUOTE *etagc DQUOTE`, and `etagc` includes the
comma) is rejected with 400 on the four mutation routes, because the list is split on
commas before the quoted strings are read.

Do exactly this:
1. Regression tests FIRST that fail on the current code: the harness's four reproductions
   as rows of the HTTP status-table test; a table test of the parser itself covering
   quoted commas, several tags with optional whitespace, weak validators, `*`, empty list
   elements (RFC 9110 5.6.1 says a recipient must accept and ignore a reasonable number
   of empty elements — decide and state it), an unterminated quote, bytes outside
   `etagc`, and a trailing comma; and extend the fuzz model so the parser is compared
   with an independent tokeniser on arbitrary header values (same accept/reject decision
   and the same tags).
2. Fix the parser as a single left-to-right scan of entity-tags (no split on commas).
   A header that is present must still never degrade to an unconditional write.
3. Through Docker: `gofmt -l .` empty, `go vet ./...`, `go test ./internal/storage/...
   ./internal/node/... ./internal/noderuntime/... ./cmd/...`, the same with `-race`, and
   `go test ./internal/storage/httpapi -run '^$' -fuzz '^FuzzObjectAPI$' -fuzztime 60s -fuzzminimizetime 5s`.
   Then on the host `agent_work/adversarial/06-integration/run-all.sh`: record its SUMMARY
   line (expect no BROKEN) and restore its evidence files afterwards
   (`git checkout -- agent_work/adversarial`).
4. Commit tests and fix, append a short "Entity-tag list fix" section to
   `agent_work/06-integration/build-notes.md` (what, tests, the harness summary) and commit.
Final message: commit list and one line. Do not run the full gate; the orchestrator does.
