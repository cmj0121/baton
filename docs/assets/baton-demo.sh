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
rm -rf "$HOME"                           # fresh state → empty dashboard every take
export PS1='\[\e[38;5;39m\]baton\[\e[0m\]:\[\e[38;5;245m\]demo\[\e[0m\]$ '
export BATON_BIN="$bin" # demo-agent.sh dials the socket back with the same binary
rm -f "$BATON_SOCK" "${BATON_SOCK%.sock}.state.json"

# The fresh HOME has no agent CLI set up, so the conductor key (C) would land in
# the real agent's first-run wizard. Point the default profile at the stand-in
# instead — see demo-agent.sh for what it does and does not fake.
#
# Pin the language too. With nothing set, the cockpit follows the recorder's
# locale, so a machine with LANG=zh_TW would record the English README's hero in
# Chinese. The clip has to look the same wherever it is recorded, so state it.
mkdir -p "$HOME/.baton"
cat >"$HOME/.baton/config" <<YAML
settings:
  language: en
panel:
  agents:
    claude:
      command: bash
      args: ["$here/demo-agent.sh"]
YAML

exec "$bin" -f
