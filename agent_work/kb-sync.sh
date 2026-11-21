#!/bin/bash
# Keep the vendored kb tool and the in-repo knowledge store in sync.
#   kb-sync.sh vendor [SHA]  re-archive the kb repo at SHA (default: its HEAD) into kb/, record the SHA
#   kb-sync.sh index         rebuild the in-repo index over docs/knowledge/*.md with the vendored kb
#   kb-sync.sh status        vendored SHA vs kb repo HEAD, and whether the index is stale
#   kb-sync.sh search ARGS   run the vendored kb's search over the in-repo store
# Operational script (not reader-facing). KB_REPO overrides the kb repo location; `vendor`
# needs that checkout, the other subcommands work on a clean clone.
# The tool is built into .kb/bin/ (ignored via .kb/.gitignore) and both built and run inside
# the dev container (`docker compose run --rm dev`), like everything else in this repository,
# so the binary always matches the machine that runs it.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
up=${KB_REPO:-/root/Software/kb}
sha_file="$repo/agent_work/kb-vendor.sha"
bin="$repo/.kb/bin/kb"

in_dev() { (cd "$repo" && docker compose run --rm -T dev bash -c "$1"); }
build() {
  local sha stamp="$repo/.kb/bin/.built-sha"
  sha=$(cat "$sha_file")
  if [ -x "$bin" ] && [ "$(cat "$stamp" 2>/dev/null)" = "$sha" ]; then return; fi
  mkdir -p "$repo/.kb/bin"
  in_dev 'cd kb && CGO_ENABLED=1 go build -buildvcs=false -tags fts5 -o /src/.kb/bin/kb ./cmd/kb'
  echo "$sha" > "$stamp"
}

case "${1:-}" in
  vendor)
    [ -d "$up/.git" ] || [ -f "$up/.git" ] || { echo "kb-sync: no kb checkout at $up (set KB_REPO)" >&2; exit 2; }
    sha=$(git -C "$up" rev-parse "${2:-HEAD}")
    rm -rf "$repo/kb"; mkdir "$repo/kb"
    git -C "$up" archive "$sha" | tar -x -C "$repo/kb"
    echo "$sha" > "$sha_file"
    rm -rf "$repo/.kb/bin"
    echo "vendored kb @ $sha" ;;
  index)
    build
    in_dev '.kb/bin/kb reindex --all' && touch "$repo/.kb/bin/.indexed" ;;
  search)
    build
    shift
    in_dev ".kb/bin/kb search $(printf '%q ' "$@")" ;;
  status)
    v=$(cat "$sha_file" 2>/dev/null || echo none)
    echo "vendored: $v"
    if u=$(git -C "$up" rev-parse HEAD 2>/dev/null); then
      echo "upstream: $u"
      [ "$v" = "$u" ] && echo "vendor: up to date" || echo "vendor: DRIFT (run: agent_work/kb-sync.sh vendor)"
    else echo "upstream: unavailable (no kb checkout at $up; drift not checked)"; fi
    n=$(ls "$repo"/docs/knowledge/*.md 2>/dev/null | grep -vc /README.md || true); echo "docs/knowledge entries: $n"
    db="$repo/.kb/bin/.indexed"  # stamp written by `index`
    if [ ! -f "$db" ] || [ ! -f "$repo/.kb/stores/default/kb.sqlite" ]; then echo "index: missing (run: agent_work/kb-sync.sh index)"
    elif [ -n "$(find "$repo/docs/knowledge" "$repo/.kb/stores/default/config.toml" -newer "$db" 2>/dev/null | head -1)" ]; then
      echo "index: STALE (docs/knowledge or store config newer than index; run: agent_work/kb-sync.sh index)"
    else echo "index: fresh"; fi ;;
  *) echo "usage: kb-sync.sh vendor [SHA] | index | status | search ARGS"; exit 2 ;;
esac
