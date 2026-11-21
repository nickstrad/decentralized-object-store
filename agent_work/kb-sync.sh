#!/bin/bash
# Keep the vendored kb tool and the in-repo knowledge store in sync.
#   kb-sync.sh vendor [SHA]  re-archive the kb repo at SHA (default: its HEAD) into kb/, record the SHA
#   kb-sync.sh index         rebuild the in-repo index over docs/knowledge/*.md with the vendored kb
#   kb-sync.sh status        vendored SHA vs kb repo HEAD, and whether the index is stale
# Operational script (not reader-facing). KB_REPO overrides the kb repo location.
# The tool is built into .kb/bin/ (ignored via .kb/.gitignore); it needs Go and cgo (sqlite fts5).
# Uses the host Go if present, otherwise `docker compose run --rm dev` (Go in the image).
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
up=${KB_REPO:-/root/Software/kb}
sha_file="$repo/agent_work/kb-vendor.sha"
bin="$repo/.kb/bin/kb"

build() {
  local sha stamp="$repo/.kb/bin/.built-sha"
  sha=$(cat "$sha_file")
  if [ -x "$bin" ] && [ "$(cat "$stamp" 2>/dev/null)" = "$sha" ]; then return; fi
  mkdir -p "$repo/.kb/bin"
  if command -v go >/dev/null 2>&1; then
    (cd "$repo/kb" && CGO_ENABLED=1 go build -buildvcs=false -tags fts5 -o "$bin" ./cmd/kb)
  else
    (cd "$repo" && docker compose run --rm dev bash -c 'cd kb && go build -buildvcs=false -tags fts5 -o /src/.kb/bin/kb ./cmd/kb')
  fi
  echo "$sha" > "$stamp"
}

case "${1:-}" in
  vendor)
    sha=$(git -C "$up" rev-parse "${2:-HEAD}")
    rm -rf "$repo/kb"; mkdir "$repo/kb"
    git -C "$up" archive "$sha" | tar -x -C "$repo/kb"
    echo "$sha" > "$sha_file"
    rm -rf "$repo/.kb/bin"
    echo "vendored kb @ $sha" ;;
  index)
    build
    (cd "$repo" && "$bin" reindex --all) && touch "$repo/.kb/bin/.indexed" ;;
  status)
    v=$(cat "$sha_file" 2>/dev/null || echo none); u=$(git -C "$up" rev-parse HEAD)
    echo "vendored: $v"; echo "upstream: $u"
    [ "$v" = "$u" ] && echo "vendor: up to date" || echo "vendor: DRIFT (run: agent_work/kb-sync.sh vendor)"
    n=$(ls "$repo"/docs/knowledge/*.md 2>/dev/null | grep -vc /README.md || true); echo "docs/knowledge entries: $n"
    db="$repo/.kb/bin/.indexed"  # stamp written by `index`
    if [ ! -f "$db" ] || [ ! -f "$repo/.kb/stores/default/kb.sqlite" ]; then echo "index: missing (run: agent_work/kb-sync.sh index)"
    elif [ -n "$(find "$repo/docs/knowledge" "$repo/.kb/stores/default/config.toml" -newer "$db" 2>/dev/null | head -1)" ]; then
      echo "index: STALE (docs/knowledge or store config newer than index; run: agent_work/kb-sync.sh index)"
    else echo "index: fresh"; fi ;;
  *) echo "usage: kb-sync.sh vendor [SHA] | index | status"; exit 2 ;;
esac
