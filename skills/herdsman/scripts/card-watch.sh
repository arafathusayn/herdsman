#!/bin/bash
# Capture and relay side-agent data; never type into any pane.
. "$(dirname "$0")/message-lib.sh"
INTERVAL=${INTERVAL:-5}; RUN_FOR=${RUN_FOR:-0}
uint INTERVAL "$INTERVAL" 1 60; uint RUN_FOR "$RUN_FOR" 0 86400
root_init
strict_json "$ROOT/watch-targets.json" || die 'strict approved watch targets required'
jq -e 'type=="array" and length>0 and all(.[]; keys==["pane","session"] and (.pane|type=="string") and (.session|keys==["agent","kind","source","value"]))' "$ROOT/watch-targets.json" >/dev/null || die 'invalid watch targets'
mkdir "$ROOT/watch.lock" 2>/dev/null || die 'watcher already owned; inspect lock'
export WATCH_OWNED=1; printf '%s\n' "$$" > "$ROOT/watch.lock/pid"
[ ! -L "$ROOT/card-events" ] || die 'card journal symlink'
mkdir -p "$ROOT/card-events"
registry || die 'unapproved pair'
if [ -e "$ROOT/resume-targets.json" ]; then
  strict_json "$ROOT/resume-targets.json" && jq -e 'type=="object" and all(to_entries[]; (.key|test("^[a-z][a-z0-9_-]{0,31}$")) and .value.role=="task-agent" and (.value.identity|keys==["pane","session"]))' "$ROOT/resume-targets.json" >/dev/null || die 'invalid resume targets'
fi
sender=$(jq -er 'to_entries[]|select(.value.role=="orchestrator")|.key' "$ROOT/peers.approved")
receiver=$(jq -er 'to_entries[]|select(.value.role=="adjutant")|.key' "$ROOT/peers.approved")
sender_pane=$(peer "$sender" | jq -r .identity.pane)
started=$(now)
while :; do
  safe_touch "$ROOT/state/watch-heartbeat"
  retry_alerts
  health || :
  while IFS= read -r target <&6; do
    pane=$(printf '%s' "$target" | jq -r .pane)
    key=$(printf '%s' "$target" | jq -Sc . | cksum | awk '{print $1 "-" $2}')
    if ! identity=$(live_identity "$pane") || ! same_identity "$identity" "$target"; then
      notify "watch-drift-$key" 'Notice monitor identity changed' 'Inspect configured route panes and rebuild approved targets.'; continue
    fi
    resolve_alert "watch-drift-$key"
    if ! screen=$(herdr pane read "$pane" --source recent-unwrapped --lines 60 </dev/null); then
      notify "watch-read-$key" 'Notice monitor read failed' 'Inspect configured pane manually.'; continue
    fi
    resolve_alert "watch-read-$key"
    # Layout rows, not arbitrary quoted words. A journal adapter is required to
    # retain cards shorter than a polling interval and cards beyond scrollback.
    cards=$(printf '%s\n' "$screen" | awk '/^[[:space:]]*✦ Heads up/{print}')
    while IFS= read -r card <&5; do
      [ -n "$card" ] || continue
      card_key=$(printf '%s\n%s' "$key" "$card" | cksum | awk '{print $1 "-" $2}')
      if [ ! -f "$ROOT/card-events/$card_key.json" ]; then
        atomic "$ROOT/card-events/$card_key.json" jq -n --arg id "$card_key" --argjson source "$target" --arg text "$card" '{version:1,type:"side_agent_data",id:$id,source:$source,text:$text}'
      fi
    done 5< <(printf '%s\n' "$cards")
    phrase=$(printf '%s\n' "$screen" | grep -iE '^[[:space:]]*(Do you want to proceed|Allow reads outside|Password for|Permission required|Yes, proceed|escalated execution requires|Queued follow-up inputs|Type your answer|Usage limit reached|Selected model is at capacity|This content can.t be shown)' | sed -n '1p') || phrase=
    if [ -n "$phrase" ]; then
      phrase_key=$(printf '%s:%s' "$key" "$phrase" | cksum | awk '{print $1 "-" $2}')
      marker="$ROOT/state/blocked-$key"
      previous=; regular "$marker" && previous=$(cat "$marker")
      if [ "$previous" = "$phrase_key" ]; then
        notify "blocked-$phrase_key" 'Agent notice or blocked state' "Inspect pane $pane. No prompt was answered; a timeout remains a refusal."
      else
        [ -z "$previous" ] || resolve_alert "blocked-$previous"
        write_text "$marker" "$phrase_key"
      fi
    else
      marker="$ROOT/state/blocked-$key"
      if regular "$marker"; then previous=$(cat "$marker"); resolve_alert "blocked-$previous"; rm "$marker"; fi
    fi
  done 6< <(jq -c '.[]' "$ROOT/watch-targets.json")
  for event in "$ROOT/card-events/"*.json; do
    [ -e "$event" ] || continue
    strict_json "$event" && jq -e '.version==1 and .type=="side_agent_data" and (.id|type=="string") and (.source|type=="object") and (.text|type=="string")' "$event" >/dev/null || { notify card-invalid 'Invalid side-agent record' 'Retain and inspect journal record.'; continue; }
    event_key=$(basename "$event" .json)
    [ ! -f "$ROOT/state/card-$event_key.queued" ] || continue
    # Service observation uses the orchestrator transport identity, never user_answer.
    if receipt=$(HERDR_PANE_ID="$sender_pane" /bin/bash "$(dirname "$0")/message-send.sh" "$sender" "$receiver" "$event" observation); then
      write_text "$ROOT/state/card-$event_key.queued" "$receipt"
    else notify "card-pending-$event_key" 'Side-agent relay pending' 'Journal retained; retry without typing into the orchestrator.'; fi
  done
  # The approved watcher owns automatic resume checks for all registered Codex
  # targets. The helper refuses the adjutant, busy turns, freezes and drift.
  while IFS= read -r resume_name <&5; do
    result=0
    /bin/bash "$(dirname "$0")/resume-goal-when-idle.sh" "$resume_name" >/dev/null || result=$?
    if [ "$result" = 2 ]; then notify "resume-$resume_name" 'Goal resume needs inspection' 'Inspect registered identity and goal state; no blind retry.'; fi
  done 5< <(jq -r 'to_entries[]|select(.value.role!="adjutant" and .value.identity.session.agent=="codex")|.key' "$ROOT/peers.approved"; if regular "$ROOT/resume-targets.json"; then jq -r 'to_entries[]|select(.value.role=="task-agent" and .value.identity.session.agent=="codex")|.key' "$ROOT/resume-targets.json"; fi)
  [ "$RUN_FOR" = 0 ] || [ $(( $(now)-started )) -lt "$RUN_FOR" ] || exit 0
  sleep "$INTERVAL"
done
