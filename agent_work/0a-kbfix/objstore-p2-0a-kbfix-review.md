# Brief — REVIEW of a two-line tooling fix (kb index build)

Seat: Gemini 4 Neon, reviewer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0a-kbfix (branch `chunk/0a-kbfix`).
READ-ONLY: the ONLY file you create is `agent_work/0a-kbfix/REVIEW-1.md`.

Review `git diff main..HEAD -- Dockerfile agent_work/kb-sync.sh` (one commit). Context:
`agent_work/kb-sync.sh index|search` build and run the vendored `kb/` tool inside the dev
container; the build failed with `sqlite3.h: No such file or directory` because the image
had no SQLite development header. The fix adds `libsqlite3-dev` to the image's apt
packages and makes `kb-sync.sh` build the image before using it.

Decide: (1) does the fix do what it says — run `agent_work/kb-sync.sh index` and
`agent_work/kb-sync.sh search "gate stage on main"` and report what they print; (2) does
adding the package change anything about the product build or the gates (the root module
must stay standard-library only; `CGO_ENABLED=1` was already set) — run
`GATE_ONLY=G0,G6 agent_work/gates/gate.sh review-check` and report; (3) anything wrong
with building the image inside `in_dev`.

Output `agent_work/0a-kbfix/REVIEW-1.md`: first line exactly `VERDICT: PASS`,
`VERDICT: NITS` or `VERDICT: FAIL`; then findings with severity, or "none"; then what you
ran. Remove `agent_work/review-check/` if your gate run created it.
