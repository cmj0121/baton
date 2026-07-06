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
export HOME="$(mktemp -d "${TMPDIR:-/tmp}/baton-demo.XXXXXX")" # fresh state → empty dashboard every take
export BATON_SOCK="/tmp/baton-demo.sock"                       # keep it short: unix socket paths cap at ~104 chars
export PS1='\[\e[38;5;39m\]baton\[\e[0m\]:\[\e[38;5;245m\]demo\[\e[0m\]$ '
rm -f "$BATON_SOCK" "${BATON_SOCK%.sock}.state.json"

exec "$bin" -f
