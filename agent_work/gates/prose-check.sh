#!/bin/bash
# Prose provenance check (PLAN 2.10, gate G9). Runs on the host because it reads git history;
# the Go tools it uses run in the dev container.
#
# Usage: agent_work/gates/prose-check.sh <base> [<head>]      (default head: HEAD)
#
# Over the commits base..head it proves:
#   (a) the tree just before the first prose commit has no comment in product Go;
#   (b) every prose commit (trailer "Prose-By: Gemini 4 Neon") changes only comments in Go
#       files (identical AST, directives, build files otherwise) and reader-facing files;
#   (c) no other commit, anywhere in the range, touches reader-facing prose, and none after
#       the first prose commit adds a comment to product Go (a comment may be removed with
#       the code it described; that is the one thing a repair is allowed to do to prose).
# The range must be linear: a merge commit can carry changes no parent shows, so any merge
# in the range is a finding (chunk branches are rebased, never merged into).
# With no prose commit in the range, (a) is checked on head.
#
# Who owns what:
#   prose seat only   README.md, docs/** and book/** except the paths below, cmd/*/help.txt
#                     (the builder creates help.txt with bare flag names before the first
#                     prose commit; after it, help.txt is prose-only)
#   either seat       docs/c4/** (diagram structure is the orchestrator's, the words around
#                     it are the prose seat's)
#   never prose       docs/knowledge/**, docs/project-source.md, book/manifest.json,
#                     book/chapters/.gitkeep, everything else
# Known limit, stated on purpose: a prose commit that edits cmd/*/help.txt changes what the
# binary prints. That is the rule working as designed (CLI help is prose), not a code change.
# Exit 0 clean, 1 findings, 2 setup error.
set -u
base=${1:-}; head=${2:-HEAD}
[ -n "$base" ] || { echo "usage: prose-check.sh <base> [<head>]" >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd -P)
root=$(cd "$here/../.." && pwd -P)
cd "$root" || exit 2
trailer='Prose-By: Gemini 4 Neon'
tmp=$(mktemp -d /tmp/prose-check.XXXXXX)
trap 'docker run --rm -v "$tmp:/work" objstore-dev:local rm -rf /work/bin /work/t >/dev/null 2>&1; rm -rf "$tmp"' EXIT
rc=0
fail() { echo "prose-check: $*"; rc=1; }

# Files only the prose seat may write.
is_prose_only() {
  case "$1" in
    docs/knowledge/*|docs/project-source.md|docs/c4/*|book/manifest.json|book/chapters/.gitkeep) return 1 ;;
    README.md|docs/*|book/*) return 0 ;;
  esac
  return 1
}
is_help() { case "$1" in cmd/*/help.txt) return 0 ;; esac; return 1; }
# Files a prose commit may touch besides Go comments.
is_reader_facing() {
  is_prose_only "$1" && return 0
  is_help "$1" && return 0
  case "$1" in docs/c4/model.json) return 1 ;; docs/c4/*) return 0 ;; esac
  return 1
}
is_product_go() {
  case "$1" in
    agent_work/*|kb/*|*/testdata/*) return 1 ;;
    *.go) return 0 ;;
  esac
  return 1
}
export_tree() { # export_tree <commit> <name>
  mkdir -p "$tmp/t/$2"
  git archive "$1" -- . ':(exclude)kb' ':(exclude)agent_work' | tar -x -C "$tmp/t/$2"
}
tool() { # tool <name> <args...>: run a gate tool in the container against exported trees
  docker run --rm -v "$tmp:/work" -w /work objstore-dev:local "/work/bin/$1" "${@:2}"
}

docker compose build -q dev || exit 2
docker compose run --rm -T -v "$tmp:/work" dev bash -c 'cd agent_work/gates/detlint && go build -o /work/bin/ ./...' || { echo "prose-check: cannot build tools" >&2; exit 2; }

commits=$(git rev-list --reverse "$base..$head") || exit 2
merges=$(git rev-list --merges "$base..$head")
[ -z "$merges" ] || fail "merge commits in the range (rebase instead): $(echo $merges | cut -c1-200)"
first_prose=
for c in $commits; do
  if git log -1 --format=%B "$c" | grep -qxF "$trailer"; then first_prose=$c; break; fi
done

if [ -z "$first_prose" ]; then
  export_tree "$head" head
  tool nocomments -root /work/t/head || fail "(a) product Go at $head has comments and there is no prose commit"
else
  export_tree "$first_prose^" pre
  tool nocomments -root /work/t/pre || fail "(a) the tree before the first prose commit $(git rev-parse --short "$first_prose") has comments"
fi

seen_prose=false
for c in $commits; do
  short=$(git rev-parse --short "$c")
  [ "$(git rev-list --parents -n1 "$c" | wc -w)" = 2 ] || continue
  [ "$c" = "$first_prose" ] && seen_prose=true
  files=$(git diff --name-only --no-renames "$c^" "$c")
  gochanged=false
  if git log -1 --format=%B "$c" | grep -qxF "$trailer"; then
    while read -r f; do
      [ -n "$f" ] || continue
      if is_product_go "$f"; then gochanged=true
      elif ! is_reader_facing "$f"; then fail "(b) prose commit $short touches $f, which is neither Go comments nor reader-facing prose"; fi
    done <<<"$files"
    if [ "$gochanged" = true ]; then
      export_tree "$c^" "$short-a"; export_tree "$c" "$short-b"
      tool astdiff "/work/t/$short-a" "/work/t/$short-b" || fail "(b) prose commit $short changes Go code, not only comments"
    fi
  else
    while read -r f; do
      [ -n "$f" ] || continue
      if is_prose_only "$f"; then fail "(c) commit $short touches $f without the prose trailer"; fi
      if is_help "$f" && [ "$seen_prose" = true ]; then fail "(c) commit $short touches $f after the prose stage without the prose trailer"; fi
      if is_product_go "$f"; then gochanged=true; fi
    done <<<"$files"
    if [ "$gochanged" = true ] && [ "$seen_prose" = true ]; then
      export_tree "$c^" "$short-a"; export_tree "$c" "$short-b"
      tool nocomments -root "/work/t/$short-b" -baseline "/work/t/$short-a" || fail "(c) commit $short adds comments to Go without the prose trailer"
    fi
  fi
done
[ $rc = 0 ] && echo "prose-check: clean over $(wc -w <<<"$commits") commits ($base..$head)"
exit $rc
