#!/bin/bash
# Tests for the herdsman message channel under macOS /bin/bash 3.2 with BSD tools, and under GNU tools.
# Run from bash or zsh: zsh <skill>/scripts/test/message-tests.sh [work dir]
# Every script runs under `env -i` with the fake herdr and nc of test/fakebin first on PATH and fake HERDR_*
# values, so no inherited HERDR_* variable reaches a live Herdr session. The fake herdr serves agent records
# from <case>/agents.json and logs every call to <case>/calls.log. HERDR_SOCKET_PATH is empty, so the courier
# and waiter use their bounded queue scans; the event stream itself is not exercised here.
# Started from zsh or sh, re-run under /bin/bash (the line is valid in all three).
[ -n "${BASH_VERSION:-}" ] || exec /bin/bash "$0" "$@"
set -u
SK=$(cd "$(dirname "$0")/.." && pwd)
T=${1:-${TMPDIR:-/tmp}/herdsman-msg-test-$$}
mkdir -p "$T"
T=$(cd "$T" && pwd -P)
chmod +x "$SK/test/fakebin/herdr" "$SK/test/fakebin/nc"
CPATH="$SK/test/fakebin:/usr/bin:/bin:/usr/sbin:/sbin:$(dirname "$(command -v jq)")"
A=route-adjutant; O=route-orchestrator; ID=m000000000001
ENVX=
pass=0; fail=0
check() { # name, expected-regex, actual
  if printf '%s' "$3" | grep -q -E "$2"; then pass=$((pass+1)); echo "PASS $1"; else fail=$((fail+1)); echo "FAIL $1: expected /$2/ got: $3"; fi
}
nocheck() { # name, forbidden-regex, actual
  if printf '%s' "$3" | grep -q -E "$2"; then fail=$((fail+1)); echo "FAIL $1: unexpected /$2/ in: $3"; else pass=$((pass+1)); echo "PASS $1"; fi
}
yn() { if [ -e "$1" ]; then echo yes; else echo no; fi; }
digest() { cksum < "$1" | awk '{print $1 "-" $2}'; }
# msg <pane> <script> [args]: run one channel script as the given pane, with $ENVX as extra assignments.
msg() {
  local pane=$1 script=$2; shift 2
  # shellcheck disable=SC2086
  env -i PATH="$CPATH" HOME="$HOME" HERDSMAN_INBOX="$C/inbox" FIXTURE="$C" HERDR_PANE_ID="$pane" HERDR_SOCKET_PATH= \
    HERDSMAN_PREFLIGHT="$C/preflight.json" WATCH_REQUIRED=0 $ENVX /bin/bash "$SK/$script" "$@" 2>&1
}
courier() { local ENVX="RUN_FOR=1 $*"; msg courier-pane message-courier.sh; }
send() { msg sender-pane message-send.sh "$A" "$O" "$C/body.txt" brief; }
agent() { # pane, session value
  jq -n --arg p "$1" --arg s "$2" '{pane_id:$p,terminal_id:("term-"+$p),workspace_id:"fixture-workspace",tab_id:"fixture-tab",
    focused:false,revision:1,agent:"claude",interactive_ready:true,launch_pending:false,agent_status:"idle",
    agent_session:{source:"hook",agent:"claude",kind:"id",value:$s}}'
}
set_agents() { # pane session ... (pairs)
  local out='{}'
  while [ $# -ge 2 ]; do out=$(printf '%s' "$out" | jq --arg p "$1" --argjson a "$(agent "$1" "$2")" '.[$p]=$a'); shift 2; done
  printf '%s\n' "$out" > "$C/agents.json"
}
jq_edit() { jq -c "$1" "$2" > "$C/edit.tmp" && mv "$C/edit.tmp" "$2"; }
verified_log() { cat "$C/inbox/state/"diagnostic-*.log 2>/dev/null | grep VERIFIED; }
# new_case <name> [noenroll]: a fresh private inbox with a preflight proof and two agents, enrolled and approved.
new_case() {
  C="$T/$1"; mkdir -m 700 "$C" "$C/inbox"; : > "$C/calls.log"
  printf '.herdsman/\n' > "$C/ignore-rule"
  jq -n --arg inbox "$C/inbox" --arg rule "$C/ignore-rule" --arg sum "$(digest "$C/ignore-rule")" \
    '{inbox:$inbox,excluded:true,tracked_files:0,rules:[{path:$rule,checksum:$sum}]}' > "$C/preflight.json"
  set_agents sender-pane sender-session receiver-pane receiver-session
  printf 'full brief\n' > "$C/body.txt"
  [ "${2:-}" = noenroll ] && return 0
  msg sender-pane message-register.sh propose "$A" adjutant >/dev/null
  msg receiver-pane message-register.sh propose "$O" orchestrator >/dev/null
  msg sender-pane message-register.sh approve "$(digest "$C/inbox/peers")" >/dev/null
  touch "$C/inbox/state/heartbeat"
}
OB() { printf '%s' "$C/inbox/$O"; }

echo "bash: $(/bin/bash --version | head -1)"
for s in message-lib message-register message-send herdr-events message-courier message-wait message-read message-ack \
  message-status message-authority message card-watch resume-goal-when-idle publish-leak-gate; do
  if /bin/bash -n "$SK/$s.sh" 2>/dev/null; then pass=$((pass+1)); echo "PASS syntax $s"; else fail=$((fail+1)); echo "FAIL syntax $s"; fi
done

# R: enrollment stores both full identities and holds them for the human's approval.
new_case register
check "R1 both full identities approved" '^sender-session receiver-session$' \
  "$(jq -r '[.["route-adjutant"].identity.session.value,.["route-orchestrator"].identity.session.value]|join(" ")' "$C/inbox/peers.approved")"
before=$(digest "$C/inbox/peers")
msg sender-pane message-register.sh propose "$A" adjutant >/dev/null
check "R1b repeating the same enrollment leaves the registry unchanged" "^$before\$" "$(digest "$C/inbox/peers")"
out=$(msg sender-pane message-register.sh propose '../escape' adjutant); check "R2 a traversal name is refused" 'propose <name>' "$out"
check "R2b and no mailbox escapes the inbox" '^no$' "$(yn "$C/escape")"
out=$(msg sender-pane message-register.sh approve 1-2); check "R3 approve needs the inspected checksum" 'inspected pending checksum' "$out"
set_agents sender-pane sender-session receiver-pane receiver-session other-pane other-session
out=$(msg other-pane message-register.sh propose route-adjutant-2 adjutant)
check "R4 a second adjutant cannot join a live pair" 'role/session already enrolled' "$out"

# D: a valid message reaches ready, and the recipient's count is reported as metadata.
new_case deliver
out=$(send); check "D1 send queues and says delivery is not confirmed" "^$ID queued" "$out"
check "D1b the envelope waits in new" '^yes$' "$(yn "$(OB)/new/$ID.md")"
courier >/dev/null
check "D2 the courier verifies and moves it to ready" '^yes no$' "$(yn "$(OB)/ready/$ID.md") $(yn "$(OB)/new/$ID.md")"
check "D2b the recipient's count is one" "report-metadata receiver-pane --source herdsman-message-count --token messages=1" "$(cat "$C/calls.log")"
check "D2c and the delivery is logged as verified (the M cases assert its absence)" "VERIFIED $ID" "$(verified_log)"
out=$(msg sender-pane message-send.sh "$A" route-nobody "$C/body.txt" brief)
check "D3 an unknown recipient is refused before queueing" 'unknown peer' "$out"
check "D3b and gets no mailbox" '^no$' "$(yn "$C/inbox/route-nobody")"

# K: read claims and prints manifests, never bodies; ack records intake; release needs every message acked.
out=$(msg receiver-pane message-read.sh "$O")
check "K1 read prints the claim, the manifest and the summary" '"type":"claim".*"type":"manifest".*"id":"m000000000001".*"type":"summary"' "$(printf '%s' "$out" | tr '\n' ' ')"
nocheck "K1b read never prints the body" 'full brief' "$out"
token=$(printf '%s\n' "$out" | jq -r 'select(.type=="claim")|.token')
out=$(msg receiver-pane message-read.sh "$O"); check "K2 a second reader is refused" 'reader claim held' "$out"
out=$(msg receiver-pane message-read.sh "$O" "$token" --release); check "K3 release with an unacked message is refused" 'acknowledge all delivered work' "$out"
printf 'intake recorded\n' > "$C/record.txt"
out=$(msg receiver-pane message-ack.sh "$O" wrong-token "$ID" "$C/record.txt"); check "K4 a wrong claim token cannot ack" 'wrong active reader claim' "$out"
out=$(msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt"); check "K5 ack with the claim token records intake" "acknowledged $ID" "$out"
out=$(msg receiver-pane message-read.sh "$O" "$token" --release); check "K6 release after every ack" '"type":"released"' "$out"
courier >/dev/null
check "K7 an acked message clears the count" "report-metadata receiver-pane --source herdsman-message-count --clear-token messages" "$(cat "$C/calls.log")"

# M1: a forged from name never reaches ready.
new_case forged-from
send >/dev/null
jq_edit '.from="route-orchestrator"' "$(OB)/new/$ID.md"
courier >/dev/null
check "M1 forged from name: quarantined" '^yes no$' "$(yn "$(OB)/quarantine/$ID.md") $(yn "$(OB)/ready/$ID.md")"
check "M1b with a permanent quarantined disposition" '^quarantined$' "$(jq -r .state "$C/inbox/ids/$ID/terminal.json")"
nocheck "M1c and never logged as verified" VERIFIED "$(verified_log)"
new_case forged-unknown
send >/dev/null
jq_edit '.from="route-intruder"' "$(OB)/new/$ID.md"
courier >/dev/null
check "M1d forged unenrolled from name: quarantined" '^yes no$' "$(yn "$(OB)/quarantine/$ID.md") $(yn "$(OB)/ready/$ID.md")"
out=$(msg receiver-pane message-status.sh "$O")
check "M1e status counts the quarantine" 'quarantine' "$out"
nocheck "M1f status never prints the rejected body" 'full brief' "$out"

# M2: the right name with a wrong session never reaches ready, edited or live.
new_case wrong-session
send >/dev/null
jq_edit '.from_identity.session.value="other-session"' "$(OB)/new/$ID.md"
courier >/dev/null
check "M2 right name, edited wrong session: quarantined" '^yes no$' "$(yn "$(OB)/quarantine/$ID.md") $(yn "$(OB)/ready/$ID.md")"
new_case live-session
send >/dev/null
set_agents sender-pane replaced-session receiver-pane receiver-session
courier >/dev/null
check "M2b right name, live session changed after send: quarantined" '^yes no$' "$(yn "$(OB)/quarantine/$ID.md") $(yn "$(OB)/ready/$ID.md")"
nocheck "M2c and never logged as verified" VERIFIED "$(verified_log)"

# M3: the sender pane closed before verification.
new_case pane-closed
send >/dev/null
set_agents receiver-pane receiver-session
courier >/dev/null
check "M3 sender pane closed before verification: quarantined" '^yes no$' "$(yn "$(OB)/quarantine/$ID.md") $(yn "$(OB)/ready/$ID.md")"
check "M3b the sender's mailbox gets a notice" '^yes$' "$(yn "$C/inbox/$A/notices/$ID.md.json")"

# M4: a changed peers file holds delivery across courier restarts until the registry matches again.
new_case peers-changed
send >/dev/null
jq_edit '.["route-adjutant"].identity.session.value="new-session"' "$C/inbox/peers"
courier >/dev/null
check "M4 peers changed: delivery held in new" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
check "M4b the user is told" 'notification show Message peers changed' "$(cat "$C/calls.log")"
courier >/dev/null
check "M4c still held after a courier restart" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
out=$(msg sender-pane message-send.sh "$A" "$O" "$C/body.txt" brief); check "M4d send refuses while the change awaits approval" 'awaiting human approval' "$out"
cp "$C/inbox/peers.approved" "$C/inbox/peers"
courier >/dev/null
check "M4e the restored registry delivers the held message" '^no yes$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"

# M5: a message older than ALERT_AFTER raises an unread alert; a fresh one does not.
new_case old-message
send >/dev/null
courier >/dev/null
nocheck "M5 a fresh message raises no unread alert" 'notification show Unread messages' "$(cat "$C/calls.log")"
touch -t 202001010000 "$(OB)/ready/$ID.md"
courier ALERT_AFTER=60 >/dev/null
check "M5b a message older than ALERT_AFTER raises the unread alert" 'notification show Unread messages' "$(cat "$C/calls.log")"
n=$(grep -c 'notification show Unread messages' "$C/calls.log")
courier ALERT_AFTER=60 >/dev/null
check "M5c the same backlog is not alerted twice" "^$n\$" "$(grep -c 'notification show Unread messages' "$C/calls.log")"

# M6: the courier restarted with messages already in new/ delivers them.
new_case restart
send >/dev/null
touch "$C/get-failure"
courier >/dev/null
check "M6 an unavailable sender check keeps the message in new" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
check "M6b and never rejects it" '^no$' "$(yn "$(OB)/quarantine/$ID.md")"
rm "$C/get-failure"
courier >/dev/null
check "M6c the restarted courier delivers the message left in new" '^no yes$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"

# W: the waiter wakes once on a delivery, then stays quiet on the same unread state.
out=$(ENVX="DEADLINE=2 RECHECK=1" msg receiver-pane message-wait.sh "$O")
check "W1 the waiter wakes on a ready message" "MESSAGE $O" "$out"
out=$(ENVX="DEADLINE=2 RECHECK=1" msg receiver-pane message-wait.sh "$O")
check "W2 the same unread state does not wake it again" 'TICK' "$out"
rm "$C/inbox/state/heartbeat"
out=$(ENVX="DEADLINE=2 RECHECK=1" msg receiver-pane message-wait.sh "$O")
check "W3 a missing courier heartbeat wakes HEALTH" 'HEALTH courier-down' "$out"

# P: the public grammar and the boundary decision.
out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh bogus); check "P1 an unknown message command fails with the grammar" 'status\|wait\|read' "$out"
out=$(msg receiver-pane message-authority.sh); st=$?
check "P2 no boundary proof: relayed gos authorize nothing outward" '^HUMAN' "$out"; check "P2b exit 3" '^3$' "$st"
jq_edit '.boundary={version:1,private_home:true,inventory_current:true,lower_trust:[{name:"im-a",harness:"codex",launch_hash:"h",sandbox:true,inbox_denied:true,socket_denied:true}]}' "$C/preflight.json"
check "P3 a complete fixture boundary permits RELAY" '^RELAY' "$(msg receiver-pane message-authority.sh)"
jq_edit '.boundary.lower_trust[0].sandbox=false' "$C/preflight.json"
check "P3b one unsandboxed agent forces HUMAN" '^HUMAN' "$(msg receiver-pane message-authority.sh)"
new_case unproven noenroll
jq_edit '.excluded=false' "$C/preflight.json"
out=$(msg sender-pane message-register.sh propose "$A" adjutant)
check "P4 an inbox without a verified exclusion is refused" 'inbox exclusion not verified' "$out"
check "P4b before any state is created" '^no$' "$(yn "$C/inbox/state")"

# L: the publish leak gate.
printf 'See /Users/someone/project for details.\n' > "$T/leak.md"
printf 'All checks passed on the current head.\n' > "$T/clean.md"
out=$(/bin/bash "$SK/publish-leak-gate.sh" "$T/leak.md" 2>&1); st=$?
check "L1 a private path fails the gate" 'LEAK' "$out"; check "L1b exit 1" '^1$' "$st"
/bin/bash "$SK/publish-leak-gate.sh" "$T/clean.md" >/dev/null 2>&1; check "L2 clean prose passes" '^0$' "$?"
/bin/bash "$SK/publish-leak-gate.sh" "$T/missing.md" >/dev/null 2>&1; check "L3 a missing file is an error, not clean" '^2$' "$?"

# N: no channel script typed into any pane.
nocheck "N1 no input call reached the fake herdr" 'agent prompt|send-keys|send-text|pane run' "$(cat "$T"/*/calls.log)"

echo "RESULT pass=$pass fail=$fail (work dir $T)"
[ "$fail" -eq 0 ]
