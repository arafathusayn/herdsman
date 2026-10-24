#!/bin/bash
# Supervised card/blocked monitor. A14 holds card relay and goal input together.
. "$(dirname "$0")/message-lib.sh"
INTERVAL=${INTERVAL:-5}; RUN_FOR=${RUN_FOR:-0}
uint INTERVAL "$INTERVAL" 1 60; uint RUN_FOR "$RUN_FOR" 0 86400
root_init
registry || die 'unapproved pair'
strict_json "$ROOT/watch-targets.json" && jq -e 'type=="array" and length>0 and all(.[]; keys==["pane","session"] and (.pane|type=="string") and (.session|keys==["agent","kind","source","value"]))' "$ROOT/watch-targets.json" >/dev/null || die 'invalid watch targets'
# Immutable validated snapshots. A changed source holds coverage until restart.
targets=$(jq -c . "$ROOT/watch-targets.json"); watch_sum=$(digest "$ROOT/watch-targets.json")
resume_sum=absent; resumes='{}'
if [ -e "$ROOT/resume-targets.json" ]; then
  strict_json "$ROOT/resume-targets.json" && jq -e 'type=="object" and all(to_entries[]; (.key|test("^[a-z][a-z0-9_-]{0,31}$")) and .value.role=="task-agent" and (.value.identity|keys==["pane","session"]))' "$ROOT/resume-targets.json" >/dev/null || die 'invalid resume targets'
  resumes=$(jq -c . "$ROOT/resume-targets.json"); resume_sum=$(digest "$ROOT/resume-targets.json")
