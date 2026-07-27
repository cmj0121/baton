#!/usr/bin/env bash
# Stand-in agent for the README demo recording (see baton-demo.tape).
#
# The clip runs on a throwaway $HOME, so the real agent CLI would open its
# first-run wizard instead of a conductor. baton-demo.sh points the "claude"
# agent profile at this script instead, so `C` lands on something that reads
# like a conductor in a couple of seconds and on any machine.
#
# The control wiring it shows is real, not a mock-up: baton spawned this process
# in a conductor workspace with the conductor identity in its env, so the
# `baton ctl` call below goes over the same socket, under the same fenced
# conductor role, as a real conductor's would. Only the agent's reasoning is
# missing — this is a stand-in for the CLI, not for baton.
set -uo pipefail

bin="${BATON_BIN:-baton}"
dim() { printf '\033[38;5;245m%s\033[0m\n' "$1"; }
cyan() { printf '\033[38;5;39m%s\033[0m\n' "$1"; }

cyan "⏺ conductor · panel ${BATON_PANEL_ID:-?} · role ${BATON_ROLE:-?}"
dim "  briefing: $PWD/BATON.md · tools: baton ctl, baton mcp"
echo

dim "> baton ctl tree"
"$bin" ctl tree 2>&1 || dim "  (control socket unavailable)"
echo

# Idle at a prompt so the panel stays live for the rest of the take — a
# conductor that exits would show up as a dead slot instead of a running mark.
export PS1='\[\e[38;5;39m\]conductor\[\e[0m\]$ '
exec bash --norc -i
