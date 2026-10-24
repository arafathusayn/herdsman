#!/bin/bash
# <me>. Persist change fingerprints across foreground turns.
. "$(dirname "$0")/message-lib.sh"
. "$(dirname "$0")/herdr-events.sh"
[ $# = 1 ] && name_ok "$1" || die 'wait <me>'
ME=$1; RECHECK=${RECHECK:-60}; DEADLINE=${DEADLINE:-1500}
uint RECHECK "$RECHECK" 1 3600; uint DEADLINE "$DEADLINE" 1 1500
root_init
if regular "$ROOT/state/timing.json"; then
  sweep=$(jq -er .sweep "$ROOT/state/timing.json")
  [ "$HEALTH_AFTER" -gt $((sweep+10)) ] || die 'health threshold conflicts with courier timing'
fi
end=$(( $(now)+DEADLINE )); next=0
while :; do
  t=$(now)
  if [ "$t" -ge "$next" ]; then
    # File-only health is observable before any potentially hanging CLI or lock.
    flags=$(health_snapshot) || :
    fault=
    if [ -z "$flags" ]; then
      if ! (self_peer "$ME"); then fault=identity-unavailable; fi
    fi
    found=; notices=; pending=0
    if [ -d "$ROOT/$ME/new" ]; then
      found=$(unacked "$ROOT/$ME" | sed 's|.*/||' | sort)
      for notice in "$ROOT/$ME/notices/"*.json; do
        regular "$notice" || continue
        notices="$notices $(basename "$notice"):$(digest "$notice")"
      done
      pending=$(count_state "$ROOT/$ME/new")
    fi
    fingerprint=$(printf '%s\n' "$found" "$notices" "$pending" "$flags" "$fault" | cksum | awk '{print $1 "-" $2}')
    previous=; state="$ROOT/state/wait-$ME"
    regular "$state" && previous=$(cat "$state")
    if [ "$previous" != "$fingerprint" ]; then
      write_text "$state" "$fingerprint"
      if [ -n "$flags$fault" ]; then printf 'HEALTH %s %s\n' "$flags" "$fault"; exit 1; fi
      if [ -n "$found" ]; then printf 'MESSAGE %s: run message-read.sh\n' "$ME"; exit 0; fi
      if [ -n "$notices" ]; then printf 'HEALTH sender notice; inspect message-status.sh\n'; exit 1; fi
      if [ "$pending" -gt 0 ]; then printf 'HEALTH pending delivery; inspect message-status.sh\n'; exit 1; fi
    fi
    next=$((t+RECHECK))
  fi
  [ "$t" -lt "$end" ] || { printf 'TICK message-wait deadline\n'; exit 0; }
  if [ -z "${EV_PID:-}" ] && ! events_open '[{"type":"pane.updated"}]'; then sleep 1; continue; fi
  t=$(now); wait_for=$((next-t)); [ "$wait_for" -gt 0 ] || continue
  [ "$wait_for" -le $((end-t)) ] || wait_for=$((end-t))
  [ "$wait_for" -gt 0 ] || continue
  if events_next "$wait_for"; then
    pane=$(printf '%s' "$EV_LINE" | jq -r '.data.pane.pane_id // empty')
    [ "$pane" != "${HERDR_PANE_ID:-}" ] || next=0
  fi
done