fi
mkdir "$ROOT/watch.lock" 2>/dev/null || die 'watcher already owned; inspect lock'
export WATCH_OWNED=1; printf '%s\n' "$$" > "$ROOT/watch.lock/pid"
[ ! -L "$ROOT/card-events" ] || die 'card journal symlink'
mkdir -p "$ROOT/card-events"
sender=$(jq -er 'to_entries[]|select(.value.role=="orchestrator")|.key' "$ROOT/peers.approved")
receiver=$(jq -er 'to_entries[]|select(.value.role=="adjutant")|.key' "$ROOT/peers.approved")
sender_pane=$(peer "$sender" | jq -r .identity.pane)
# Count the source's journal records whose relay the adjutant has not acknowledged, and resolve its journal alert
# when the count is under the cap. Reads $pane and $key; sets $active.
count_journal() {
  local event queued relayed
  active=0
  for event in "$ROOT/card-events/"*.json; do
    regular "$event" || continue
    jq -e --arg pane "$pane" '.source.pane==$pane' "$event" >/dev/null || continue
    queued="$ROOT/state/card-$(basename "$event" .json).queued"
    if regular "$queued"; then
      relayed=$(awk '{print $1; exit}' "$queued")
      if id_ok "$relayed" && regular "$ROOT/ids/$relayed/terminal.json" && jq -e '.state=="handled"' "$ROOT/ids/$relayed/terminal.json" >/dev/null 2>&1; then continue; fi
    fi
    active=$((active+1))
  done
  [ "$active" -ge 8 ] || resolve_alert "card-journal-$key"
}
started=$(now)
while :; do
  safe_touch "$ROOT/state/watch-heartbeat"
  retry_alerts; health || :
  current_resume=absent
  regular "$ROOT/resume-targets.json" && current_resume=$(digest "$ROOT/resume-targets.json")
  coverage=1
  if ! regular "$ROOT/watch-targets.json" || [ "$(digest "$ROOT/watch-targets.json")" != "$watch_sum" ] || [ "$current_resume" != "$resume_sum" ]; then coverage=0; fi
  auto=0
  if [ "$coverage" = 1 ] && boundary_ok; then
    auto=1; rm -f "$ROOT/state/automation-held" "$ROOT/state/agent-list-failures"; resolve_alert automation-held
  else
    # A live agent outside the protected setup is exposure: it stays recorded after that agent exits, until the
    # human repeats setup and removes the record. A failed list is availability, never exposure.
    [ "$coverage" != 1 ] || record_exposure
    failures=0
    if [ "$coverage" = 1 ] && [ "${BOUNDARY_FAULT:-}" = list ]; then
      regular "$ROOT/state/agent-list-failures" && failures=$(cat "$ROOT/state/agent-list-failures")
      uint agent-list-failures "$failures" 0 99999999; failures=$((failures+1))
      write_text "$ROOT/state/agent-list-failures" "$failures"
    else rm -f "$ROOT/state/agent-list-failures"; fi
    # One failed list holds relay and resume quietly, without the waiters' HEALTH record, and clears itself on the
    # next good list. The second failure in a row is recorded and alerts.
    [ "$failures" = 1 ] || write_text "$ROOT/state/automation-held" 'HUMAN: inspect cards and paused goals; no relay or input. Rebuild protected setup after any unsandboxed launch.'
    [ "$failures" = 1 ] || notify automation-held 'Automatic coordination held' 'Human inspection required. No goal resume or card relay; the adjutant must tell the user.'
  fi
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
    cards=$(printf '%s\n' "$screen" | awk '/^[[:space:]]*✦ Heads up/{print}')
    # One bounded capture per source/poll; overflow is an incident, never channel
    # backpressure. Durable harness capture retains full history outside this cap.
    count=$(printf '%s\n' "$cards" | awk 'NF{n++}END{print n+0}')
    if [ "$count" -gt 4 ]; then notify "card-overflow-$key" 'Side-agent cards need inspection' 'Capture cap reached; inspect the source pane and durable harness journal.'
    else resolve_alert "card-overflow-$key"; fi
    # The journal cap counts only records whose relay the adjutant has not acknowledged. It is counted only when a new
    # card needs the cap check or while its journal alert is open, so a drained journal clears the alert.
    active=
    if regular "$ROOT/alerts/card-journal-$key.pending" || regular "$ROOT/alerts/card-journal-$key.sent"; then count_journal; fi
    while IFS= read -r card <&5; do
      [ -n "$card" ] || continue
      card_key=$(printf '%s\n%s' "$key" "$card" | cksum | awk '{print $1 "-" $2}')
      marker="$ROOT/state/card-visible-$card_key"
      if [ ! -f "$marker" ] && [ "$auto" = 1 ]; then
        [ -n "$active" ] || count_journal
        if [ "$active" -ge 8 ]; then
          notify "card-journal-$key" 'Side-agent cards need inspection' 'Source journal cap reached; human disposition required.'
        else
          event=$(mktemp "$ROOT/card-events/$card_key.XXXXXX"); rm "$event"
          atomic "$event.json" jq -n --arg id "$(basename "$event")" --argjson source "$target" --arg text "$card" '{version:1,type:"side_agent_data",id:$id,source:$source,text:$text}'
          active=$((active+1))
        fi
      fi
      [ "$auto" != 1 ] || write_text "$marker" "$card"
    done 5< <(printf '%s\n' "$cards" | sed -n '1,4p')
    # Re-arm identical headlines after one confirmed absent poll.
    for marker in "$ROOT/state/"card-visible-*; do
      regular "$marker" || continue
      old_card=$(cat "$marker")
      old_key=$(printf '%s\n%s' "$key" "$old_card" | cksum | awk '{print $1 "-" $2}')
      [ "$(basename "$marker")" != "card-visible-$old_key" ] || printf '%s\n' "$cards" | grep -Fx "$old_card" >/dev/null || rm "$marker"
    done
    phrase=$(printf '%s\n' "$screen" | sed 's/^[[:space:]]*❯[[:space:]]*/ /; s/^[[:space:]]*■[[:space:]]*/ /' | grep -iE '^[[:space:]]*(([>]?[[:space:]]*[0-9]+[.)]?[[:space:]]*)?)(Do you want to proceed|Do you trust|Allow reads outside|Allow once|Allow always|Password for|Device not configured|Permission required|approval required|Switch to gpt|Yes, proceed|Yes, and don|escalated execution requires|Queued follow-up inputs|Type your answer|Usage limit|Selected model is at capacity|This content can.t be shown)' | sed -n '1p') || phrase=
    marker="$ROOT/state/blocked-$key"
    if [ -n "$phrase" ]; then
      phrase_key=$(printf '%s:%s' "$key" "$phrase" | cksum | awk '{print $1 "-" $2}')
      previous=; regular "$marker" && previous=$(cat "$marker")
      if [ "$previous" = "$phrase_key" ]; then notify "blocked-$phrase_key" 'Agent notice or blocked state' "Inspect pane $pane. No prompt was answered; a timeout remains a refusal."
      else [ -z "$previous" ] || resolve_alert "blocked-$previous"; write_text "$marker" "$phrase_key"; fi
    elif regular "$marker"; then previous=$(cat "$marker"); resolve_alert "blocked-$previous"; rm "$marker"; fi
  done 6< <(printf '%s' "$targets" | jq -c '.[]')
  current_resume=absent
  regular "$ROOT/resume-targets.json" && current_resume=$(digest "$ROOT/resume-targets.json")
  if ! regular "$ROOT/watch-targets.json" || [ "$(digest "$ROOT/watch-targets.json")" != "$watch_sum" ] || [ "$current_resume" != "$resume_sum" ]; then
    auto=0
    write_text "$ROOT/state/automation-held" 'HUMAN changed watch coverage; restart under human setup'
    notify automation-held 'Automatic coordination held' 'Watch coverage changed; no resume or card relay. Tell the user.'
  fi
  if [ "$auto" = 1 ]; then
    invalid=0
    for event in "$ROOT/card-events/"*.json; do
      [ -e "$event" ] || continue
      if ! strict_json "$event" || ! jq -e '.version==1 and .type=="side_agent_data" and (.id|type=="string") and (.source|type=="object") and (.text|type=="string")' "$event" >/dev/null; then invalid=1; continue; fi
      event_key=$(basename "$event" .json)
      [ ! -f "$ROOT/state/card-$event_key.queued" ] || continue
      source_key=$(jq -Sc .source "$event" | cksum | awk '{print $1 "-" $2}')
      # Recheck the live boundary before each relay, including durable records.
      boundary_ok || { record_exposure; break; }
      if receipt=$(HERDR_PANE_ID="$sender_pane" /bin/bash "$(dirname "$0")/message-send.sh" "$sender" "$receiver" "$event" side_agent_data 2>&1); then
        write_text "$ROOT/state/card-$event_key.queued" "$(printf '%s\n' "$receipt" | grep -E '^m[0-9]{12} queued' | sed -n 1p)"
        resolve_alert "relay-overflow-$source_key"; resolve_alert "relay-failed-$source_key"
      else
        case "$receipt" in
          *'side-agent budget full'*) notify "relay-overflow-$source_key" 'Side-agent cards need inspection' 'Source relay budget unavailable; durable journal retained for human inspection.' ;;
          *'HUMAN boundary'*) break ;;
          *) notify "relay-failed-$source_key" 'Side-agent relay failed' 'The send was refused; durable journal retained. Inspect message status.' ;;
        esac
      fi
    done
    if [ "$invalid" = 1 ]; then notify card-invalid 'Invalid side-agent record' 'Retain and inspect journal record.'; else resolve_alert card-invalid; fi
    while IFS= read -r resume_name <&5; do
      result=0
      output=$(RESUME_TARGET_SUM="$resume_sum" /bin/bash "$(dirname "$0")/resume-goal-when-idle.sh" "$resume_name") || result=$?
      if [ "$result" = 0 ]; then resolve_alert "resume-$resume_name"; rm -f "$ROOT/state/resume-held-$resume_name"
      elif [ "$result" = 2 ]; then notify "resume-$resume_name" 'Goal resume needs inspection' 'Inspect identity, focus and retained read-back; no blind retry.'
      else
        case "$output" in
          # A deliberate hold or a busy, blocked or changed turn is already visible in status; stay quiet.
          'HELD mutation lock busy'*) : ;;
          'HELD user pause or freeze'*|'HELD turn is not idle or is blocked'*|'HELD turn changed before input'*)
             write_text "$ROOT/state/resume-held-$resume_name" "$output" ;;
          *) if regular "$ROOT/state/resume-held-$resume_name"; then notify "resume-$resume_name" 'Goal resume held' "$output"
             else write_text "$ROOT/state/resume-held-$resume_name" "$output"; fi ;;
        esac
      fi
    done 5< <(jq -r 'to_entries[]|select(.value.role!="adjutant" and .value.identity.session.agent=="codex")|.key' "$ROOT/peers.approved"; printf '%s' "$resumes" | jq -r 'to_entries[]|select(.value.identity.session.agent=="codex")|.key')
  fi
  [ "$RUN_FOR" = 0 ] || [ $(( $(now)-started )) -lt "$RUN_FOR" ] || exit 0
  sleep "$INTERVAL"
done
