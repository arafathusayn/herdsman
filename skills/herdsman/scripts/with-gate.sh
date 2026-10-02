#!/bin/bash
# Run one heavy command at a time on this machine: a full test suite, a type check, a build.
# Every agent (implementers, the integrator, reviewers, and other routes on the same machine) shares one lock,
# so heavy gates queue instead of running together and starving the machine. The command runs at a lower
# CPU priority. Targeted tests of a few files do not need the gate. bash 3 compatible.
# Usage: /bin/bash <skill>/scripts/with-gate.sh <command> [args...]
#        a command line with && or a pipe goes in one argument: with-gate.sh sh -c 'bun test && tsc'
# HERDSMAN_GATE       the lock folder (default /tmp/herdsman-gate: a sandboxed agent can write /tmp)
# HERDSMAN_GATE_WAIT  seconds to wait for the lock before running anyway, so a stuck holder never stops a route (default 1800)
# HERDSMAN_GATE_POLL  seconds between lock checks (default 10)
# HERDSMAN_NICE       niceness of the command and its children (default 10)
# Exit status: the command's, or 2 on a usage error.
[ $# -gt 0 ] || { echo 'usage: with-gate.sh <command> [args...]' >&2; exit 2; }
GATE=${HERDSMAN_GATE:-/tmp/herdsman-gate}
WAIT=${HERDSMAN_GATE_WAIT:-1800}
POLL=${HERDSMAN_GATE_POLL:-10}
NICE=${HERDSMAN_NICE:-10}

# A gated command that runs this script again (a suite script that starts the type check) already holds the lock.
[ -n "${HERDSMAN_GATE_HELD:-}" ] && exec "$@"

# The holder is alive unless its process is gone. A sandbox can refuse the signal check ("not permitted"):
# that holder is alive. A lock without a pid file is stale after a minute (its taker died before writing it).
holder_alive() {
  pid=$(cat "$GATE/pid" 2>/dev/null)
  if [ -z "$pid" ]; then
    [ -z "$(find "$GATE" -maxdepth 0 -mmin +1 2>/dev/null)" ]
    return
  fi
  kill -0 "$pid" 2>/dev/null && return 0
  kill -0 "$pid" 2>&1 | grep -q 'not permitted'
}

start=$(date +%s)
said=""
until mkdir "$GATE" 2>/dev/null; do
  if ! holder_alive; then
    # Move the stale lock aside first: a rename is atomic, so two waiters cannot both clear it.
    mv "$GATE" "$GATE.stale.$$" 2>/dev/null && rm -f "$GATE.stale.$$/pid" "$GATE.stale.$$/cmd" && rmdir "$GATE.stale.$$"
    continue
  fi
  if [ $(( $(date +%s) - start )) -ge "$WAIT" ]; then
    echo "with-gate: waited ${WAIT}s for pid $(cat "$GATE/pid" 2>/dev/null) ($(cat "$GATE/cmd" 2>/dev/null)); running without the gate" >&2
    HERDSMAN_GATE_HELD=1 exec nice -n "$NICE" "$@"
  fi
  if [ -z "$said" ]; then
    echo "with-gate: waiting for pid $(cat "$GATE/pid" 2>/dev/null) ($(cat "$GATE/cmd" 2>/dev/null))" >&2
    said=1
  fi
  sleep "$POLL"
done
echo $$ > "$GATE/pid"
printf '%s\n' "$*" > "$GATE/cmd"
trap 'rm -f "$GATE/pid" "$GATE/cmd"; rmdir "$GATE" 2>/dev/null' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
HERDSMAN_GATE_HELD=1 nice -n "$NICE" "$@"
