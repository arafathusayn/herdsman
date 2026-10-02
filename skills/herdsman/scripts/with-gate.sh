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
# HERDSMAN_NICE       how much to lower the priority of the command and its children (nice increment, default 10)
# Exit status: the command's, or 2 on a usage error.
[ $# -gt 0 ] || { echo 'usage: with-gate.sh <command> [args...]' >&2; exit 2; }
GATE=${HERDSMAN_GATE:-/tmp/herdsman-gate}
WAIT=${HERDSMAN_GATE_WAIT:-1800}
POLL=${HERDSMAN_GATE_POLL:-10}
NICE=${HERDSMAN_NICE:-10}

# A gated command that runs this script again (a suite script that starts the type check) already holds the lock.
# HERDSMAN_GATE_HELD carries the holder's pid, so a child that outlives the lock does not skip the gate.
[ -n "${HERDSMAN_GATE_HELD:-}" ] && [ "$(cat "$GATE/pid" 2>/dev/null)" = "$HERDSMAN_GATE_HELD" ] && exec "$@"

older_than_a_minute() { [ -n "$(find "$1" -prune -mmin +1 2>/dev/null)" ]; }

# A process is alive unless it is gone. A sandbox can refuse the signal check ("not permitted"): alive.
alive() { kill -0 "$1" 2>/dev/null || kill -0 "$1" 2>&1 | grep -q 'not permitted'; }

# The lock is held while the gate's shell or the command it started is alive: a gate shell killed alone
# leaves its command running. A lock without a pid file is stale after a minute (its taker died before writing it).
holder_alive() {
  pid=$(cat "$GATE/pid" 2>/dev/null)
  if [ -z "$pid" ]; then
    ! older_than_a_minute "$GATE"
    return
  fi
  alive "$pid" && return 0
  child=$(cat "$GATE/child" 2>/dev/null)
  [ -n "$child" ] && alive "$child"
}

# Clear a stale lock under a second lock, and check it again there: a waiter that found the lock stale
# must not remove the new, live lock that another waiter took in the meantime. A takeover lock older than
# a minute belongs to a waiter that died inside these few steps.
take_over() {
  if ! mkdir "$GATE.takeover" 2>/dev/null; then
    older_than_a_minute "$GATE.takeover" && rmdir "$GATE.takeover" 2>/dev/null
    return
  fi
  if [ -d "$GATE" ] && ! holder_alive; then
    rm -f "$GATE/pid" "$GATE/child" "$GATE/cmd"
    rmdir "$GATE" 2>/dev/null
  fi
  rmdir "$GATE.takeover"
}

start=$(date +%s)
said=""
until mkdir "$GATE" 2>/dev/null; do
  # A takeover that cannot clear the lock (another user's folder) falls through to the wait limit and the sleep.
  if ! holder_alive; then
    take_over
    mkdir "$GATE" 2>/dev/null && break
  fi
  if [ $(( $(date +%s) - start )) -ge "$WAIT" ]; then
    echo "with-gate: waited ${WAIT}s for pid $(cat "$GATE/pid" 2>/dev/null) ($(cat "$GATE/cmd" 2>/dev/null)); running without the gate" >&2
    exec nice -n "$NICE" "$@"
  fi
  if [ -z "$said" ]; then
    echo "with-gate: waiting for pid $(cat "$GATE/pid" 2>/dev/null) ($(cat "$GATE/cmd" 2>/dev/null))" >&2
    said=1
  fi
  sleep "$POLL"
done
echo $$ > "$GATE/pid"
printf '%s\n' "$*" > "$GATE/cmd"
trap 'rm -f "$GATE/pid" "$GATE/child" "$GATE/cmd"; rmdir "$GATE" 2>/dev/null' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
# The command records its own pid (sh then becomes the command through exec) and stays in the foreground,
# so an interrupt reaches it.
HERDSMAN_GATE_HELD=$$ HERDSMAN_NICE=$NICE /bin/sh -c 'echo $$ > "$0/child"; exec nice -n "$HERDSMAN_NICE" "$@"' "$GATE" "$@"
