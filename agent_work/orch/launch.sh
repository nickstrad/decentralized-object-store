#!/bin/bash
# Launch one headless agent stage, detached, in a worktree.
# Usage: launch.sh <run-name> <opus|sonnet|neon|neon-or|astra> <workdir> <brief-file>
# Writes $RUNS/objstore-p2-<run-name>.{log,pid,exit}; the exit file appears when the agent stops.
# Verifies 15 s in that the process is alive and its cwd is <workdir>; a stray run is killed.
# Neon (opencode) needs --auto or every write and shell call is rejected, and it can stop
# early with exit 0; its prompt asks for a final NEON-DONE line and a run without it exits 70.
# A brief outside <workdir> is fine for every seat once --auto is set. At the default reasoning
# variant Neon spent its whole output budget thinking and returned nothing on a large review
# (2026-11-21); NEON_VARIANT=high is the default here, medium is the proven fallback.
set -u
RUNS=${RUNS:-/root/runs}
[ $# = 4 ] || { echo "usage: launch.sh <run-name> <seat> <workdir> <brief-file>" >&2; exit 2; }
[ -d "$3" ] || { echo "no workdir: $3" >&2; exit 2; }
name=$1; seat=$2; wd=$(cd "$3" && pwd -P); brief=$(readlink -f "$4")
[ -n "$wd" ] && [ "$wd" != / ] || { echo "bad workdir: $3" >&2; exit 2; }
base="$RUNS/objstore-p2-$name"
[ -f "$brief" ] || { echo "no brief: $brief" >&2; exit 2; }
rm -f "$base.exit"
export PATH="$HOME/.nvm/versions/node/v22.23.2/bin:$HOME/.deno/bin:$HOME/.opencode/bin:/usr/local/go/bin:$PATH"
ask="Read the brief file $brief and carry it out completely, working only in the current directory ($wd). The brief is your whole task."
done_ask="When everything the brief asks for is finished and written to disk, print a final line containing exactly NEON-DONE."
case "$seat" in
  opus)   inner="IS_SANDBOX=1 claude -p --model claude-opus-5-5 --dangerously-skip-permissions < '$brief'" ;;
  sonnet) inner="IS_SANDBOX=1 claude -p --model claude-sonnet-5-5 --dangerously-skip-permissions < '$brief'" ;;
  neon)   inner="set -a; . /root/Software/.secrets/yt-transcribe.env; . /root/Software/.secrets/gemini-neon.env; set +a; opencode run --auto --variant ${NEON_VARIANT:-high} -m google/gemini-4-neon \"$ask $done_ask\" </dev/null 2>&1 | tee '$base.out'; rc=\${PIPESTATUS[0]}; tail -n 5 '$base.out' | sed 's/\x1b\[[0-9;]*m//g' | grep -qxE '[[:space:]]*NEON-DONE[[:space:]]*' || { [ \$rc -ne 0 ] || rc=70; }; rm -f '$base.out'; (exit \$rc)" ;;
  neon-or) inner="set -a; . /root/Software/.secrets/yt-transcribe.env; . /root/Software/.secrets/gemini-neon.env; set +a; opencode run --auto --variant ${NEON_VARIANT:-high} -m openrouter/google/gemini-4-neon \"$ask $done_ask\" </dev/null 2>&1 | tee '$base.out'; rc=\${PIPESTATUS[0]}; tail -n 5 '$base.out' | sed 's/\x1b\[[0-9;]*m//g' | grep -qxE '[[:space:]]*NEON-DONE[[:space:]]*' || { [ \$rc -ne 0 ] || rc=70; }; rm -f '$base.out'; (exit \$rc)" ;;
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
