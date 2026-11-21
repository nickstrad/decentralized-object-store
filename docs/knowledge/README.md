# Project knowledge

This folder holds the project's durable knowledge, one or more Markdown entries per landed
chunk. Each entry records what that chunk learned that the next builder or reader should not
have to rediscover:

- findings: what was measured or confirmed, with the command or test that shows it
- seeds that mattered: the simulator seeds that exposed a bug or pinned a behavior
- narrowed claims: where a claim had to be weakened, and the exact wording that held up
- pitfalls: mistakes that cost time and how to avoid them

Name files for the chunk, for example `chunk-3-findings.md`. Entries are plain Markdown; the
first `# ` heading is the title. Keep an entry to current knowledge: correct or extend an
existing file rather than adding a duplicate.

The vendored `kb` tool indexes `docs/knowledge/*.md` through `.kb/` (this README is excluded
from the index). Rebuild with `agent_work/kb-sync.sh index`, then search with
`.kb/bin/kb search "<question>"`.
