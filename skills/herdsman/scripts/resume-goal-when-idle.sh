#!/bin/bash
# <registered-name>. Approved automatic Codex pause recovery, never the adjutant.
. "$(dirname "$0")/message-lib.sh"
[ $# = 1 ] && name_ok "$1" || die 'resume <registered-name>'
root_init
registry || die 'pending registry'
target=$(peer "$1") || {
  strict_json "$ROOT/resume-targets.json" || die 'no approved resume targets'
  target=$(jq -ce --arg n "$1" '.[$n] // empty' "$ROOT/resume-targets.json") || die 'unknown target'
}
role=$(printf '%s' "$target" | jq -r .role)
case "$role" in orchestrator|task-agent) ;; *) die 'target must be orchestrator or task-agent, never adjutant' ;; esac
expected=$(printf '%s' "$target" | jq -c .identity)
pane=$(printf '%s' "$expected" | jq -r .pane)
adjutant_pane=$(jq -r '.[]|select(.role=="adjutant")|.identity.pane' "$ROOT/peers.approved")
[ "$pane" != "$adjutant_pane" ] || die 'never target the adjutant pane'
[ "$(printf '%s' "$expected" | jq -r .session.agent)" = codex ] || die 'target is not Codex'
if [ -e "$ROOT/state/route-paused" ] || [ -e "$ROOT/state/route-frozen" ]; then printf 'HELD user pause or freeze\n'; exit 3; fi
identity=$(live_identity "$pane") || die 'identity unavailable'
same_identity "$identity" "$expected" || die 'identity changed'
raw=$(herdr agent get "$pane" </dev/null) || die 'status unavailable'
[ "$(printf '%s' "$raw" | jq -r .result.agent.agent)" = codex ] || die 'target is not Codex'
if [ "$(printf '%s' "$raw" | jq -r .result.agent.agent_status)" != idle ]; then printf 'WAIT turn is not idle\n'; exit 3; fi
screen=$(herdr pane read "$pane" --source recent-unwrapped --lines 30 </dev/null) || die 'pane read failed'
key=$(printf '%s' "$expected" | jq -Sc . | cksum | awk '{print $1 "-" $2}')
if ! printf '%s\n' "$screen" | grep -qE '^[[:space:]]*Goal paused( \(/goal resume\))?[[:space:]]*$'; then
  if printf '%s\n' "$screen" | grep -qE '^[[:space:]]*(Working|Goal achieved)'; then rm -f "$ROOT/state/resume-$key.sent"; fi
  printf 'NOOP no current paused-goal marker\n'; exit 0
fi
if printf '%s\n' "$screen" | grep -qiE '^[[:space:]]*(Working|.*esc to interrupt|Queued follow-up inputs|Type your answer|Usage limit reached|Permission required|Do you want to proceed)'; then printf 'HELD busy or blocked\n'; exit 3; fi
[ ! -f "$ROOT/state/resume-$key.sent" ] || { printf 'HELD reconcile the previous resume before retry\n'; exit 3; }
mkdir "$ROOT/herdr-mutation.lock" 2>/dev/null || { printf 'HELD mutation lock busy\n'; exit 3; }
export MUTATION_OWNED=1
printf '%s\n' "$$" > "$ROOT/herdr-mutation.lock/pid"
raw=$(herdr agent get "$pane" </dev/null) || die 'identity unavailable before input'
identity=$(printf '%s' "$raw" | jq -ce '.result.agent|select(.agent=="codex" and .agent_status=="idle" and .interactive_ready==true and .launch_pending==false)|{pane:.pane_id,session:.agent_session}') || { printf 'HELD turn changed before input\n'; exit 3; }
same_identity "$identity" "$expected" || die 'identity changed before input'
# This is the sole automated input exception. Persist intent before submission;
# after a crash or uncertain result inspect the actual goal, never resend blindly.
write_text "$ROOT/state/resume-$key.sent" "$(now) intent /goal resume"
herdr agent prompt "$pane" '/goal resume' </dev/null || die 'resume result uncertain; inspect before retry'
herdr pane read "$pane" --source recent-unwrapped --lines 30 </dev/null >/dev/null || die 'resume sent; read-back failed'
rm "$ROOT/herdr-mutation.lock/pid"; rmdir "$ROOT/herdr-mutation.lock"; MUTATION_OWNED=
printf 'SENT /goal resume; reconcile read-back before rearming\n'
