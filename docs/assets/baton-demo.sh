#!/usr/bin/env bash
# Isolated baton for the README demo recording (see baton-demo.tape).
#
# Brings baton up on a private, throwaway HOME and socket so the clip never
# touches your real fleet, and clears BATON_DAEMON in case you record from
# inside a baton panel — otherwise the re-exec'd child runs as a headless
# daemon instead of a cockpit and the screen stays blank.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"

# Use a prebuilt binary if present (override with BATON_BIN), else build one.
bin="${BATON_BIN:-$repo/bin/baton}"
if [ ! -x "$bin" ]; then
	echo "baton binary not found — building $bin" >&2
	(cd "$repo" && go build -o bin/baton ./cmd/baton)
fi

unset BATON_DAEMON
export TERM=xterm-256color
export HOME="/tmp/baton-demo-home"       # short and fixed, so the paths on screen stay readable
export BATON_SOCK="/tmp/baton-demo.sock" # keep it short: unix socket paths cap at ~104 chars
# Stop the previous take's daemon BEFORE wiping the state, not after. A cockpit
# exits at the end of a take but its server does not, and `baton -f` finds the
# server it is meant to force-stop through the state file beside the socket — so
# wiping first strands an orphan that still holds the socket, and the take after
# that never comes up at all ("baton server did not come up").
#
# The match is the demo HOME, which is a path no real fleet carries. It must stay
# that specific: a blanket `pkill baton` here would kill the fleet of whoever is
# recording, which is the one machine we know has baton running on it.
pkill -f "$HOME/.baton" 2>/dev/null || true
while pgrep -f "$HOME/.baton" >/dev/null 2>&1; do sleep 0.2; done

rm -rf "$HOME"                           # fresh state → empty dashboard every take
export PS1='\[\e[38;5;39m\]baton\[\e[0m\]:\[\e[38;5;245m\]demo\[\e[0m\]$ '
export BATON_BIN="$bin" # demo-agent.sh dials the socket back with the same binary
rm -f "$BATON_SOCK" "${BATON_SOCK%.sock}.state.json"

# Everything below exists for one reason: the clip has to look the same wherever
# it is recorded. A recording that reads off the recorder's machine is a
# recording that shows a fleet nobody else has.
#
# language  — with nothing set the cockpit follows the recorder's locale, so a
#             machine with LANG=zh_TW would record the English README's hero in
#             Chinese.
# keycast   — on for the same reason the clip exists: without it a viewer sees
#             panels appear and group themselves with no sign of the keys.
# agents    — the fresh HOME has no agent CLI set up, so the conductor would land
#             in the real agent's first-run wizard. The claude profile points at
#             the stand-in instead (see demo-agent.sh for what it does and does
#             not fake). The other presets are pointed at a command that is
#             deliberately absent, which takes them out of detection: A opens the
#             backend picker only when the machine has more than one, and whether
#             the recorder happens to have codex installed must not change what
#             the clip shows.
mkdir -p "$HOME/.baton"
cat >"$HOME/.baton/config" <<YAML
settings:
  language: en
  keycast: true
panel:
  agents:
    claude:
      command: bash
      args: ["$here/demo-agent.sh"]
    codex:
      command: baton-demo-no-such-agent
    gemini:
      command: baton-demo-no-such-agent
    aider:
      command: baton-demo-no-such-agent
    opencode:
      command: baton-demo-no-such-agent
YAML

exec "$bin" -f
