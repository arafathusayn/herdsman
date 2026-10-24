#!/bin/bash
# Sourced hint transport. Queue scans, never event delivery, decide correctness.
# A timeout may discard a partial Bash read. Reconnect and rescan on all timeouts
# or malformed frames, so a fragment is never joined to the next JSON object.
events_close() {
  if [ -n "${EV_PID:-}" ]; then kill "$EV_PID" 2>/dev/null || :; wait "$EV_PID" 2>/dev/null || :; fi
  EV_PID=
  exec 7>&- 8>&-
  if [ -n "${EV_DIR:-}" ]; then rm -f "$EV_DIR/in" "$EV_DIR/out" "$EV_DIR/owner.json" "$EV_DIR/child.pid" "$EV_DIR/child.identity" "$EV_DIR/scanned"; rmdir "$EV_DIR"; fi
  EV_DIR=
}
events_open() {
  events_close
  # Check in parent before starting nc. A missing/invalid socket cannot block
  # FIFO opens: the parent opens both ends read/write before spawning its child.
  [ -n "${HERDR_SOCKET_PATH:-}" ] && [ -e "$HERDR_SOCKET_PATH" ] || return 1
  EV_DIR=$(mktemp -d "$ROOT/state/.events.XXXXXX") || return 1
  atomic "$EV_DIR/owner.json" jq -n --argjson owner "$$" --arg dir "$EV_DIR" --arg socket "$HERDR_SOCKET_PATH" '{owner_pid:$owner,directory:$dir,child:"child.pid",identity:"child.identity",expected_command:"nc -U",socket:$socket,state:"preparing"}' || return 1
  mkfifo "$EV_DIR/in" "$EV_DIR/out" || { events_close; return 1; }
  exec 7<>"$EV_DIR/in" 8<>"$EV_DIR/out"
  /bin/bash -c 'printf "%s\n" "$$" > "$1/child.pid"; ps -p "$$" -o lstart= -o command= > "$1/child.identity"; exec nc -U "$2"' transport "$EV_DIR" "$HERDR_SOCKET_PATH" <"$EV_DIR/in" >"$EV_DIR/out" 7>&- 8>&- &
  EV_PID=$!
  printf '{"id":"message-hints","method":"events.subscribe","params":{"subscriptions":%s}}\n' "$1" >&7
  local first
  if ! IFS= read -r -t 1 -u 8 first ||
     ! printf '%s' "$first" | jq -e '.result.type=="subscription_started"' >/dev/null 2>&1; then
    events_close; return 1
  fi
}
events_next() {
  EV_LINE=
  local status=0 limit=$1
  if [ -n "${EVENT_READ_MAX:-}" ]; then
    uint EVENT_READ_MAX "$EVENT_READ_MAX" 1 3600
    [ "$limit" -le "$EVENT_READ_MAX" ] || limit=$EVENT_READ_MAX
  fi
  IFS= read -r -t "$limit" -u 8 EV_LINE || status=$?
  if [ "$status" != 0 ]; then
    # Bash may erase a partial buffer on timeout, so empty cannot prove quiet.
    events_close
    return 2
  fi
  case "$EV_LINE" in
    *message_out*|*messages*|*pane_closed*|*pane_exited*) ;;
    *'"event"'*) return 3 ;;
    *) events_close; return 2 ;;
  esac
  if printf '%s' "$EV_LINE" | jq -e 'type=="object" and (.event|type=="string")' >/dev/null 2>&1; then return 0; fi
  events_close; return 2
}
