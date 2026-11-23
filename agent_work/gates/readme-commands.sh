#!/bin/bash
# Run every command the README quotes, on a clean clone, the way a new reader would.
#
# Usage: agent_work/gates/readme-commands.sh [--list] [<commit>]      (default commit: HEAD)
#   --list   print the blocks that would run, with their README line numbers, and stop
#
# Clones <commit> into a fresh temporary directory, takes every fenced block of README.md
# whose fence is exactly ```sh, and runs the blocks top to bottom in ONE shell at the
# clone's root, so a later block can rely on an earlier one (build the image, then use it).
# Blocks fenced any other way (```text for long runs) are shown to readers, not executed.
# A README with no ```sh block is a failure: the check would have proved nothing.
# Prints one line per block, `README.md:<line> PASS|FAIL <seconds>s <first command>`, and
# stops at the first failing block (later ones may depend on it). Block output is kept in
# $README_OUT (default: a new temporary directory, printed at the end). Afterwards the
# clone's compose project is taken down and the clone removed, also on failure.
# Needs git, docker and bash on the host. README_TIMEOUT (seconds, default 1800) bounds the
# whole run. Exit: 0 every block passed, 1 a block failed or timed out, 2 usage or setup.
set -u
list=0; [ "${1:-}" = --list ] && { list=1; shift; }
repo=$(cd "$(dirname "$0")/../.." && pwd)
commit=$(git -C "$repo" rev-parse --verify "${1:-HEAD}^{commit}" 2>/dev/null) || { echo "readme-commands: no such commit" >&2; exit 2; }
work=$(mktemp -d /tmp/readme-commands.XXXXXX) || exit 2
clone="$work/decentralized-object-store"
cleanup() {
  if [ -f "$clone/compose.yaml" ]; then (cd "$clone" && docker compose --profile '*' down --remove-orphans --volumes >/dev/null 2>&1); fi
  # Containers run as root and may leave root-owned files in the clone.
  rm -rf "$work" 2>/dev/null || docker run --rm -v "$work":/w busybox sh -c 'rm -rf /w/* /w/.[!.]*' >/dev/null 2>&1
  rm -rf "$work" 2>/dev/null
}
trap cleanup EXIT
{ git clone -q --no-hardlinks "$repo" "$clone" && git -C "$clone" checkout -q --detach "$commit"; } || { echo "readme-commands: clone failed" >&2; exit 2; }
blocks="$work/blocks"; mkdir -p "$blocks"
awk -v dir="$blocks" '
  /^```sh[ \t]*$/ && !inb { inb=1; file=sprintf("%s/%05d.sh", dir, NR); next }
  /^```[ \t]*$/ && inb    { inb=0; close(file); next }
  inb { print > file }
  END { if (inb) exit 3 }' "$clone/README.md" || { echo "readme-commands: README.md has an unclosed sh block" >&2; exit 2; }
n=$(find "$blocks" -name '*.sh' | wc -l)
[ "$n" -gt 0 ] || { echo "readme-commands: README.md quotes no sh block; nothing was checked" >&2; exit 1; }
if [ $list = 1 ]; then
  for f in "$blocks"/*.sh; do echo "README.md:$((10#$(basename "$f" .sh)))"; sed 's/^/    /' "$f"; done
  exit 0
fi
out=${README_OUT:-$(mktemp -d /tmp/readme-commands-out.XXXXXX)}; mkdir -p "$out"
cat > "$work/run.sh" <<'RUN'
blocks=$1; out=$2; cur=
exec 3>&1 4>&2
trap 'rc=$?; if [ -n "$cur" ]; then exec 1>&3 2>&4; echo "$cur FAIL $(( $(date +%s) - t0 ))s rc=$rc $first"; echo "  output: $log"; exit 1; fi' EXIT
for f in "$blocks"/*.sh; do
  line=$((10#$(basename "$f" .sh))); first=$(grep -v '^[[:space:]]*\(#.*\)\?$' "$f" | head -1)
  cur="README.md:$line"; log="$out/README-$line.log"; t0=$(date +%s)
  exec > "$log" 2>&1
  set -e; source "$f"; set +e
  exec 1>&3 2>&4
  echo "$cur PASS $(( $(date +%s) - t0 ))s $first"; cur=
done
RUN
(cd "$clone" && timeout "${README_TIMEOUT:-1800}" bash "$work/run.sh" "$blocks" "$out" < /dev/null); rc=$?
[ $rc = 124 ] && echo "readme-commands: timed out after ${README_TIMEOUT:-1800}s"
echo "readme-commands: $n blocks, commit $commit, output kept in $out"
[ $rc = 0 ] || exit 1
