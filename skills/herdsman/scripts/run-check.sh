#!/bin/bash
# run-check.sh <command> [args]: one heavy check (type check, lint, tests, formatter, build) at a time on this
# machine, behind a load gate, at a lower CPU priority. bash 3 compatible; macOS (lockf) and Linux (flock).
# Every agent of every route on the machine shares one lock. Inside the lock the check waits while the 1-minute
# load is at or over the limit or free memory is low: a reading taken before the lock is stale by the time the
# lock is free. A check that does not run says so on a line that starts with "run-check: NOT RUN".
# The route folder holds a small wrapper that sets the route's limits and runs this script:
#   export HERDSMAN_LOAD_LIMIT=<idle load plus a margin> HERDSMAN_MIN_FREE=<free memory percent>
#   exec /bin/bash <skill>/scripts/run-check.sh "$@"
# Publisher (outside any sandbox, in a background shell of the orchestrator): run-check.sh --publish <hours>
#   writes "<1-minute load> <free memory %>" to HERDSMAN_READING every 15 s, then stops. Sandboxes that deny
#   the probes read that file; with no reading at all a check runs under the lock only and says so.
# Settings: HERDSMAN_LOCK (default /tmp/herdsman-checks.lock), HERDSMAN_READING (default /tmp/herdsman-load),
# HERDSMAN_LOAD_LIMIT (default: the core count), HERDSMAN_MIN_FREE (default 10), HERDSMAN_LOCK_WAIT (seconds,
# default 5400), HERDSMAN_GATE_WAIT (seconds on the load gate, default 600), HERDSMAN_NICE (default 10).
# Exit status: the command's; 75 when the load gate gave up or no lock came within HERDSMAN_LOCK_WAIT;
# 64 on a usage error; 69 without lockf or flock; 70 when the start marker cannot be written.
LOCK=${HERDSMAN_LOCK:-/tmp/herdsman-checks.lock}
READING=${HERDSMAN_READING:-/tmp/herdsman-load}
LOCK_WAIT=${HERDSMAN_LOCK_WAIT:-5400}
GATE_WAIT=${HERDSMAN_GATE_WAIT:-600}
MIN_FREE=${HERDSMAN_MIN_FREE:-10}
NICE=${HERDSMAN_NICE:-10}
POLL=${HERDSMAN_GATE_POLL:-15}

# GNU and BSD (macOS) stat differ.
if stat -c %Y / >/dev/null 2>&1; then file_mtime() { stat -c %Y "$1" 2>/dev/null; }
else file_mtime() { stat -f %m "$1" 2>/dev/null; }; fi

# "<1-minute load> <free memory %>" from the machine, or nothing. HERDSMAN_PROBES=off skips the probes
# (tests, and sandboxes where a probe hangs instead of failing).
probe() {
  [ "${HERDSMAN_PROBES:-on}" = off ] && return
  if [ -r /proc/loadavg ]; then
    load=$(cut -d' ' -f1 /proc/loadavg)
    free=$(awk '/^MemTotal/{t=$2} /^MemAvailable/{a=$2} END{if (t) print int(a*100/t)}' /proc/meminfo 2>/dev/null)
  else
    load=$(LC_ALL=C sysctl -n vm.loadavg 2>/dev/null | tr -d '{}' | awk '{print $1}')
    free=$(sysctl -n kern.memorystatus_level 2>/dev/null)
    [ -n "$free" ] || free=$(memory_pressure 2>/dev/null | awk -F': ' '/free percentage/ {print $2+0}')
  fi
  [ -n "$load" ] && [ -n "$free" ] && echo "$load $free"
}

# The publisher's reading while it is fresh (under a minute old), else the probes.
reading() {
  m=$(file_mtime "$READING")
  if [ -n "$m" ] && [ $(( $(date +%s) - m )) -lt 60 ]; then cat "$READING"; return; fi
  probe
}

