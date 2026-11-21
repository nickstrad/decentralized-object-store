#!/bin/bash
# Launch one headless agent stage, detached, in a worktree.
# Usage: launch.sh <run-name> <opus|sonnet|neon|neon-or|astra> <workdir> <brief-file>
# Writes $RUNS/objstore-p2-<run-name>.{log,pid,exit}; the exit file appears when the agent stops.
# Verifies 15 s in that the process is alive and its cwd is <workdir>; a stray run is killed.
set -u
RUNS=${RUNS:-/root/runs}
name=$1; seat=$2; wd=$(cd "$3" && pwd -P); brief=$(readlink -f "$4")
base="$RUNS/objstore-p2-$name"
[ -f "$brief" ] || { echo "no brief: $brief" >&2; exit 2; }
rm -f "$base.exit"
export PATH="$HOME/.nvm/versions/node/v22.23.2/bin:$HOME/.deno/bin:$HOME/.opencode/bin:/usr/local/go/bin:$PATH"
ask="Read the brief file $brief and carry it out completely, working only in the current directory ($wd). The brief is your whole task."
case "$seat" in
  opus)   inner="IS_SANDBOX=1 claude -p --model claude-opus-5-5 --dangerously-skip-permissions < '$brief'" ;;
  sonnet) inner="IS_SANDBOX=1 claude -p --model claude-sonnet-5-5 --dangerously-skip-permissions < '$brief'" ;;
  neon)   inner="set -a; . /root/Software/.secrets/yt-transcribe.env; . /root/Software/.secrets/gemini-neon.env; set +a; opencode run -m google/gemini-4-neon \"$ask\" </dev/null" ;;
  neon-or) inner="set -a; . /root/Software/.secrets/yt-transcribe.env; . /root/Software/.secrets/gemini-neon.env; set +a; opencode run -m openrouter/google/gemini-4-neon \"$ask\" </dev/null" ;;
  astra)  inner="codex exec -m gpt-6-astra --dangerously-bypass-approvals-and-sandbox \"$ask\" </dev/null" ;;
  *) echo "unknown seat $seat" >&2; exit 2 ;;
esac
cd "$wd" || exit 2
setsid nohup bash -c "$inner; echo \$? > '$base.exit'" > "$base.log" 2>&1 < /dev/null &
pid=$!
echo "$pid" > "$base.pid"
sleep 15
if [ -f "$base.exit" ]; then echo "LAUNCH $name: already exited $(cat "$base.exit")"; tail -5 "$base.log"; exit 1; fi
cwd=$(readlink -f "/proc/$pid/cwd" 2>/dev/null)
if [ "$cwd" != "$wd" ]; then echo "LAUNCH $name: cwd '$cwd' != '$wd' — killing"; kill -- -"$pid" 2>/dev/null; exit 1; fi
[ -f "$base.log" ] || { echo "LAUNCH $name: no log"; exit 1; }
echo "LAUNCH $name: ok pid=$pid seat=$seat cwd=$cwd"
