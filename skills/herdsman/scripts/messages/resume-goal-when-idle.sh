#!/bin/bash
# One approved Codex recovery episode, held whenever protected setup is uncertain.
. "$(dirname "$0")/message-lib.sh"
[ $# = 1 ] && name_ok "$1" || die 'resume <registered-name>'
NAME=$1
root_init
boundary_ok || { printf 'HELD HUMAN boundary; no automatic input\n'; exit 3; }
target_record() {
  registry || return 1
  if [ -n "${RESUME_TARGET_SUM:-}" ]; then
    actual=absent
    regular "$ROOT/resume-targets.json" && actual=$(digest "$ROOT/resume-targets.json")
    [ "$actual" = "$RESUME_TARGET_SUM" ] || return 1
  fi
  peer "$NAME" || {
    strict_json "$ROOT/resume-targets.json" || return 1
    jq -ce --arg n "$NAME" '.[$n]|select(.role=="task-agent")' "$ROOT/resume-targets.json"
  }
}
target=$(target_record) || die 'unknown approved target'
role=$(printf '%s' "$target" | jq -r .role)
case "$role" in orchestrator|task-agent) ;; *) die 'target must be orchestrator or task-agent, never adjutant' ;; esac
expected=$(printf '%s' "$target" | jq -c .identity)
pane=$(printf '%s' "$expected" | jq -r .pane)
adjutant_pane=$(jq -r '.boundary.pair[]|select(.role=="adjutant")|.identity.pane' "$HERDSMAN_PREFLIGHT")
[ "$pane" != "$adjutant_pane" ] || die 'never target the adjutant pane'
[ "$(printf '%s' "$expected" | jq -r .session.agent)" = codex ] || die 'target is not Codex'
key=$(printf '%s' "$expected" | jq -Sc . | cksum | awk '{print $1 "-" $2}')
held() { printf 'HELD %s\n' "$*"; exit 3; }
guards() {
  [ ! -e "$ROOT/state/route-paused" ] && [ ! -e "$ROOT/state/route-frozen" ] &&
    [ ! -e "$ROOT/state/hold-$NAME" ] && [ ! -L "$ROOT/state/hold-$NAME" ]
}
# Any hold record holds, whatever its owners or format; the message names the owners it can read.
pause_held() {
  local owners=unreadable
  if regular "$ROOT/state/hold-$NAME" && strict_json "$ROOT/state/hold-$NAME"; then
    owners=$(jq -r '.holds|keys|join(" ")' "$ROOT/state/hold-$NAME" 2>/dev/null) || owners=unreadable
  elif [ ! -e "$ROOT/state/hold-$NAME" ] && [ ! -L "$ROOT/state/hold-$NAME" ]; then owners=route
  fi
  held "user pause or freeze; holds: $owners"
}
observe() {
  raw=$(herdr agent get "$pane" </dev/null) || die 'identity unavailable'
  identity=$(printf '%s' "$raw" | jq -ce '.result.agent|select(.agent=="codex" and .interactive_ready==true and (.launch_pending // false)==false)|{pane:.pane_id,session:.agent_session}') || die 'target is not Codex or ready'
  same_identity "$identity" "$expected" || die 'identity changed'
  status=$(printf '%s' "$raw" | jq -r .result.agent.agent_status)
  screen=$(herdr pane read "$pane" --source recent-unwrapped --lines 60 </dev/null) || die 'pane read failed'
}
classify() {
  # Active/blocked body evidence wins over stale footers.
  if [ "$status" = working ] || printf '%s\n' "$screen" | grep -qiE '^[[:space:]]*(Working|Pursuing goal|Goal active|Incubating|thinking)|esc to interrupt|… \([0-9]'; then printf working
  elif printf '%s\n' "$screen" | grep -qiE '^[[:space:]■]*(Goal achieved)'; then printf achieved
  elif [ "$status" != idle ] || printf '%s\n' "$screen" | grep -qiE '^[[:space:]■]*(Queued follow-up inputs|Type your answer|Usage limit|Selected model is at capacity|This content can.t be shown|Do you|Allow |Permission|Password|Replace current goal|Resume paused goal)'; then printf blocked
  elif printf '%s\n' "$screen" | grep -qE '^[[:space:]]*Goal paused( \(/goal resume\))?[[:space:]]*$'; then printf paused
  else printf unknown; fi
}
guards || pause_held
observe
state=$(classify)
case "$state" in
  working|achieved) rm -f "$ROOT/state/resume-$key.sent"; printf 'NOOP verified %s\n' "$state"; exit 0 ;;
  blocked) held 'turn is not idle or is blocked' ;;
  unknown) [ ! -f "$ROOT/state/resume-$key.sent" ] || held 'reconcile unknown read-back after previous resume'; printf 'NOOP no current paused-goal marker\n'; exit 0 ;;
esac
[ ! -e "$ROOT/state/resume-$key.sent" ] || held 'reconcile the previous resume'
mkdir "$ROOT/herdr-mutation.lock" 2>/dev/null || held 'mutation lock busy'
export MUTATION_OWNED=1
printf '%s\n' "$$" > "$ROOT/herdr-mutation.lock/pid"
# Recheck the intent under the lock: an overlapping helper may have sent in this episode since the first check.
[ ! -e "$ROOT/state/resume-$key.sent" ] || held 'reconcile the previous resume'
guards || pause_held
boundary_ok || held 'HUMAN boundary changed'
current=$(target_record) || held 'target changed'
[ "$(printf '%s' "$current" | jq -Sc .)" = "$(printf '%s' "$target" | jq -Sc .)" ] || held 'target changed'
observe
[ "$(classify)" = paused ] || held 'turn changed before input'
# Setup writers use this same lock. A final guard also catches an uncooperative
# writer discovered during observation, before the durable intent commits.
guards || pause_held
focus=$(herdr pane list </dev/null | jq -er '[.result.panes[]|select(.focused==true)|.tab_id]|unique|select(length==1)|.[0]') || die 'cannot capture focus'
write_text "$ROOT/state/resume-$key.sent" "$(now) intent /goal resume; restore tab $focus"
result=0
herdr agent prompt "$pane" '/goal resume' </dev/null || result=$?
# Narrow two-mutation transaction: restoration is the next Herdr call even when
# prompt fails. Keep the shared lock through restoration and read-back.
herdr tab focus "$focus" </dev/null >/dev/null || die 'focus restoration uncertain; human inspection required'
screen=$(herdr pane read "$pane" --source recent-unwrapped --lines 60 </dev/null) || die 'resume read-back unavailable'
write_text "$ROOT/state/resume-$key.readback" "$screen"
[ "$result" = 0 ] || held 'resume submission uncertain; inspect read-back'
if printf '%s\n' "$screen" | grep -qiE '^[[:space:]]*(Working|Pursuing goal|Goal active)|esc to interrupt'; then
  printf 'SENT /goal resume; working read-back retained\n'
else held 'resume read-back not working; inspect retained evidence'; fi