if [ "${1:-}" = --publish ]; then
  [ -n "${2:-}" ] || { echo "usage: run-check.sh --publish <hours>" >&2; exit 64; }
  end=$(( $(date +%s) + ${HERDSMAN_PUBLISH_SECONDS:-$(( $2 * 3600 ))} ))
  while [ "$(date +%s)" -lt "$end" ]; do
    r=$(probe)
    [ -n "$r" ] && printf '%s\n' "$r" > "$READING.$$" && mv "$READING.$$" "$READING"
    sleep "${HERDSMAN_PUBLISH_EVERY:-15}"
  done
  echo "run-check: publisher stopped at $(date '+%H:%M'); sandboxed checks now run without a load gate" >&2
  exit 0
fi

case "${HERDSMAN_CHECK:-}" in
  gated) exec "$@" ;;   # a check started by a check: it already holds the lock and passed the gate
  locked) ;;            # started again by lockf or flock below: gate, then run
  *)
    [ $# -gt 0 ] || { echo "usage: run-check.sh <command> [args]" >&2; exit 64; }
    base=$(mktemp -u "$(dirname "$LOCK")/herdsman-check.XXXXXX")
    [ -n "$base" ] || { echo "run-check: NOT RUN (no name for the start marker)" >&2; exit 70; }
    export HERDSMAN_CHECK=locked HERDSMAN_CHECK_IN="$base.in" HERDSMAN_CHECK_RAN="$base.ran"
    # The lock belongs to the file descriptor, which the check and its children inherit: the kernel frees it
    # when the last of them exits, also after a kill -9. A process a check leaves running keeps the lock.
    attempt() {
      w=$1; shift
      if command -v lockf >/dev/null 2>&1; then lockf -k -s -t "$w" "$LOCK" /bin/bash "$0" "$@"
      elif [ "$w" = 0 ]; then flock -n -E 75 "$LOCK" /bin/bash "$0" "$@"
      else flock -w "$w" -E 75 "$LOCK" /bin/bash "$0" "$@"; fi
    }
    command -v lockf >/dev/null 2>&1 || command -v flock >/dev/null 2>&1 \
      || { echo "run-check: NOT RUN (no lockf or flock on this machine)" >&2; exit 69; }
    attempt 0 "$@"; s=$?
    if [ ! -e "$HERDSMAN_CHECK_IN" ]; then
      holders=$(lsof -t "$LOCK" 2>/dev/null | tr '\n' ' ')
      echo "run-check: waiting for the machine lock, held by pid ${holders:-?}" >&2
      attempt "$LOCK_WAIT" "$@"; s=$?
    fi
    if [ -e "$HERDSMAN_CHECK_RAN" ]; then rm -f "$HERDSMAN_CHECK_IN" "$HERDSMAN_CHECK_RAN"; exit "$s"; fi
    if [ -e "$HERDSMAN_CHECK_IN" ]; then echo "run-check: NOT RUN (exit $s; the line above says why)" >&2
    else echo "run-check: NOT RUN (no lock within ${LOCK_WAIT}s)" >&2; fi
    rm -f "$HERDSMAN_CHECK_IN"
    exit "$s" ;;
esac

touch "$HERDSMAN_CHECK_IN"
limit=${HERDSMAN_LOAD_LIMIT:-$(sysctl -n hw.ncpu 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null)}
start=$(date +%s)
while :; do
  r=$(reading)
  if [ -z "$r" ]; then echo "run-check: no load reading, running under the lock only" >&2; break; fi
  load=${r%% *}; free=${r##* }
  under=1
  [ -n "$limit" ] && ! awk -v l="$load" -v m="$limit" 'BEGIN { exit !(l < m) }' && under=0
  [ "$under" = 1 ] && [ "${free%.*}" -ge "$MIN_FREE" ] && break
  if [ $(( $(date +%s) - start )) -ge "$GATE_WAIT" ]; then
    echo "run-check: NOT RUN (load gate gave up after ${GATE_WAIT}s: load $load, limit $limit, free $free%)" >&2
    exit 75
  fi
  sleep "$POLL"
done
export HERDSMAN_CHECK=gated
# Mark the start before the check runs; without a marker, stop rather than run a check that would read as NOT RUN.
touch "$HERDSMAN_CHECK_RAN" && [ -e "$HERDSMAN_CHECK_RAN" ] || { echo "run-check: NOT RUN (cannot write the start marker)" >&2; exit 70; }
exec nice -n "$NICE" "$@"
