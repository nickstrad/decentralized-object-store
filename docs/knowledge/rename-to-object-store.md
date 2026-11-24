# Rename to Object Store and the prose cleanup (2026-11-23)

The project was renamed from "Decentralized Object Store & Local-First Sync Engine" to
**Object Store**. The old name described a local-first sync engine and a CRDT layer that
the design no longer has: metadata uses optimistic concurrency control with
last-writer-wins, and sync runs between nodes only.

## Old to new

| What | Old | New |
| --- | --- | --- |
| GitHub repository | `nickstrad/decentralized-object-store` | `nickstrad/object-store` (GitHub redirects the old URL) |
| Go module | `github.com/nickstrad/decentralized-object-store` | `github.com/nickstrad/object-store` |
| Site | `https://nickstrad.github.io/decentralized-object-store/` (no longer served) | `https://nickstrad.github.io/object-store/` |
| Checkout on the build machine | `/root/Software/decentralized-object-store` | `/root/Software/object-store` |
| Worktrees | `…/decentralized-object-store-work/` | `/root/Software/object-store-work/` |

Git history was not rewritten. Records under `agent_work/` (reports, logs, evidence,
briefs) keep the old name on purpose; code and scripts there were updated so they still
build and run. `PLAN.md` and `docs/project-source.md` are records too and keep the old name.

## What is easy to get wrong

- **The module path is also a string literal.** `internal/testutil/leakcheck/leakcheck.go`
  recognises the project's own goroutines by the module path inside a stack trace. A
  rename that only rewrites `import` lines leaves the leak check blind without failing to
  compile. Search for the bare path, not for import statements.
- **Nested modules.** The check harnesses under `agent_work/adversarial/*/` and the lint
  tools under `agent_work/gates/detlint` are separate Go modules with a `replace` back to
  the root; each `go.mod` names the root module and needs the same rename. `go build ./...`
  at the root does not compile them — vet each one.
- **The site lint hardcodes the repository URL** (`agent_work/gates/site_lint.py`,
  `REPO_URL` and two path patterns) to tell private links from public ones, and
  `.github/scripts/build_site.py` uses it for the footer and for links to files that are
  not site pages. `agent_work/gates/site-links.py` has the Pages base URL as its default.
- **Pages after a repository rename.** The site moves with the repository name and the old
  Pages URL stops serving (GitHub redirects the repository, not the Pages site). The new
  URL serves the last deployment at once; a push redeploys it.
- **The plan is no longer a site page.** `build_site.py` used to publish `PLAN.md` as
  `plan.html` with a navigation entry. It is the build's record, so it left the site; a
  link to it from a published page would now go to the repository.
- **Prose provenance still applies to a rename.** CI's `ci-prose.sh` fails a push in which
  a commit without the prose trailer touches `README.md`, `docs/` or `book/` prose, or in
  which a prose commit touches anything but prose and Go comments. So the rename is split:
  module path, scripts, `book/manifest.json` and `docs/c4/model.json` in ordinary commits;
  README, front page, book, C4 pages and Go comments in one prose commit; the prose seat's
  own report under `agent_work/` in an ordinary commit (mixing it into the prose commit is
  what made the check red at the end of the build).

## Checking a verbiage-only pass

`agent_work/09-cleanup/check-prose.py <base>` compares the reader-facing files with a base
commit: every fenced block must be byte-identical, numeric tokens outside fences are
listed when removed or added, and `docs/c4/model.json` must keep its structure with only
description strings changed. `agent_work/09-cleanup/sweep.sh` is the grep sweep for the old
name, study framing and build-process words; its second part is a list for a reader to
judge, because words such as chunk, orchestrator and owner also have technical meanings
here (chunk store, saga orchestrator, flip owner).
