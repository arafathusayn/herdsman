#!/bin/bash
# Event hints first, absolute sweep deadline as safety net.
. "$(dirname "$0")/message-lib.sh"
. "$(dirname "$0")/herdr-events.sh"
SWEEP=${SWEEP:-60}; ALERT_AFTER=${ALERT_AFTER:-600}; RUN_FOR=${RUN_FOR:-0}
uint SWEEP "$SWEEP" 1 3600; uint ALERT_AFTER "$ALERT_AFTER" 1 86400; uint RUN_FOR "$RUN_FOR" 0 86400
root_init
[ "$HEALTH_AFTER" -gt $((SWEEP+10)) ] || die 'HEALTH_AFTER must exceed SWEEP plus scan allowance'
mkdir "$ROOT/courier.lock" 2>/dev/null || die 'courier lock exists; inspect owner and heartbeat'
export COURIER_OWNED=1; printf '%s\n' "$$" > "$ROOT/courier.lock/pid"
atomic "$ROOT/state/timing.json" jq -n --argjson sweep "$SWEEP" --argjson health "$HEALTH_AFTER" '{sweep:$sweep,health:$health}'
SUBS='[{"type":"pane.updated"},{"type":"pane.closed"},{"type":"pane.exited"}]'
process_entry() {
  local f=$1 name=$2 box="$ROOT/$2" from='' expected target observed identity rc=0 id
  export COURIER_OWNED='' WATCH_OWNED='' EV_PID='' EV_DIR='' LOCK_OWNED='' MUTATION_OWNED='' TMP=''
  trap cleanup EXIT
  bounded "$f" || die 'suspicious queue entry held'
  id=$(basename "$f" .md)
  target=$(peer "$name") || die 'orphaned mailbox'
  if ! envelope "$f"; then rc=3
  else
    from=$(jq -r .from "$f")
    if ! expected=$(peer "$from"); then rc=3
    elif ! jq -e --arg to "$name" --argjson from "$expected" --argjson target "$target" '.to==$to and .from_identity==$from.identity and .to_identity==$target.identity' "$f" >/dev/null; then rc=3
    elif [ "$(jq -r .reply_to "$f")" != '' ] && ! regular "$ROOT/ids/$(jq -r .reply_to "$f")/envelope"; then rc=3
    elif ! regular "$ROOT/ids/$id/envelope" || ! cmp -s "$f" "$ROOT/ids/$id/envelope"; then rc=3
    else
      identity=$(printf '%s' "$expected" | jq -c .identity)
      if observed=$(live_identity "$(jq -r .from_identity.pane "$f")"); then
        same_identity "$observed" "$identity" || rc=3
      else rc=$?; fi
    fi
  fi
  try_lock || exit 75
  registry || die 'registry changed during verification'
  [ -e "$f" ] || exit 0
  if [ -e "$ROOT/ids/$id/terminal.json" ] || [ -e "$box/ready/$id.md" ] || [ -e "$box/cur/$id.md" ] || [ -e "$box/quarantine/$id.md" ]; then
    unlock
    notify "duplicate-$name-$id" 'Duplicate message held' 'Preserve both copies; reconcile terminal disposition before archive.'
    exit 0
  fi
  case "$rc" in
    0) move_unique "$f" "$box/ready/$id.md"; log_line "VERIFIED $id for $name" ;;
    3)
      move_unique "$f" "$box/quarantine/$id.md"
      if id_ok "$id" && [ -d "$ROOT/ids/$id" ]; then
        atomic "$ROOT/ids/$id/terminal.json" jq -n --arg id "$id" '{id:$id,state:"quarantined"}' || die 'terminal record failed'
      fi
      log_line "QUARANTINED $id for $name"
      if name_ok "$from" && peer "$from" >/dev/null; then
        box_init "$from"
        atomic "$ROOT/$from/notices/$id.md.json" jq -n --arg id "$id" --arg to "$name" '{id:$id,to:$to,state:"quarantined",authority:"notice-only"}' || die 'sender notice failed'
      fi
      unlock
      notify "quarantine-$name-$id" 'Messages quarantined' 'An envelope failed checks. Show headers only; resend with a fresh ID.'
      exit 0 ;;
    *) unlock; notify "verification-$name-$id" 'Message verification unavailable' 'Retrying queued work; no permanent rejection on transient CLI errors.'; exit 0 ;;
  esac
  unlock
  resolve_alert "verification-$name-$id"
}
sweep() {
  local box name f expected pane observed count state previous stamp t old='' fp='' key summary baseline result id from invalid_box=0
  safe_touch "$ROOT/state/heartbeat"
  retry_alerts
  if ! registry; then
    baseline=unavailable; key=unavailable
    regular "$ROOT/peers.approved" && baseline=$(digest "$ROOT/peers.approved")
    regular "$ROOT/peers" && key=$(digest "$ROOT/peers")
    summary=$(jq -c '[to_entries[]|{name:.key,session:(.value.identity.session.value|.[0:8])}]' "$ROOT/peers.approved" "$ROOT/peers" 2>/dev/null | head -c 300) || summary=unavailable
    notify "peers-$key" 'Message peers changed' "Held: approved $baseline, pending $key. Old then new prefixes: $summary"
    return 0
  fi
  for box in "$ROOT/"*; do
    [ -d "$box/new" ] || continue
    safe_touch "$ROOT/state/heartbeat"
    name=$(basename "$box")
    if [ -L "$box" ] || ! name_ok "$name"; then
      invalid_box=1
      notify invalid-box 'Suspicious queue entry' 'Invalid mailbox held; inspect without opening its body.'; continue
    fi
    if ! peer "$name" >/dev/null; then
      notify "orphan-$name" 'Orphaned message mailbox' 'Reconcile or archive pending work; no automatic handover.'; continue
    fi
    resolve_alert "orphan-$name"
    if ! (box_init "$name"); then notify "box-$name" 'Suspicious queue entry' 'Mailbox state held.'; continue; fi
    resolve_alert "box-$name"
    for f in "$box/quarantine/"*.md; do
      regular "$f" || continue
      (
      export COURIER_OWNED='' WATCH_OWNED='' EV_PID='' EV_DIR='' LOCK_OWNED='' MUTATION_OWNED='' TMP=''
      trap cleanup EXIT
      id=$(basename "$f" .md)
      if id_ok "$id" && [ -d "$ROOT/ids/$id" ] && [ ! -L "$ROOT/ids/$id" ]; then
        if try_lock; then
          if [ -e "$ROOT/ids/$id/terminal.json" ]; then
            if ! strict_json "$ROOT/ids/$id/terminal.json" || ! jq -e --arg id "$id" '.id==$id and .state=="quarantined"' "$ROOT/ids/$id/terminal.json" >/dev/null; then
              unlock; notify "terminal-conflict-$name-$id" 'Quarantine terminal conflict' 'Hold original bytes and inspect the permanent disposition.'; exit 0
            fi
          else atomic "$ROOT/ids/$id/terminal.json" jq -n --arg id "$id" '{id:$id,state:"quarantined"}' || die 'quarantine recovery failed'; fi
          from=$(jq -r '.from // empty' "$f" 2>/dev/null) || from=
          if name_ok "$from" && peer "$from" >/dev/null; then
            atomic "$ROOT/$from/notices/$id.md.json" jq -n --arg id "$id" --arg to "$name" '{id:$id,to:$to,state:"quarantined",authority:"notice-only"}' || die 'notice recovery failed'
          fi
          unlock
          notify "quarantine-$name-$id" 'Messages quarantined' 'An envelope failed checks. Show headers only; resend with a fresh ID.'
        fi
      fi
      ) || notify "quarantine-recovery-$name-$(basename "$f" .md)" 'Quarantine recovery held' 'Inspect this disposition; unrelated work continues.'
    done
    for f in "$box/new/"*.md; do
      [ -e "$f" ] || [ -L "$f" ] || continue
      safe_touch "$ROOT/state/heartbeat"
      key=$(printf '%s' "$f" | cksum | awk '{print $1 "-" $2}')
      result=0; (process_entry "$f" "$name") || result=$?
      case "$result" in
        0) resolve_alert "entry-$key" ;;
        75) log_line "DEFERRED queue lock $name" ;;
        *) notify "entry-$key" 'Suspicious queue entry' 'Entry held after a transition or verification error; unrelated work continues.' || : ;;
      esac
    done
    count=$(unacked "$box" | wc -l | tr -d ' ')
    expected=$(peer "$name" | jq -c .identity); pane=$(printf '%s' "$expected" | jq -r .pane)
    if observed=$(live_identity "$pane") && same_identity "$observed" "$expected"; then
      resolve_alert "identity-$name"
      state="$ROOT/state/count-$name"; previous=; stamp=0
      if regular "$state"; then read -r previous stamp < "$state"; fi
      t=$(now)
      if [ "$previous" != "$count" ] || [ $((t-stamp)) -ge "$SWEEP" ]; then
        if [ "$count" -gt 0 ]; then
          if display_call pane report-metadata "$pane" --source herdsman-message-count --token "messages=$count" --ttl-ms "$((SWEEP*3000))" >/dev/null; then write_text "$state" "$count $t"; fi
        elif display_call pane report-metadata "$pane" --source herdsman-message-count --clear-token messages >/dev/null; then write_text "$state" "$count $t"; fi
      fi
    else notify "identity-$name" 'Message peer identity unavailable' 'Reconcile pane/session; pending work stays bound to its generation.'; fi
    for f in "$box/new/"*.md "$box/ready/"*.md "$box/cur/"*.md; do
      regular "$f" || continue
      ack_valid "$box" "$(basename "$f" .md)" && continue
      stamp=$(mtime "$f" 2>/dev/null) || continue
      if [ $(( $(now)-stamp )) -ge "$ALERT_AFTER" ]; then old=1; fp="$fp $name/$(basename "$f")"; fi
    done
    summary=
    for f in "$box/tmp/"* "$box/tmp/".[!.]*; do
      [ -e "$f" ] || continue
      summary=partial
      notify "partial-$name" 'Interrupted message write' 'Inspect incomplete state before removal.'; break
    done
    [ -n "$summary" ] || resolve_alert "partial-$name"
  done
  [ "$invalid_box" = 1 ] || resolve_alert invalid-box
  if [ -n "$old" ]; then
    key=$(printf '%s' "$fp" | cksum | awk '{print $1 "-" $2}')
    notify "unread-$key" 'Unread messages' 'Messages exceeded the configured age; includes new and unacknowledged cur.'
  fi
  safe_touch "$ROOT/state/heartbeat"
}
log_line 'courier start'
sweep
started=$(now); next=$((started+SWEEP))
while :; do
  t=$(now)
  swept=0
  [ "$RUN_FOR" = 0 ] || [ $((t-started)) -lt "$RUN_FOR" ] || break
  if [ "$t" -ge "$next" ]; then sweep; swept=1; next=$(( $(now)+SWEEP )); fi
  if [ -z "${EV_PID:-}" ]; then
    if events_open "$SUBS"; then
      [ "$swept" = 1 ] || sweep
      write_text "$EV_DIR/scanned" "$(now)"
      next=$(( $(now)+SWEEP ))
    else sleep 1; continue; fi
  fi
  t=$(now); wait_for=$((next-t)); [ "$wait_for" -gt 0 ] || continue
  if [ "$RUN_FOR" != 0 ] && [ "$wait_for" -gt $((RUN_FOR-(t-started))) ]; then wait_for=$((RUN_FOR-(t-started))); fi
  [ "$wait_for" -gt 0 ] || continue
  if events_next "$wait_for"; then
    case "$EV_LINE" in
      *message_out*)
        ring=$(printf '%s' "$EV_LINE" | jq -r '.data.pane.tokens.message_out // empty')
        pane=$(printf '%s' "$EV_LINE" | jq -r '.data.pane.pane_id // empty')
        if id_ok "$ring"; then
          key=$(printf '%s:%s' "$pane" "$ring" | cksum | awk '{print $1 "-" $2}')
          if [ ! -f "$ROOT/state/ring-$key" ]; then sweep; write_text "$ROOT/state/ring-$key" "$ring"; fi
        fi ;;
      *pane_closed*|*pane_exited*) sweep ;;
    esac
  fi
done
log_line 'courier stop'
