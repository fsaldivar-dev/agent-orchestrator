#!/bin/zsh
set -eu
fork_root="${0:A:h:h:h}"
fork_binary="$fork_root/frontend/daemon/ao"
fork_desktop="$HOME/Applications/Orchestrator Fork.app/Contents/MacOS/agent-orchestrator"
if [[ ! -x "$fork_binary" || ! -x "$fork_desktop" ]]; then
  print -u2 'Build the fork daemon and install the official desktop first. See FORK.md.'
  exit 1
fi
# The fork app contains its own tested daemon; the official app stays intact.
unset AO_DAEMON_COMMAND
export AO_TELEMETRY_EVENTS=off
exec "$fork_desktop"
