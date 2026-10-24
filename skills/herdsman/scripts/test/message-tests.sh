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
SK=$(cd "$(dirname "$0")/.." && pwd -P)
MS="$SK/messages"
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
nfiles() { # dir, glob: how many entries match
  local f n=0
  # shellcheck disable=SC2086
  for f in "$1"/$2; do [ -e "$f" ] && n=$((n+1)); done
  echo "$n"
}
digest() { cksum < "$1" | awk '{print $1 "-" $2}'; }
# The channel scripts live in scripts/messages; the phase-two adapters and the checkpointer in scripts.
spath() { if [ -e "$MS/$1" ]; then printf '%s' "$MS/$1"; else printf '%s' "$SK/$1"; fi; }
# The protected installation manifest of the setup proof: every channel script, adapter and the checkpointer, each
# with its checksum.
SCRIPTS=$(for f in "$MS"/*.sh "$SK/approval-gate.sh" "$SK/transcript-audit.sh" "$SK/checkpointer-guard.sh" "$SK/checkpointer.sh"; do jq -n --arg p "$f" --arg c "$(digest "$f")" '{checksum:$c,path:$p}'; done | jq -sc .)
# msg <pane> <script> [args]: run one channel script as the given pane, with $ENVX as extra assignments.
msg() {
  local pane=$1 script=$2; shift 2
  # shellcheck disable=SC2086
  env -i PATH="$CPATH" HOME="$HOME" HERDSMAN_INBOX="$C/inbox" FIXTURE="$C" HERDR_PANE_ID="$pane" HERDR_SOCKET_PATH= \
    HERDSMAN_PREFLIGHT="$C/inbox/setup.json" WATCH_REQUIRED=0 $ENVX /bin/bash "$(spath "$script")" "$@" 2>&1
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
  printf 'sandbox policy\n' > "$C/ignore-rule"
  jq -n --arg inbox "$C/inbox" --arg rule "$C/ignore-rule" --arg sum "$(digest "$C/ignore-rule")" --argjson scripts "$SCRIPTS" \
    '{inbox:$inbox,policy_verified:true,rules:[{path:$rule,checksum:$sum}],scripts:$scripts}' > "$C/inbox/setup.json"
  set_agents sender-pane sender-session receiver-pane receiver-session
  printf 'full brief\n' > "$C/body.txt"
  [ "${2:-}" = noenroll ] && return 0
  msg sender-pane message-register.sh propose "$A" adjutant >/dev/null
  msg receiver-pane message-register.sh propose "$O" orchestrator >/dev/null
  msg sender-pane message-register.sh approve "$(digest "$C/inbox/peers")" >/dev/null
  touch "$C/inbox/state/heartbeat"
}
OB() { printf '%s' "$C/inbox/$O"; }
# relay_boundary: record the protected human setup for the current agents: the approved pair pinned, and every
# other live agent as a sandboxed lower-trust launch. message-authority.sh then prints RELAY.
relay_boundary() {
  jq --slurpfile pair "$C/inbox/peers.approved" --slurpfile ag "$C/agents.json" '
    ([$pair[0][].identity]) as $ids |
    .boundary={version:1,inventory_current:true,private_home:true,human_checked:true,protected_domain:true,launch_admission:true,
      pair:$pair[0],courier_panes:[],
      lower_trust:[$ag[0][] | {pane:.pane_id,session:.agent_session} as $i | select(any($ids[]; .==$i)|not) |
        {identity:$i,harness:.agent,launch_hash:"fixture-check",sandbox:true,inbox_denied:true,socket_denied:true,
         code_denied:true,proof_denied:true}]}' "$C/inbox/setup.json" > "$C/edit.tmp" && mv "$C/edit.tmp" "$C/inbox/setup.json"
}

echo "bash: $(/bin/bash --version | head -1)"
for s in message-lib message-register message-send herdr-events message-courier message-wait message-read message-ack \
  message-status message-authority message message-hold card-watch resume-goal-when-idle publish-leak-gate \
  approval-gate transcript-audit checkpointer-guard checkpointer; do
  if /bin/bash -n "$(spath "$s.sh")" 2>/dev/null; then pass=$((pass+1)); echo "PASS syntax $s"; else fail=$((fail+1)); echo "FAIL syntax $s"; fi
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
check "M1e status counts the quarantine" "^$O orphan=[^ ]* new=0 ready=0 cur=0 quarantine=1\$" "$(printf '%s\n' "$out" | grep "^$O orphan=")"
nocheck "M1f status never prints the rejected body" 'full brief' "$out"
new_case status-zero
check "M1g a clean mailbox shows a zero quarantine count" "^$O orphan=[^ ]* .*quarantine=0\$" "$(msg receiver-pane message-status.sh "$O" | grep "^$O orphan=")"

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
for form in 'status extra' 'wait extra' 'read a b' 'release' 'ack a b' 'send a' 'hold a' 'unhold' 'unhold a b'; do
  # shellcheck disable=SC2086
  out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh $form); s=$?
  check "P1b a wrong argument count prints the same grammar: $form" '^2 message: status\|wait\|read' "$s $out"
done
out=$(msg receiver-pane message-authority.sh); st=$?
check "P2 no boundary proof: relayed gos authorize nothing outward" '^HUMAN' "$out"; check "P2b exit 3" '^3$' "$st"
set_agents sender-pane sender-session receiver-pane receiver-session task-pane task-session
relay_boundary
check "P3 a complete protected boundary permits RELAY" '^RELAY' "$(msg receiver-pane message-authority.sh)"
jq_edit '.boundary.lower_trust[0].sandbox=false' "$C/inbox/setup.json"
check "P3b one unsandboxed agent forces HUMAN" '^HUMAN' "$(msg receiver-pane message-authority.sh)"
# Each trigger alone forces HUMAN; the restored boundary is the positive control after each one.
relay_boundary; cp "$C/inbox/setup.json" "$C/setup.base"
for edit in '.boundary.inventory_current=false:stale inventory' '.boundary.private_home=false:a shared home' \
  '.boundary.human_checked=false:no human check' '.boundary.protected_domain=false:no protected domain' \
  '.boundary.lower_trust[0]|=del(.socket_denied):no socket denial recorded' \
  '.boundary.lower_trust[0].code_denied=false:code not denied' '.boundary.lower_trust[0].proof_denied=false:proof not denied' \
  '.boundary.lower_trust=[]:a live agent missing from the inventory' \
  '.boundary.pair={}:a pair that differs from the approved registry'; do
  cp "$C/setup.base" "$C/inbox/setup.json"; jq_edit "${edit%%:*}" "$C/inbox/setup.json"
  check "P3c ${edit#*:} forces HUMAN" '^HUMAN' "$(msg receiver-pane message-authority.sh)"
done
cp "$C/setup.base" "$C/inbox/setup.json"
jq_edit '.["task-pane"].agent_session.value="relaunched"' "$C/agents.json"
check "P3d a relaunched lower-trust session is unknown and forces HUMAN" '^HUMAN' "$(msg receiver-pane message-authority.sh)"
set_agents sender-pane sender-session receiver-pane receiver-session; relay_boundary
check "P3e with only the pair live and an empty lower_trust list, RELAY" '^RELAY' "$(msg receiver-pane message-authority.sh)"
touch "$C/inbox/state/boundary-tainted"
check "P3f a recorded unsandboxed exposure forces HUMAN until human setup" '^HUMAN' "$(msg receiver-pane message-authority.sh)"
new_case unproven noenroll
jq_edit '.policy_verified=false' "$C/inbox/setup.json"
out=$(msg sender-pane message-register.sh propose "$A" adjutant)
check "P4 a setup proof without verified sandbox policies is refused" 'proof stale; repeat human setup' "$out"
check "P4b before any state is created" '^no$' "$(yn "$C/inbox/state")"
jq_edit '.policy_verified=true' "$C/inbox/setup.json"; printf 'changed rule\n' >> "$C/ignore-rule"
out=$(msg sender-pane message-register.sh propose "$A" adjutant)
check "P4c a changed sandbox policy file after the setup is refused" 'proof stale; repeat human setup' "$out"
check "P4d before any state is created" '^no$' "$(yn "$C/inbox/state")"
printf 'sandbox policy\n' > "$C/ignore-rule"; jq_edit '.scripts[0].checksum="1-1"' "$C/inbox/setup.json"
out=$(msg sender-pane message-register.sh propose "$A" adjutant)
check "P4e a changed protected script checksum is refused" 'proof stale; repeat human setup' "$out"
jq --argjson s "$SCRIPTS" '.scripts=$s' "$C/inbox/setup.json" > "$C/edit.tmp" && mv "$C/edit.tmp" "$C/inbox/setup.json"
# V3a: administrative names are reserved, so a peer can never be named like channel state.
for reserved in setup peers state ids alerts; do
  out=$(msg sender-pane message-register.sh propose "$reserved" adjutant)
  check "V3a the reserved name $reserved cannot enroll" 'propose <name> adjutant|orchestrator' "$out"
done
check "V3b and no mailbox or state was created" '^no no$' "$(yn "$C/inbox/state") $(yn "$C/inbox/setup/new")"
# V3c: an escaped key equal to another key is a duplicate.
sed 's/^{/{"policy_\\u0076erified":true,/' "$C/inbox/setup.json" > "$C/edit.tmp" && mv "$C/edit.tmp" "$C/inbox/setup.json"
out=$(msg sender-pane message-register.sh propose "$A" adjutant)
check "V3c an escaped duplicate key in the setup proof is refused" 'proof stale; repeat human setup' "$out"
jq --argjson s "$SCRIPTS" '.scripts=$s[1:]' "$C/inbox/setup.json" > "$C/edit.tmp" && mv "$C/edit.tmp" "$C/inbox/setup.json"
out=$(msg sender-pane message-register.sh propose "$A" adjutant)
check "P4f a manifest that leaves out an installed script is refused" 'proof stale; repeat human setup' "$out"
check "P4g and no state exists" '^no$' "$(yn "$C/inbox/state")"

# L: the publish leak gate.
printf 'See /Users/someone/project for details.\n' > "$T/leak.md"
printf 'All checks passed on the current head.\n' > "$T/clean.md"
# leak <file> [heading ...]: the gate in the same clean environment as the channel scripts.
leak() {
  env -i PATH="$CPATH" HOME="$HOME" LEAK_ALLOW_FILE="${LEAK_ALLOW_FILE:-}" LEAK_PATTERN="${LEAK_PATTERN:-}" \
    /bin/bash "$MS/publish-leak-gate.sh" "$@"
}
out=$(leak "$T/leak.md" 2>&1); st=$?
check "L1 a private path fails the gate" 'LEAK' "$out"; check "L1b exit 1" '^1$' "$st"
leak "$T/clean.md" >/dev/null 2>&1; check "L2 clean prose passes" '^0$' "$?"
leak "$T/missing.md" >/dev/null 2>&1; check "L3 a missing file is an error, not clean" '^2$' "$?"

# V: names, roles and identities that cannot enroll, and permanent retirement.
new_case names
for bad in Upper 'has space' abcdefghijklmnopqrstuvwxyz0123456 '-dash'; do
  out=$(msg sender-pane message-register.sh propose "$bad" adjutant); check "V1 invalid name refused: $bad" 'propose <name>' "$out"
done
out=$(msg sender-pane message-register.sh propose route-x boss); check "V2 an unknown role is refused" 'propose <name>|invalid role' "$out"
msg sender-pane message-register.sh retire "$A" "$(digest "$C/inbox/peers")" >/dev/null
out=$(msg sender-pane message-register.sh propose "$A" adjutant); check "V3 a retired name never reactivates" 'retired name' "$out"
new_case path-session noenroll
set_agents sender-pane 'a/b' receiver-pane receiver-session
out=$(msg sender-pane message-register.sh propose "$A" adjutant); check "V4 a path-bearing session cannot enroll" 'cannot enroll current session' "$out"
new_case same-session noenroll
set_agents sender-pane shared-session receiver-pane shared-session
msg sender-pane message-register.sh propose "$A" adjutant >/dev/null
out=$(msg receiver-pane message-register.sh propose "$O" orchestrator); check "V5 one session in two panes cannot form a pair" 'role/session already enrolled' "$out"

# E: allocation, envelope checks and verification that stays retryable.
new_case envelope
send >/dev/null
check "E1 the queued envelope equals its allocation copy" '^same$' "$(cmp -s "$C/inbox/ids/$ID/envelope" "$(OB)/new/$ID.md" && echo same)"
check "E1b no write is left in tmp" '^x$' "$(ls -A "$(OB)/tmp")x"
check "E1c the next allocation increases" 'm000000000002 queued' "$(send)"
jq_edit '.to="route-adjutant"' "$(OB)/new/m000000000002.md"
jq_edit '.["sender-pane"].agent_status="unknown"' "$C/agents.json"
courier >/dev/null
check "E2 an edited recipient is quarantined" '^yes$' "$(yn "$(OB)/quarantine/m000000000002.md")"
check "E3 an unknown lifecycle keeps the message in new, never rejected" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/quarantine/$ID.md")"
touch "$C/get-invalid"; jq_edit '.["sender-pane"].agent_status="idle"' "$C/agents.json"
courier >/dev/null
check "E4 invalid agent JSON keeps the message in new" '^yes$' "$(yn "$(OB)/new/$ID.md")"
rm "$C/get-invalid"
courier >/dev/null
check "E5 a restored observation delivers it" '^yes$' "$(yn "$(OB)/ready/$ID.md")"
printf 'from: route-adjutant\nto: route-orchestrator\nnot json\n' > "$(OB)/new/m000000000007.md"
jq -c . "$(OB)/ready/$ID.md" | sed 's/^{/{"version":1,/' > "$(OB)/new/m000000000008.md"
send >/dev/null; jq_edit '.reply_to="m000000000099"' "$(OB)/new/m000000000003.md"
send >/dev/null; jq_edit '.to_identity.session.value="old-generation"' "$(OB)/new/m000000000004.md"
courier >/dev/null
for q in 7:'a body with header lines' 8:'a duplicate JSON key' 3:'an unknown reply id' 4:'a wrong recipient generation'; do
  check "E6 quarantined: ${q#*:}" '^yes$' "$(yn "$(OB)/quarantine/m00000000000${q%%:*}.md")"
done
# E6b: a duplicate key alone. Filename, allocation copy and identities stay valid, so only the duplicate check refuses it.
new_case duplicate-only
send >/dev/null
sed 's/^{/{"kind":"brief",/' "$(OB)/new/$ID.md" > "$C/dup.md"; cp "$C/dup.md" "$(OB)/new/$ID.md"; cp "$C/dup.md" "$C/inbox/ids/$ID/envelope"
courier >/dev/null
check "E6b a duplicate key with an equal allocation copy is quarantined" '^yes no$' "$(yn "$(OB)/quarantine/$ID.md") $(yn "$(OB)/ready/$ID.md")"
# E6c: an edit of only the body of a correctly addressed envelope; only the allocation-copy compare can refuse it.
new_case body-edit
send >/dev/null
jq_edit '.body="changed brief\n"' "$(OB)/new/$ID.md"
courier >/dev/null
check "E6c a changed body is quarantined" '^yes no$' "$(yn "$(OB)/quarantine/$ID.md") $(yn "$(OB)/ready/$ID.md")"
C="$T/envelope"
cp "$(OB)/ready/$ID.md" "$C/ready-copy"
cp "$(OB)/ready/$ID.md" "$(OB)/new/$ID.md"
courier >/dev/null
check "E7 a duplicate ID is held, not delivered over ready" 'notification show Duplicate message held' "$(cat "$C/calls.log")"
check "E7b ready keeps its bytes and the copy stays in new" '^same yes$' "$(cmp -s "$C/ready-copy" "$(OB)/ready/$ID.md" && echo same) $(yn "$(OB)/new/$ID.md")"
set_agents sender-pane sender-session receiver-pane replaced-session
out=$(msg receiver-pane message-read.sh "$O"); check "E8 a new recipient session cannot claim old work" 'own pane/session changed' "$out"
check "E8b and the work stays ready" '^yes$' "$(yn "$(OB)/ready/$ID.md")"

# Q: quarantine notices and the correlated resend.
C="$T/wrong-session"
out=$(ENVX="DEADLINE=2 RECHECK=1" msg sender-pane message-wait.sh "$A"); check "Q1 a quarantine notice wakes the sender" 'HEALTH sender notice' "$out"
check "Q2 a fresh correlated resend is queued" 'm000000000002 queued' "$(msg sender-pane message-send.sh "$A" "$O" "$C/body.txt" brief "$ID")"
courier >/dev/null
check "Q2b and delivered while the original stays quarantined" '^yes yes$' "$(yn "$(OB)/ready/m000000000002.md") $(yn "$(OB)/quarantine/$ID.md")"

# H: alerts, rings, held entries, singletons, controls and the read-only status.
new_case alerts
touch "$C/toast-disabled"
send >/dev/null; courier >/dev/null
touch -t 202001010000 "$(OB)/ready/$ID.md"
courier ALERT_AFTER=60 >/dev/null
check "H1 a disabled toast stays pending, not sent" '^1 0$' "$(nfiles "$C/inbox/alerts" 'unread-*.pending') $(nfiles "$C/inbox/alerts" 'unread-*.sent')"
rm "$C/toast-disabled"; touch -t 202001010000 "$C/inbox/alerts/"unread-*.attempt
courier ALERT_AFTER=60 >/dev/null
check "H1b a later retry records the shown toast" '^0 1$' "$(nfiles "$C/inbox/alerts" 'unread-*.pending') $(nfiles "$C/inbox/alerts" 'unread-*.sent')"
touch "$C/metadata-failure"
out=$(send); check "H2 a failed ring warns and the message stays queued" 'WARNING ring failed.*queued' "$(printf '%s' "$out" | tr '\n' ' ')"
rm "$C/metadata-failure"
mkdir -p "$C/inbox/route-ghost/new"; cp "$C/body.txt" "$C/inbox/route-ghost/new/m000000000050.md"
ln -s "$C/body.txt" "$(OB)/new/m000000000051.md"
courier >/dev/null
check "H3 an orphan mailbox alerts and keeps its work" 'notification show Orphaned message mailbox' "$(cat "$C/calls.log")"
check "H3b orphan work intact" '^yes$' "$(yn "$C/inbox/route-ghost/new/m000000000050.md")"
check "H4 a queue symlink is held with a safe alert" 'notification show Suspicious queue entry' "$(cat "$C/calls.log")"
check "H4b the link stays in new and its target is untouched" '^yes full brief$' "$(yn "$(OB)/new/m000000000051.md") $(cat "$C/body.txt")"
out=$(courier SWEEP=abc); check "H5 a malformed control fails before start" 'invalid SWEEP' "$out"
check "H5b without taking the courier lock" '^no$' "$(yn "$C/inbox/courier.lock")"
mkdir "$C/inbox/courier.lock"
out=$(courier); check "H6 a second courier is refused" 'courier lock exists' "$out"
rmdir "$C/inbox/courier.lock"
snap() { find "$C/inbox" -print | sort; find "$C/inbox" -type f -exec cksum {} + | sort; }
before=$(snap); calls=$(wc -l < "$C/calls.log" | tr -d ' ')
out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh)
check "H7 message with no action is status" 'courier pid=' "$out"
check "H7b status changes no inbox byte" '^same$' "$( [ "$before" = "$(snap)" ] && echo same)"
check "H7c and makes no Herdr call" "^$calls\$" "$(wc -l < "$C/calls.log" | tr -d ' ')"
new_case backlog
i=0; while [ "$i" -lt 1000 ]; do : > "$(OB)/ready/x$i.md"; i=$((i+1)); done
out=$(send); check "H8 a full backlog refuses send before allocation" 'pending backlog full' "$out"
check "H8b no ID was allocated" '^no$' "$(yn "$C/inbox/ids/$ID")"

# G: the automatic Codex goal resume sends only the exact input, and only when every guard passes.
new_case resume
jq_edit '.["task-pane"]=(.["sender-pane"]|.pane_id="task-pane"|.agent="codex"|.agent_session.agent="codex"|.agent_session.value="task-session")' "$C/agents.json"
jq -n '{"im-task":{role:"task-agent",identity:{pane:"task-pane",session:{source:"hook",agent:"codex",kind:"id",value:"task-session"}}}}' > "$C/inbox/resume-targets.json"
printf 'Goal paused (/goal resume)\n' > "$C/screen-task-pane"
touch "$C/allow-resume"
prompts() { grep -c '^agent prompt' "$C/calls.log"; }
# A14: without the protected boundary the resume is held, and nothing is typed.
out=$(msg sender-pane resume-goal-when-idle.sh im-task); check "G0 under the HUMAN boundary the resume is held" '^HELD HUMAN boundary' "$out"
check "G0b and no input was sent" '^0$' "$(prompts)"
relay_boundary
out=$(msg sender-pane resume-goal-when-idle.sh "$A"); check "G1 the adjutant is never a resume target" 'never adjutant' "$out"
out=$(msg sender-pane resume-goal-when-idle.sh "$O"); check "G2 a target that is not Codex is refused" 'not Codex' "$out"
touch "$C/inbox/state/route-frozen"
out=$(msg sender-pane resume-goal-when-idle.sh im-task); check "G3 a user freeze holds the resume" 'HELD user pause or freeze' "$out"
rm "$C/inbox/state/route-frozen"
jq_edit '.["task-pane"].agent_status="working"' "$C/agents.json"
out=$(msg sender-pane resume-goal-when-idle.sh im-task); check "G4 a working turn is a no-op that clears any old intent" '^NOOP verified working' "$out"
jq_edit '.["task-pane"].agent_status="idle"' "$C/agents.json"
cp "$C/inbox/resume-targets.json" "$C/resume-targets.base"; jq_edit '.["im-task"].identity.session.value="drifted"' "$C/inbox/resume-targets.json"
out=$(msg sender-pane resume-goal-when-idle.sh im-task); check "G5 a registered identity that differs from the live session is refused" 'identity changed' "$out"
mv "$C/resume-targets.base" "$C/inbox/resume-targets.json"
jq_edit '.["task-pane"].agent_session.value="drifted"' "$C/agents.json"
out=$(msg sender-pane resume-goal-when-idle.sh im-task); check "G5b a relaunched live session is outside the inventory and held" '^HELD HUMAN boundary' "$out"
jq_edit '.["task-pane"].agent_session.value="task-session"' "$C/agents.json"
jq_edit '.["task-pane"].agent_session.value="task-session"' "$C/agents.json"
touch "$C/read-failure"
out=$(msg sender-pane resume-goal-when-idle.sh im-task); check "G6 a failed pane read is refused" 'pane read failed' "$out"
rm "$C/read-failure"
check "G7 no guard sent any input" '^0$' "$(prompts)"
out=$(msg sender-pane resume-goal-when-idle.sh im-task); check "G8 an idle paused Codex goal gets the resume" '^SENT /goal resume' "$out"
check "G8b exactly one input, the exact text, to the task pane" '^agent prompt task-pane /goal resume$' "$(grep '^agent prompt' "$C/calls.log")"
out=$(msg sender-pane resume-goal-when-idle.sh im-task); check "G9 a working turn after the resume is a no-op" '^NOOP' "$out"

# C: side-agent cards relay to the adjutant as data; blocked prompts alert; nothing is typed.
new_case cards
jq -n '[{pane:"receiver-pane",session:{source:"hook",agent:"claude",kind:"id",value:"receiver-session"}}]' > "$C/inbox/watch-targets.json"
printf '%s\n' 'output line' '✦ Heads up · first finding' '✦ Heads up · second finding' 'Permission required' > "$C/screen-receiver-pane"
# A14: without the protected boundary card relay is held and the human is alerted.
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "C0 under the HUMAN boundary no card is relayed" '^0$' "$(nfiles "$C/inbox/$A/new" '*.md')"
check "C0b and the human is alerted" 'notification show' "$(cat "$C/calls.log")"
relay_boundary
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
AB="$C/inbox/$A"
check "C1 every visible card is journaled" '^2$' "$(nfiles "$C/inbox/card-events" '*.json')"
check "C2 and relayed once to the adjutant with the dedicated side-agent kind" '^2 side_agent_data side_agent_data$' \
  "$(nfiles "$AB/new" '*.md') $(cat "$AB/new/"*.md | jq -r .kind | tr '\n' ' ' | sed 's/ $//')"
check "C2b each relay is side-agent data from the orchestrator transport with the exact source" '^2$' \
  "$(cat "$AB/new/"*.md | jq -s --arg o "$O" '[.[] | select(.from==$o and .kind=="side_agent_data" and
    (.body|rtrimstr("\n")|fromjson|.version==1 and .type=="side_agent_data" and (.id|type=="string" and length>0) and
     .source=={pane:"receiver-pane",session:{source:"hook",agent:"claude",kind:"id",value:"receiver-session"}}))] | length')"
check "C2c and keeps each card's text and notice ID" '^first finding second finding 2$' \
  "$(cat "$AB/new/"*.md | jq -rs '[.[]|.body|rtrimstr("\n")|fromjson] | ([.[].text|sub("^.*· ";"")]|sort|join(" ")) + " " + ([.[].id]|unique|length|tostring)')"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "C4 a second watcher run relays nothing again" '^2$' "$(nfiles "$AB/new" '*.md')"
check "C3 a blocked prompt still shown on the next pass alerts" 'notification show Agent notice or blocked state' "$(cat "$C/calls.log")"
set_agents sender-pane sender-session receiver-pane reused-session
reads=$(grep -c '^pane read receiver-pane' "$C/calls.log")
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "C5 a reused pane is not read under the old target" "^$reads\$" "$(grep -c '^pane read receiver-pane' "$C/calls.log")"
check "C5b and the drift alerts" 'notification show Notice monitor identity changed' "$(cat "$C/calls.log")"

# X: strict administrative JSON, shared timing, locks, claim links, concurrency and data-only bodies.
new_case strict
sed 's/^{/{"policy_verified":true,/' "$C/inbox/setup.json" > "$C/edit.tmp" && mv "$C/edit.tmp" "$C/inbox/setup.json"
out=$(send); check "X1 a duplicate setup proof key is refused" 'proof stale; repeat human setup' "$out"
new_case timing
courier >/dev/null
out=$(ENVX="HEALTH_AFTER=99" msg sender-pane message-send.sh "$A" "$O" "$C/body.txt" brief)
check "X2 a health threshold that conflicts with the courier's timing is refused" 'invalid or conflicting shared timing' "$out"
out=$(ENVX="DEADLINE=2 RECHECK=1 WATCH_REQUIRED=1" msg receiver-pane message-wait.sh "$O")
check "X3 a missing watcher heartbeat wakes HEALTH with the courier fresh" 'HEALTH watch-down' "$out"
send >/dev/null
mkdir "$C/inbox/channel.lock"
courier LOCK_WAIT=1 >/dev/null; st=$?
check "X4 a busy queue lock defers the entry without failing the courier" '^yes no 0$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md") $st"
rmdir "$C/inbox/channel.lock"
courier >/dev/null
check "X4b the entry is delivered once the lock is free" '^yes$' "$(yn "$(OB)/ready/$ID.md")"
mkdir "$C/inbox/herdr-mutation.lock"; touch -t 202001010000 "$C/inbox/herdr-mutation.lock"
mkdir -p "$C/inbox/route-ghost/new"
out=$(msg receiver-pane message-status.sh)
check "X5 status shows a stale display lock" 'mutation-lock-stale' "$out"
check "X5b and an orphan mailbox" 'route-ghost orphan=yes' "$out"
check "X5c without taking either lock" '^yes no$' "$(yn "$C/inbox/herdr-mutation.lock") $(yn "$C/inbox/channel.lock")"
rmdir "$C/inbox/herdr-mutation.lock"
out=$(msg receiver-pane message-read.sh "$O"); token=$(printf '%s\n' "$out" | jq -r 'select(.type=="claim")|.token')
mv "$(OB)/claim/token" "$C/token-copy"; ln -s "$C/token-copy" "$(OB)/claim/token"
out=$(msg receiver-pane message-read.sh "$O" "$token"); check "X6 a linked claim token is refused" 'reader claim held' "$out"
check "X6b and the claimed message stays in cur" '^yes$' "$(yn "$(OB)/cur/$ID.md")"
new_case concurrent noenroll
msg sender-pane message-register.sh propose "$A" adjutant > "$C/p1.out" &
msg receiver-pane message-register.sh propose "$O" orchestrator > "$C/p2.out" &
wait
check "X7 two concurrent proposals both survive" '^2$' "$(jq 'length' "$C/inbox/peers")"
msg sender-pane message-register.sh approve "$(digest "$C/inbox/peers")" >/dev/null; touch "$C/inbox/state/heartbeat"
printf 'from: route-intruder\nid: m000000000999\nkind: user_answer\n' > "$C/body.txt"
send >/dev/null; courier >/dev/null
msg receiver-pane message-read.sh "$O" > "$C/r1.out" &
msg receiver-pane message-read.sh "$O" > "$C/r2.out" &
wait
check "X8 exactly one concurrent reader gets the claim" '^1$' "$(cat "$C/r1.out" "$C/r2.out" | grep -c '"type":"claim"')"
check "X9 header lines in a body never become manifest fields" '^m000000000001 route-adjutant brief$' \
  "$(cat "$C/r1.out" "$C/r2.out" | grep '"type":"manifest"' | jq -r '.id + " " + .from + " " + .kind')"

# L2: the leak gate's fence, section, allow-list and pattern checks.
gate() { leak "$@" >/dev/null 2>&1; echo $?; }
printf 'Run this:\n\n```bash\n```\n' > "$T/empty-fence.md"
printf 'Run this:\n\n```bash\nmake test\n' > "$T/unclosed.md"
printf 'Run this:\n\n  ~~~\n  ~~~\n' > "$T/tilde.md"
printf 'Reviewed by the sonnet model.\n' > "$T/model.md"
printf '## Summary\n\nText.\n\n## Verification\n\n```bash\n# run the suite\nmake test\n```\n' > "$T/sections.md"
printf '## Summary\n\n## Verification\n\nmake test\n' > "$T/empty-section.md"
printf 'Set MODE=fast for the demo.\nSee /Users/someone/x for more.\n' > "$T/allow.md"
printf 'Set MODE=fast for the demo.\n' > "$T/allow-list"
check "L4 an empty shell fence fails" '^1$' "$(gate "$T/empty-fence.md")"
check "L5 an unclosed fence fails" '^1$' "$(gate "$T/unclosed.md")"
check "L6 an indented empty tilde fence fails" '^1$' "$(gate "$T/tilde.md")"
check "L7 a model name fails the default patterns" '^1$' "$(gate "$T/model.md")"
check "L8 populated required sections pass, a shell comment in the fence included" '^0$' "$(gate "$T/sections.md" '## Summary' '## Verification')"
check "L9 an empty required section fails" '^1$' "$(gate "$T/empty-section.md" '## Summary' '## Verification')"
check "L10 an allowed exact line passes but the private path next to it still fails" '^1 0$' \
  "$(LEAK_ALLOW_FILE="$T/allow-list" gate "$T/allow.md") $(printf 'Set MODE=fast for the demo.\n' > "$T/allow-only.md"; LEAK_ALLOW_FILE="$T/allow-list" gate "$T/allow-only.md")"
check "L11 an invalid extra pattern is an error" '^2$' "$(LEAK_PATTERN='(' gate "$T/clean.md")"

# F: injected tool failures. test/faultbin comes first on PATH only in these cases; a failed or faked transition
# never loses work, never reports success, and a failed producer never commits state.
FB="$SK/test/faultbin"; chmod +x "$FB"/*
FP="PATH=$FB:$CPATH"
new_case fail-move
send >/dev/null; touch "$C/fail-move"; courier "$FP" >/dev/null
check "F1 a failed delivery move keeps the envelope in new" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
nocheck "F1b and logs no verified delivery" VERIFIED "$(verified_log)"
new_case pretend-move
send >/dev/null; touch "$C/pretend-move"; courier "$FP" >/dev/null
nocheck "F2 a move that claims success without moving reports no count to the recipient" 'report-metadata receiver-pane .*messages=1' "$(cat "$C/calls.log")"
nocheck "F2b and logs no verified delivery" VERIFIED "$(verified_log)"
new_case fail-read-move
send >/dev/null; courier >/dev/null; touch "$C/fail-move"
ENVX=$FP msg receiver-pane message-read.sh "$O" >/dev/null; s=$?
check "F3 a failed read transition fails the read" '^[1-9]' "$s"
check "F3b the message stays in ready with no ack" '^yes 0$' "$(yn "$(OB)/ready/$ID.md") $(nfiles "$(OB)/ack" '*')"
new_case fail-quarantine-move
send >/dev/null; jq_edit '.from="route-intruder"' "$(OB)/new/$ID.md"; touch "$C/fail-move"
courier "$FP" >/dev/null; s=$?
check "F4 a failed quarantine move keeps the entry in new and the courier exits 0" '^yes no 0$' \
  "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/quarantine/$ID.md") $s"
check "F4b and raises the suspicious entry alert" 'notification show Suspicious queue entry' "$(cat "$C/calls.log")"
new_case fail-body-read
send >/dev/null; courier >/dev/null; touch "$C/fail-cat"
out=$(ENVX=$FP msg receiver-pane message-read.sh "$O")
check "F5 a failed body read still claims and lists the message" "\"id\":\"$ID\"" "$out"
check "F5b the message moves to cur and is not acked" '^yes 0$' "$(yn "$(OB)/cur/$ID.md") $(nfiles "$(OB)/ack" '*')"
new_case fail-send
touch "$C/fail-send"
ENVX=$FP msg sender-pane message-send.sh "$A" "$O" "$C/body.txt" brief >/dev/null; s=$?
check "F6 a failed publication fails the send" '^[1-9]' "$s"
check "F6b and leaves no temp file and nothing queued" '^0 0 0$' \
  "$(nfiles "$(OB)/tmp" '.send.*') $(nfiles "$(OB)/tmp" '*') $(nfiles "$(OB)/new" '*')"
rm "$C/fail-send"
check "F6c the next send gets a fresh ID: the failed one is never reused" '^m000000000002 queued' "$(send)"
new_case fail-ack-producer
send >/dev/null; courier >/dev/null
token=$(msg receiver-pane message-read.sh "$O" | jq -r 'select(.type=="claim")|.token')
printf 'intake recorded\n' > "$C/record.txt"; printf -- '--rawfile record\n' > "$C/fail-jq"
ENVX=$FP msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt" >/dev/null; s=$?
check "F7 a failed ack producer fails the ack and commits no ack" '^[1-9][0-9]* no$' "$s $(yn "$(OB)/ack/$ID.md.json")"
rm "$C/fail-jq"
check "F7b the unhandled message is still listed for the claim" "\"id\":\"$ID\"" "$(msg receiver-pane message-read.sh "$O" "$token")"
new_case fail-atomic
# shellcheck disable=SC2016
code='mkdir -p "$ROOT/ids/probe"; producer() { printf "{}\n"; return 1; }
for leaf in peers peers.approved state/test.json enrolled/test.json alerts/test.pending state/sequence ids/probe/terminal.json \
  ids/probe/claim route-orchestrator/ack/probe.json route-orchestrator/notices/probe.json state/count-probe state/timing.json; do
  write_text "$ROOT/$leaf" 1; if atomic "$ROOT/$leaf" producer; then exit 9; fi; [ "$(cat "$ROOT/$leaf")" = 1 ] || exit 8
done'
env -i PATH="$CPATH" HOME="$HOME" HERDSMAN_INBOX="$C/inbox" FIXTURE="$C" HERDR_PANE_ID=courier-pane HERDR_SOCKET_PATH= \
  HERDSMAN_PREFLIGHT="$C/inbox/setup.json" WATCH_REQUIRED=0 /bin/bash -c '. "$1"; root_init; '"$code" probe "$MS/message-lib.sh" >/dev/null 2>&1
check "F8 a failed producer leaves every class of state file unchanged" '^0$' "$?"
new_case fail-jq-feature noenroll
printf 'IN(\n' > "$C/fail-jq"
out=$(ENVX=$FP msg sender-pane message-register.sh propose fresh adjutant); s=$?
check "F9 a jq without IN or streaming support fails enrollment" '^2 .*jq needs streaming and IN support' "$s $out"
check "F9b before any state exists" '^no$' "$(yn "$C/inbox/state")"
# F10: the exact age boundary on a fixed clock, with the system stat and with the GNU form only.
for variant in system gnu; do
  new_case "exact-age-$variant"
  [ "$variant" = gnu ] && touch "$C/stat-gnu"
  send >/dev/null; perl -e 'utime 100, 100, $ARGV[0]' "$(OB)/new/$ID.md"
  # Without the marker awk would copy the main loop; fail the case instead of running it.
  grep -q "^log_line 'courier start'" "$MS/message-courier.sh" || { fail=$((fail+1)); echo "FAIL F10 courier start marker missing"; break; }
  awk "/^log_line 'courier start'/{exit} {print}" "$MS/message-courier.sh" > "$C/courier-functions.sh"
  for at in 101 102 103; do
    printf '%s\n' "$at" > "$C/fixed-now"
    env -i PATH="$FB:$CPATH" HOME="$HOME" HERDSMAN_INBOX="$C/inbox" FIXTURE="$C" HERDR_PANE_ID=courier-pane HERDR_SOCKET_PATH= \
      HERDSMAN_PREFLIGHT="$C/inbox/setup.json" WATCH_REQUIRED=0 ALERT_AFTER=2 SWEEP=1 \
      /bin/bash -c '. "$1"; sweep' "$MS/message-courier.sh" "$C/courier-functions.sh" >/dev/null 2>&1
    printf '%s ' "$(grep -c 'notification show Unread messages' "$C/calls.log")" >> "$C/alerts-by-time"
  done
  check "F10 $variant stat: no alert one second early, one at the exact age, none repeated" '^0 1 1 $' "$(cat "$C/alerts-by-time")"
done

# S: sender identity, acks, claims, links and bounds.
new_case forged-with-ledger
send >/dev/null
jq_edit '.from="route-orchestrator"' "$(OB)/new/$ID.md"; cp "$(OB)/new/$ID.md" "$C/inbox/ids/$ID/envelope"
courier >/dev/null
check "S1 a forged from name with a matching allocation copy is still quarantined" '^yes no$' "$(yn "$(OB)/quarantine/$ID.md") $(yn "$(OB)/ready/$ID.md")"
out=$(msg sender-pane message-send.sh "$O" "$A" "$C/body.txt" brief); s=$?
check "S2 the adjutant's pane cannot send under the orchestrator's name" '^2 own pane/session changed' "$s $out"
check "S2b and nothing is queued for the adjutant" '^0$' "$(nfiles "$C/inbox/$A/new" '*')"
new_case moved-sender
set_agents moved-pane sender-session receiver-pane receiver-session
out=$(send); s=$?
check "S3 a moved sender pane must enroll again before it sends" '^2 own identity unavailable' "$s $out"
new_case identity-namespace
jq_edit '.["sender-pane"].agent_session.source="other-namespace"' "$C/agents.json"
out=$(send); s=$?
check "S4 the same session value in another identity namespace cannot send" '^2 own pane/session changed' "$s $out"
new_case reordered-keys
jq_edit 'map_values(.agent_session|=(to_entries|reverse|from_entries))' "$C/agents.json"
msg sender-pane message-register.sh propose "$A" adjutant >/dev/null
send >/dev/null; courier >/dev/null
check "S5 reordered identity keys still deliver" '^yes$' "$(yn "$(OB)/ready/$ID.md")"
check "S5b and read" "\"id\":\"$ID\"" "$(msg receiver-pane message-read.sh "$O")"
jq '[to_entries[]|{pane:.key,session:.value.agent_session}]' "$C/agents.json" > "$C/inbox/watch-targets.json"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "S5c and the card watcher accepts both targets" '^[2-9]' "$(grep -c '^pane read' "$C/calls.log")"
new_case invalid-acks
send >/dev/null; courier >/dev/null
token=$(msg receiver-pane message-read.sh "$O" | jq -r 'select(.type=="claim")|.token')
# The third ack is valid except for its claim, so only the claim compare can refuse it.
for value in '' '{}' "{\"id\":\"$ID\",\"claim\":\"wrong\",\"time\":1,\"processing_record\":\"record\",\"envelope_checksum\":\"$(digest "$(OB)/cur/$ID.md")\"}"; do
  printf '%s' "$value" > "$(OB)/ack/$ID.md.json"
  check "S6 an invalid ack (${value:-empty}) leaves the message unhandled" "\"id\":\"$ID\"" "$(msg receiver-pane message-read.sh "$O" "$token")"
done
new_case release-history
send >/dev/null; courier >/dev/null
token=$(msg receiver-pane message-read.sh "$O" | jq -r 'select(.type=="claim")|.token')
printf 'intake recorded\n' > "$C/record.txt"
msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt" >/dev/null
msg receiver-pane message-read.sh "$O" "$token" --release >/dev/null
send >/dev/null; courier >/dev/null
out=$(msg receiver-pane message-read.sh "$O")
check "S7 a new claim lists the new message" '"id":"m000000000002"' "$out"
nocheck "S7b and does not revive the released, acked one" "\"id\":\"$ID\"" "$out"
new_case incomplete-claim
send >/dev/null; courier >/dev/null; mkdir "$(OB)/claim"
out=$(msg receiver-pane message-read.sh "$O"); s=$?
check "S8 an incomplete claim is held" '^2 .*reader claim held' "$s $out"
rmdir "$(OB)/claim"
token=$(msg receiver-pane message-read.sh "$O" | jq -r 'select(.type=="claim")|.token')
check "S8b after the human clears it, a complete claim is written" "^$token yes\$" "$(cat "$(OB)/claim/token") $(yn "$(OB)/claim/owner")"
new_case alert-link
printf 'original\n' > "$C/untouched"; ln -s "$C/untouched" "$C/inbox/alerts/courier-down.pending"
touch -t 202001010000 "$C/inbox/state/heartbeat"
out=$(ENVX="DEADLINE=1 RECHECK=1" msg receiver-pane message-wait.sh "$O"); s=$?
check "S9 a linked alert state file wakes the waiter with HEALTH and is not followed" '^1 original HEALTH' "$s $(cat "$C/untouched") $out"
new_case lock-owner-link
printf 'outside\n' > "$C/outside"; mkdir "$C/inbox/courier.lock"; ln -s "$C/outside" "$C/inbox/courier.lock/pid"
msg receiver-pane message-status.sh >/dev/null; s=$?
check "S10 a linked courier lock owner is refused" '^2 outside$' "$s $(cat "$C/outside")"
new_case encoded-bound
head -c 60000 /dev/zero | tr '\0' '\001' > "$C/body.txt"
out=$(send); s=$?
check "S11 an oversized encoded envelope is refused before publication" '^2 .*encoded envelope' "$s $out"
check "S11b and nothing is queued" '^0$' "$(nfiles "$(OB)/new" '*')"
new_case changed-backlog
send >/dev/null; touch -t 202001010000 "$(OB)/new/$ID.md"; courier ALERT_AFTER=1 >/dev/null
n=$(grep -c 'notification show Unread messages' "$C/calls.log")
send >/dev/null; touch -t 202001010000 "$(OB)/new/m000000000002.md"; courier ALERT_AFTER=1 >/dev/null
check "S12 a newly overdue message alerts again although the old backlog was alerted" "^$((n+1))\$" \
  "$(grep -c 'notification show Unread messages' "$C/calls.log")"
out=$(courier SWEEP=3600 HEALTH_AFTER=180); s=$?
check "S13 a sweep longer than the health threshold refuses the courier" '^2 .*scan allowance' "$s $out"
new_case release-forms
out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh release missing); s=$?
check "S14 release with no matching claim fails" '^2 .*no such active claim' "$s $out"
send >/dev/null; courier >/dev/null
check "S15 the first delivery wakes the waiter" 'MESSAGE' "$(ENVX="DEADLINE=1 RECHECK=1" msg receiver-pane message-wait.sh "$O")"
token=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh read | jq -r 'select(.type=="claim")|.token')
check "S15b the claimed, unchanged state does not wake it" 'TICK' "$(ENVX="DEADLINE=1 RECHECK=1" msg receiver-pane message-wait.sh "$O")"
send >/dev/null; courier >/dev/null
check "S15c a second delivery wakes it again" 'MESSAGE' "$(ENVX="DEADLINE=1 RECHECK=1" msg receiver-pane message-wait.sh "$O")"
printf 'intake recorded\n' > "$C/record.txt"
msg receiver-pane message-read.sh "$O" "$token" >/dev/null
msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt" >/dev/null
msg receiver-pane message-ack.sh "$O" "$token" m000000000002 "$C/record.txt" >/dev/null
ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh release "$token" >/dev/null; s=$?
check "S16 release of a fully acked claim succeeds and leaves no claim" '^0 no$' "$s $(yn "$(OB)/claim")"

# CP: in adjutant mode the checkpointer checks the approved orchestrator identity right before each input.
ckpt() { # target [assignments]: run checkpointer.sh --once from the courier pane and print its exit status
  local target=$1; shift
  ENVX="TARGET=$target TRANSCRIPT=$C/transcript.jsonl LOG=$C/ckpt.log LIMIT=1 PROMPT=/save-memory COMPACT=/compact $*" \
    msg courier-pane checkpointer.sh --once >/dev/null; echo "$?"
}
cpcase() {
  new_case "$1"; touch "$C/allow-prompt"
  printf '%s\n' '{"type":"assistant","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}' > "$C/transcript.jsonl"
}
sent() { grep '^agent prompt' "$C/calls.log" | tr '\n' '|'; }
cpcase ckpt-ordinary
ckpt receiver-pane >/dev/null
check "CP1 ordinary mode is unchanged: memory prompt then compact, no registry needed" \
  '^agent prompt receiver-pane /save-memory\|agent prompt receiver-pane /compact\|$' "$(sent)"
cpcase ckpt-approved
rc=$(ckpt receiver-pane "HERDSMAN_ORCHESTRATOR=$O")
check "CP2 the approved orchestrator identity gets both inputs" \
  '^0 agent prompt receiver-pane /save-memory\|agent prompt receiver-pane /compact\|$' "$rc $(sent)"
cpcase ckpt-drift
jq_edit '.["receiver-pane"].agent_session.value="new-session"' "$C/agents.json"
rc=$(ckpt receiver-pane "HERDSMAN_ORCHESTRATOR=$O")
check "CP3 a changed orchestrator session gets no input" '^1 $' "$rc $(sent)"
check "CP3b and the log names the refusal" 'orchestrator session changed' "$(cat "$C/ckpt.log")"
cpcase ckpt-adjutant
rc=$(ckpt sender-pane "HERDSMAN_ORCHESTRATOR=$A")
check "CP4 the adjutant is never a checkpoint target" '^1 $' "$rc $(sent)"
check "CP4b refused as not the orchestrator" 'never type into the adjutant' "$(cat "$C/ckpt.log")"
rc=$(ckpt sender-pane "HERDSMAN_ORCHESTRATOR=$O")
check "CP4c the orchestrator name with the adjutant pane is refused" '^1 $' "$rc $(sent)"
check "CP4d as the wrong pane" 'not the approved orchestrator pane' "$(cat "$C/ckpt.log")"
cpcase ckpt-closed
jq_edit 'del(.["receiver-pane"])' "$C/agents.json"
rc=$(ckpt receiver-pane "HERDSMAN_ORCHESTRATOR=$O")
check "CP5 a closed orchestrator pane gets no input" '^1 .*closed' "$rc $(sent) $(grep 'identity check' "$C/ckpt.log")"
cpcase ckpt-pending
jq_edit '.["route-orchestrator"].identity.session.value="pending-session"' "$C/inbox/peers"
rc=$(ckpt receiver-pane "HERDSMAN_ORCHESTRATOR=$O")
check "CP6 a pending registry change holds every input" '^1 .*awaiting human approval' "$rc $(sent) $(grep 'identity check' "$C/ckpt.log")"
cpcase ckpt-midway
touch "$C/drift-after-prompt"
rc=$(ckpt receiver-pane "HERDSMAN_ORCHESTRATOR=$O")
check "CP7 drift after the memory prompt stops the compact input" '^1 agent prompt receiver-pane /save-memory\|$' "$rc $(sent)"
cpcase ckpt-unknown
rc=$(ckpt receiver-pane "HERDSMAN_ORCHESTRATOR=route-nobody")
check "CP8 an unknown orchestrator name gets no input" '^1 .*unknown orchestrator' "$rc $(sent) $(grep 'identity check' "$C/ckpt.log")"
cpcase ckpt-stale
jq_edit '.policy_verified=false' "$C/inbox/setup.json"
rc=$(ckpt receiver-pane "HERDSMAN_ORCHESTRATOR=$O")
check "CP9 a stale setup proof gets no input" '^1 .*proof stale' "$rc $(sent) $(grep 'identity check' "$C/ckpt.log")"

# AG: the executor adapter checks a relayed go's boundary, bindings, trusted interaction, expiry and cancellation,
# records consumption before the effect, and returns the recorded outcome for a duplicate.
new_case gate
relay_boundary
printf 'reviewed text\n' > "$C/draft.txt"
APR="$C/inbox/approvals"; mkdir -p "$APR/interactions"
gojson() { # request-id expires-offset [jq edit]
  jq -n --arg r "$1" --arg d "$(digest "$C/draft.txt")" --argjson e "$(( $(date +%s) + $2 ))" \
    '{version:1,type:"user_go",question_id:"choice",request_id:$r,action:"post",target:"pr-comment",revision:"head1",
      draft_hash:$d,expires_at:$e,text:"post this draft",human_interaction:("ask-"+$r)}' | jq -c "${3:-.}" > "$C/go-$1.json"
  jq -n --arg r "$1" --arg d "$(digest "$C/draft.txt")" '{source:"ask-tool",question_id:"choice",request_id:$r,revision:"head1",
    approved:true,action:"post",target:"pr-comment",draft_hash:$d}' > "$APR/interactions/ask-$1.json"
}
gate() { msg receiver-pane approval-gate.sh "$@"; echo "rc=$?"; }
consume() { gate consume "$C/go-$1.json" post "${2:-pr-comment}" "${3:-head1}" "${4:-$C/draft.txt}"; }
gojson r1 600
out=$(consume r1); check "AG1 a bound, current go is consumed" '^CONSUME r1.*rc=0' "$(printf '%s' "$out" | tr '\n' ' ')"
check "AG1b consumption is recorded before the effect" '^consuming post pr-comment head1$' \
  "$(jq -r '[.state,.action,.target,.revision]|join(" ")' "$APR/consumed/r1.json")"
out=$(consume r1); check "AG2 a repeat before the outcome must reconcile, never act again" 'RECONCILE r1.*rc=5' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(gate complete r1 posted); check "AG3 the outcome is recorded" 'COMPLETED r1 outcome=posted.*rc=0' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(consume r1); check "AG3b a duplicate returns the recorded outcome" 'DUPLICATE r1 outcome=posted.*rc=4' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(gate complete r1 posted); check "AG3c a second outcome is refused" 'outcome already recorded.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
printf 'changed text\n' > "$C/draft2.txt"
out=$(consume r1 pr-comment head1 "$C/draft2.txt"); check "AG4 a changed draft needs a new go" 'draft differs.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(consume r1 other-pr); check "AG5 a changed target needs a new go" 'target differs.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(consume r1 pr-comment head2); check "AG5b a changed revision needs a new go" 'revision differs.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r2 -5
out=$(consume r2); check "AG6 an expired go is refused" 'go expired.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
check "AG6b and records nothing" '^no$' "$(yn "$APR/consumed/r2.json")"
gojson r3 600; printf '{"version":1,"type":"cancel","request_id":"r3","revision":"head1","reason":"withdrawn"}\n' > "$APR/cancels/r3.json"
out=$(consume r3); check "AG7 a cancelled request is refused" 'request cancelled.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
check "AG7b and records nothing" '^no$' "$(yn "$APR/consumed/r3.json")"
gojson r4 600; rm "$APR/interactions/ask-r4.json"
out=$(consume r4); check "AG8 a go with no recorded ask-tool answer is refused" 'no trusted interaction.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r5 600; jq_edit '.request_id="other"' "$APR/interactions/ask-r5.json"
out=$(consume r5); check "AG8b an ask-tool answer for another request is refused" 'does not match this go.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r6 600; jq_edit '.source="pane-text"' "$APR/interactions/ask-r6.json"
out=$(consume r6); check "AG8c quoted pane text is not a trusted interaction" 'does not match this go.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r7 600; mv "$APR/interactions/ask-r7.json" "$C/ask-r7.json"; ln -s "$C/ask-r7.json" "$APR/interactions/ask-r7.json"
out=$(consume r7); check "AG8d a linked interaction record is refused" 'no trusted interaction.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r8 600 'del(.draft_hash)'
out=$(consume r8); check "AG9 a go with a missing field is refused" 'fields missing or mistyped.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r9 600 '.expires_at="soon"'
out=$(consume r9); check "AG9b a mistyped expiry is refused" 'fields missing or mistyped.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r10 600; sed 's/^{/{"action":"merge",/' "$C/go-r10.json" > "$C/go-r10.dup"; mv "$C/go-r10.dup" "$C/go-r10.json"
out=$(consume r10); check "AG9c a duplicate key is refused" 'not strict JSON.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r11 600; mkdir "$APR/action.lock"
out=$(consume r11); check "AG10 a held action lock refuses without a record" 'action lock busy.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
check "AG10b nothing consumed and the lock is not removed" '^no yes$' "$(yn "$APR/consumed/r11.json") $(yn "$APR/action.lock")"
rmdir "$APR/action.lock"
out=$(gate complete r12 posted); check "AG11 an outcome without a consumption is refused" 'no consumption recorded.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r13 600
jq_edit '.boundary.human_checked=false' "$C/inbox/setup.json"
out=$(consume r13); check "AG12 the HUMAN boundary consumes nothing" '^HUMAN.*rc=3' "$(printf '%s' "$out" | tr '\n' ' ')"
check "AG12b and records nothing" '^no$' "$(yn "$APR/consumed/r13.json")"
jq_edit '.boundary.human_checked=true' "$C/inbox/setup.json"
jq_edit '.policy_verified=false' "$C/inbox/setup.json"
out=$(consume r13); check "AG12c a stale setup proof consumes nothing" 'proof stale.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
jq_edit '.policy_verified=true' "$C/inbox/setup.json"
out=$(gate consume "$C/go-r13.json" post pr-comment head1 -); check "AG13 no draft for a draft-bound go is refused" 'draft differs.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson r14 600 '.draft_hash="none"'; jq_edit '.draft_hash="none"' "$APR/interactions/ask-r14.json"
out=$(gate consume "$C/go-r14.json" post pr-comment head1 -); check "AG13b a go without a draft binds to no draft" '^CONSUME r14.*rc=0' "$(printf '%s' "$out" | tr '\n' ' ')"
check "AG14 the gate typed nothing and called no display mutation" '^0$' "$(grep -c -E '^(agent prompt|pane |notification)' "$C/calls.log")"

# TA: the read-only transcript audit reads tool-call records, never prose, for the named current session.
TC="$T/audit"; mkdir -p "$TC/memory" "$TC/project"
aud() { env -i PATH="$CPATH" AUDIT_CHANNEL_DIR=/skill/scripts ${AUDX:-} /bin/bash "$SK/transcript-audit.sh" "$@" 2>&1; echo "rc=$?"; }
tu() { # id name input-json [session]
  jq -nc --arg s "${4:-coord-session}" --arg id "$1" --arg n "$2" --argjson i "$3" \
    '{type:"assistant",sessionId:$s,message:{content:[{type:"tool_use",id:$id,name:$n,input:$i}]}}'
}
sh_call() { tu "$1" Bash "$(jq -nc --arg c "$2" '{command:$c}')"; }
{
  jq -nc '{type:"user",sessionId:"coord-session",message:{content:"please run git push and npm test"}}'
  jq -nc '{type:"assistant",sessionId:"coord-session",message:{content:[{type:"text",text:"I will not run git push or gh pr create."}]}}'
  tu ok1 Read '{"file_path":"/any/file"}'
  sh_call ok2 '/bin/bash /skill/scripts/message.sh status'
  sh_call ok3 'git log --oneline -3'
  tu ok4 Write "$(jq -nc --arg p "$TC/memory/note.md" '{file_path:$p,content:"x"}')"
  sh_call ok5 'gh pr view 9 --json state'
} > "$TC/clean.jsonl"
out=$(aud claude "$TC/clean.jsonl" coord-session "$TC/memory/")
check "TA1 allowed calls and prose that names forbidden commands are clean" '^AUDIT clean calls=5 .*rc=0' "$(printf '%s' "$out" | tr '\n' ' ')"
{
  cat "$TC/clean.jsonl"
  tu w1 Write "$(jq -nc --arg p "$TC/project/app.ts" '{file_path:$p,content:"x"}')"
  tu w2 Edit "$(jq -nc --arg p "$TC/memory/../project/app.ts" '{file_path:$p}')"
  sh_call b1 'git commit -m change'
  sh_call b2 'cd /repo && git -C /repo push origin HEAD'
  sh_call b3 'gh pr create --title t --body-file f'
  sh_call b4 'gh api -X POST repos/o/r/issues/1/comments -f body=SECRETVALUE'
  sh_call b5 'curl -X POST https://example.invalid/hook'
  sh_call b6 'npm test'
  sh_call b7 '/bin/bash scripts/test/run-tests.sh'
  sh_call b8 'docker compose up -d'
  tu m1 mcp__chat__send_message '{"text":"hello"}'
} > "$TC/flagged.jsonl"
out=$(aud claude "$TC/flagged.jsonl" coord-session "$TC/memory/")
for want in 'w1 class=project-write' 'w2 class=project-write' 'b1 class=git-write' 'b2 class=git-write' 'b3 class=outward-write' \
  'b4 class=outward-write' 'b5 class=outward-write' 'b6 class=test-or-package' 'b7 class=test-or-package' \
  'b8 class=service-or-container' 'm1 class=tracker-or-chat-write'; do
  check "TA2 flagged: ${want%% *} ${want#* }" "call=$want" "$out"
done
check "TA2b the audit reports the flagged count and exits 1" 'AUDIT flagged=11 calls=16.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
nocheck "TA2c targets are redacted: no argument values or full paths" "SECRETVALUE|$TC/project" "$out"
check "TA2d a flag names the tool and the call id" '^FLAG tool=Bash call=b1 ' "$out"
out=$(aud claude "$TC/clean.jsonl" other-session "$TC/memory/")
check "TA3 another session's transcript is an audit gap, never clean" '^GAP transcript is not the named current session.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
{ cat "$TC/clean.jsonl"; sh_call x1 'git push' | sed 's/coord-session/other-session/'; } > "$TC/mixed.jsonl"
out=$(aud claude "$TC/mixed.jsonl" coord-session "$TC/memory/"); check "TA3b records of two sessions are a gap" '^GAP .*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(aud claude "$TC/missing.jsonl" coord-session); check "TA4 a missing transcript is a gap" '^GAP transcript missing.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(aud opencode "$TC/clean.jsonl" coord-session); check "TA4b an unsupported harness is a gap" '^GAP no transcript adapter.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
{ cat "$TC/clean.jsonl"; printf '{"type":"assistant",\n'; } > "$TC/broken.jsonl"
out=$(aud claude "$TC/broken.jsonl" coord-session); check "TA4c a malformed record is a gap" '^GAP malformed transcript record.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(aud claude "audit/clean.jsonl" coord-session); check "TA4d a relative transcript path is a gap" '^GAP transcript path must be absolute.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
: > "$TC/empty.jsonl"; out=$(aud claude "$TC/empty.jsonl" coord-session); check "TA4e an empty transcript is a gap" '^GAP no session record.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
ln -s "$TC/clean.jsonl" "$TC/link.jsonl"; out=$(aud claude "$TC/link.jsonl" coord-session); check "TA4f a linked transcript is a gap" '^GAP transcript missing or not a regular file.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
printf '%s\n' 'git commit -m change' > "$TC/grants"
out=$(AUDX="AUDIT_GRANT_FILE=$TC/grants" aud claude "$TC/flagged.jsonl" coord-session "$TC/memory/")
nocheck "TA5 an exact granted command line is not flagged" 'call=b1 ' "$out"
check "TA5b and the grant exempts no other line" 'call=b2 class=git-write' "$out"
{
  jq -nc '{type:"session_meta",payload:{id:"codex-session"}}'
  jq -nc '{type:"response_item",payload:{type:"function_call",name:"exec_command",call_id:"c1",arguments:"{\"cmd\":\"/bin/bash /skill/scripts/message.sh read\"}"}}'
  jq -nc '{type:"response_item",payload:{type:"function_call",name:"shell",call_id:"c2",arguments:"{\"command\":[\"bash\",\"-lc\",\"git push origin HEAD\"]}"}}'
  jq -nc --arg p "*** Begin Patch
*** Update File: $TC/project/app.ts
@@
-a
+b
*** End Patch" '{type:"response_item",payload:{type:"custom_tool_call",name:"apply_patch",call_id:"c3",input:$p}}'
  jq -nc --arg p "*** Begin Patch
*** Add File: $TC/memory/note.md
+x
*** End Patch" '{type:"response_item",payload:{type:"custom_tool_call",name:"apply_patch",call_id:"c4",input:$p}}'
  jq -nc '{type:"response_item",payload:{type:"message",role:"assistant",content:[{type:"output_text",text:"git push is not mine to run"}]}}'
} > "$TC/codex.jsonl"
out=$(aud codex "$TC/codex.jsonl" codex-session "$TC/memory/")
check "TA6 Codex: a shell git push is flagged" 'call=c2 class=git-write' "$out"
check "TA6b Codex: a patch outside the allowed roots is flagged" 'call=c3 class=project-write' "$out"
nocheck "TA6c Codex: channel commands and an allowed patch are not flagged" 'call=c1 |call=c4 ' "$out"
check "TA6d Codex: two flags of four calls" 'AUDIT flagged=2 calls=4.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(aud codex "$TC/codex.jsonl" other-session); check "TA6e Codex: another session is a gap" '^GAP transcript is not the named current session.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
jq -nc '{type:"response_item",payload:{type:"function_call",name:"shell",call_id:"c9",arguments:"not json"}}' > "$TC/codex-bad.jsonl"
{ jq -nc '{type:"session_meta",payload:{id:"codex-session"}}'; cat "$TC/codex-bad.jsonl"; } > "$TC/codex-bad2.jsonl"
out=$(aud codex "$TC/codex-bad2.jsonl" codex-session); check "TA6f Codex: unreadable call arguments are a gap" '^GAP malformed tool-call arguments: call c9.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
# TA8: a push hidden in a script is not visible in command text, so any script or interpreter run is not clean
# unless it starts a channel script by its exact installed path.
{
  jq -nc '{type:"user",sessionId:"coord-session",message:{content:"start"}}'
  sh_call s1 'bash /tmp/do-push.sh'
  sh_call s2 './deploy.sh --now'
  sh_call s3 'python3 -c "import os"'
  sh_call s4 '/bin/bash /skill/scripts/../tmp/message.sh status'
  sh_call s5 '/bin/bash /other/scripts/message.sh status'
  sh_call s6 'source ./env.sh'
  sh_call s7 'sh -c true'
  sh_call s8 'ls /tmp && /bin/bash /skill/scripts/message.sh status && perl x.pl'
  sh_call s9 'env X=1 /skill/scripts/hidden.sh'
  sh_call s10 '/bin/bash -c /skill/scripts/message.sh'
  sh_call k1 'ls -la /tmp'
  sh_call k2 'cd /x && /bin/bash /skill/scripts/message.sh read | jq -c .'
  sh_call k3 "HERDSMAN_NAME=route-adjutant /bin/bash '/skill/scripts/message.sh' status"
  sh_call k4 'cat /x/notes.md'
} > "$TC/scripts.jsonl"
out=$(aud claude "$TC/scripts.jsonl" coord-session)
for n in 1 2 3 4 5 6 7 8 9 10; do check "TA8 a script or interpreter run is flagged: s$n" "call=s$n class=script-run" "$out"; done
nocheck "TA8b exact channel scripts and plain reads stay clean" 'call=k[0-9]' "$out"
check "TA8c ten flags of fourteen calls" 'AUDIT flagged=10 calls=14.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(env -i PATH="$CPATH" /bin/bash "$SK/transcript-audit.sh" claude "$TC/scripts.jsonl" coord-session 2>&1)
check "TA8d without the channel folder even a channel script run is flagged" 'call=k2 class=script-run' "$out"
before=$(digest "$TC/flagged.jsonl"); aud claude "$TC/flagged.jsonl" coord-session >/dev/null
check "TA7 the audit leaves the transcript unchanged" "^$before\$" "$(digest "$TC/flagged.jsonl")"

# Z: the final design fixes, each reproduced with the failing case first.
# Z1: an installed folder may hold other reviewed scripts. Every listed script is checked, every script of the folder
# must be listed, and the adapters and the checkpointer beside the folder are bound by checksum too.
new_case install
mkdir -p "$C/skill/messages"; cp "$MS"/*.sh "$C/skill/messages/"
for s in approval-gate transcript-audit checkpointer-guard checkpointer; do cp "$SK/$s.sh" "$C/skill/"; done
printf '#!/bin/bash\nexit 0\n' > "$C/skill/messages/existing-route-check.sh"
set_scripts() { # glob ...: record exactly these installed files in the setup proof
  local s f; s=$(for f in "$@"; do jq -n --arg p "$f" --arg c "$(digest "$f")" '{checksum:$c,path:$p}'; done | jq -sc .)
  jq --argjson s "$s" '.scripts=$s' "$C/inbox/setup.json" > "$C/edit.tmp" && mv "$C/edit.tmp" "$C/inbox/setup.json"
}
isend() { # one send from the installed copy
  env -i PATH="$CPATH" HOME="$HOME" HERDSMAN_INBOX="$C/inbox" FIXTURE="$C" HERDR_PANE_ID=sender-pane HERDR_SOCKET_PATH= \
    HERDSMAN_PREFLIGHT="$C/inbox/setup.json" WATCH_REQUIRED=0 /bin/bash "$C/skill/messages/message-send.sh" "$A" "$O" "$C/body.txt" brief 2>&1
}
iadapter() { # script args: one adapter run from the installed copy
  local s=$1; shift
  env -i PATH="$CPATH" HOME="$HOME" HERDSMAN_INBOX="$C/inbox" FIXTURE="$C" HERDR_PANE_ID=receiver-pane HERDR_SOCKET_PATH= \
    HERDSMAN_PREFLIGHT="$C/inbox/setup.json" WATCH_REQUIRED=0 /bin/bash "$C/skill/$s" "$@" 2>&1
}
set_scripts "$C/skill/messages/"*.sh "$C/skill/"*.sh
check "Z1 an installed folder with one more reviewed script still sends" ' queued' "$(isend)"
set_scripts "$C/skill/messages/"[a-df-z]*.sh "$C/skill/"*.sh
check "Z1b a script of the folder missing from the proof makes it stale" 'proof stale' "$(isend)"
set_scripts "$C/skill/messages/"*.sh "$C/skill/checkpointer.sh"
check "Z1c an adapter beside the folder missing from the proof makes it stale" 'proof stale' "$(isend)"
set_scripts "$C/skill/messages/"*.sh "$C/skill/"*.sh
printf '# changed\n' >> "$C/skill/approval-gate.sh"
check "Z1d a changed executor adapter makes the proof stale for the channel" 'proof stale' "$(isend)"
check "Z1e and the changed adapter refuses to run" 'proof stale' "$(iadapter approval-gate.sh complete r1 posted)"
check "Z1f and the checkpointer guard refuses input" 'proof stale' "$(iadapter checkpointer-guard.sh "$O" receiver-pane)"
set_scripts "$C/skill/messages/"*.sh "$C/skill/"*.sh
check "Z1g the proof of the changed bytes is the positive control" ' queued' "$(isend)"
printf '# changed\n' >> "$C/skill/transcript-audit.sh"
check "Z1h a changed transcript audit makes the proof stale" 'proof stale' "$(isend)"

# Z2: an agent outside the protected setup taints the boundary; its exit does not restore automatic coordination.
new_case taint
set_agents sender-pane sender-session receiver-pane receiver-session task-pane task-session
jq -n '[{pane:"task-pane",session:{source:"hook",agent:"claude",kind:"id",value:"task-session"}}]' > "$C/inbox/watch-targets.json"
printf '%s\n' '✦ Heads up · taint finding' > "$C/screen-task-pane"
relay_boundary
check "Z2 the protected boundary is the positive control" '^RELAY' "$(msg receiver-pane message-authority.sh)"
jq_edit '.["rogue-pane"]=(.["task-pane"]|.pane_id="rogue-pane"|.agent_session.value="rogue-session")' "$C/agents.json"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "Z2b an unknown live agent is recorded as boundary exposure" 'rogue-pane' "$(cat "$C/inbox/state/boundary-tainted" 2>&1)"
jq_edit 'del(.["rogue-pane"])' "$C/agents.json"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "Z2c after that agent exits the authority stays HUMAN" '^HUMAN' "$(msg receiver-pane message-authority.sh)"
check "Z2d and no card was relayed" '^0 yes$' "$(nfiles "$C/inbox/$A/new" '*.md') $(yn "$C/inbox/state/automation-held")"
# A closed lower-trust pane is no risk: its record need not stay live.
new_case closed-task
set_agents sender-pane sender-session receiver-pane receiver-session task-pane task-session; relay_boundary
set_agents sender-pane sender-session receiver-pane receiver-session
check "Z2e a closed lower-trust pane keeps RELAY" '^RELAY' "$(msg receiver-pane message-authority.sh)"
# One failed agent list holds automation quietly and taints nothing; a repeated failure alerts.
jq -n '[{pane:"receiver-pane",session:{source:"hook",agent:"claude",kind:"id",value:"receiver-session"}}]' > "$C/inbox/watch-targets.json"
touch "$C/list-failure-once"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
nocheck "Z2f one failed agent list does not alert the human" 'notification show Automatic coordination held' "$(cat "$C/calls.log")"
check "Z2g and records no exposure" '^no$' "$(yn "$C/inbox/state/boundary-tainted")"
touch "$C/list-failure"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "Z2h a repeated agent list failure alerts" 'notification show Automatic coordination held' "$(cat "$C/calls.log")"
check "Z2i and still records no exposure" '^no$' "$(yn "$C/inbox/state/boundary-tainted")"
rm "$C/list-failure"

# Z3: automatic coordination needs the explicit launch admission record of the protected setup.
new_case admission
relay_boundary; jq_edit '.boundary|=del(.launch_admission)' "$C/inbox/setup.json"
check "Z3 without the launch admission record the authority is HUMAN" '^HUMAN' "$(msg receiver-pane message-authority.sh)"
jq_edit '.boundary.launch_admission=false' "$C/inbox/setup.json"
check "Z3b a false admission record is HUMAN" '^HUMAN' "$(msg receiver-pane message-authority.sh)"

# Z4: an admission that wins the race against a card relay prevents the enqueue.
new_case relay-race
set_agents sender-pane sender-session receiver-pane receiver-session task-pane task-session
jq -n '[{pane:"task-pane",session:{source:"hook",agent:"claude",kind:"id",value:"task-session"}}]' > "$C/inbox/watch-targets.json"
printf '%s\n' '✦ Heads up · race finding' > "$C/screen-task-pane"
relay_boundary
mkdir "$C/race-bin"
# The orchestrator transport's own identity check runs inside the send; an admission writer acts during it.
printf '%s\n' '#!/bin/bash' \
  'if [ "$1 $2 $3" = "agent get receiver-pane" ] && [ ! -e "$FIXTURE/admitted" ]; then' \
  '  mkdir "$HERDSMAN_INBOX/herdr-mutation.lock"' \
  '  printf "unsandboxed admission during the send\n" > "$HERDSMAN_INBOX/state/boundary-tainted"' \
  '  jq ".[\"rogue-pane\"]=(.[\"task-pane\"]|.pane_id=\"rogue-pane\"|.agent_session.value=\"rogue-session\")" "$FIXTURE/agents.json" > "$FIXTURE/agents.tmp"' \
  '  mv "$FIXTURE/agents.tmp" "$FIXTURE/agents.json"; rmdir "$HERDSMAN_INBOX/herdr-mutation.lock"; touch "$FIXTURE/admitted"' \
  'fi' "exec $SK/test/fakebin/herdr \"\$@\"" > "$C/race-bin/herdr"
chmod +x "$C/race-bin/herdr"
ENVX="RUN_FOR=1 INTERVAL=1 PATH=$C/race-bin:$CPATH" msg courier-pane card-watch.sh >/dev/null
check "Z4 the admission happened during the relay" '^yes$' "$(yn "$C/admitted")"
check "Z4b no card was queued after the admission" '^0$' "$(nfiles "$C/inbox/$A/new" '*.md')"
check "Z4c and the authority is HUMAN" '^HUMAN' "$(msg receiver-pane message-authority.sh)"

# Z4d: the send holds the input lock from its boundary check through the enqueue. An admission writer that starts
# during the send's agent list waits for the lock, so it sees the card already queued, never queued after it.
new_case lock-window
set_agents sender-pane sender-session receiver-pane receiver-session task-pane task-session
jq -n '[{pane:"task-pane",session:{source:"hook",agent:"claude",kind:"id",value:"task-session"}}]' > "$C/inbox/watch-targets.json"
printf '%s\n' '✦ Heads up · window finding' > "$C/screen-task-pane"
relay_boundary
mkdir "$C/race-bin"
printf '%s\n' '#!/bin/bash' \
  'if [ "$1 $2" = "agent list" ]; then n=0; [ ! -f "$FIXTURE/list-count" ] || n=$(cat "$FIXTURE/list-count"); n=$((n+1)); echo "$n" > "$FIXTURE/list-count"' \
  '  if [ "$n" = 3 ]; then (' \
  '    until mkdir "$HERDSMAN_INBOX/herdr-mutation.lock" 2>/dev/null; do sleep 0.1; done' \
  '    q=0; for f in "$HERDSMAN_INBOX"/route-adjutant/new/*.md; do [ ! -e "$f" ] || q=$((q+1)); done' \
  '    rmdir "$HERDSMAN_INBOX/herdr-mutation.lock"; echo "$q" > "$FIXTURE/queued-at-admission"' \
  '  ) </dev/null >/dev/null 2>&1 & fi' \
  'fi' "exec $SK/test/fakebin/herdr \"\$@\"" > "$C/race-bin/herdr"
chmod +x "$C/race-bin/herdr"
ENVX="RUN_FOR=1 INTERVAL=1 PATH=$C/race-bin:$CPATH" msg courier-pane card-watch.sh >/dev/null
i=0; while [ ! -f "$C/queued-at-admission" ] && [ "$i" -lt 100 ]; do sleep 0.1; i=$((i+1)); done
check "Z4d an admission during the send's boundary check waits until the card is queued" '^1$' "$(cat "$C/queued-at-admission" 2>&1)"
# Z4e: an unknown agent that only the send's locked recheck sees is still recorded as exposure.
new_case send-exposure
set_agents sender-pane sender-session receiver-pane receiver-session task-pane task-session
jq -n '[{pane:"task-pane",session:{source:"hook",agent:"claude",kind:"id",value:"task-session"}}]' > "$C/inbox/watch-targets.json"
printf '%s\n' '✦ Heads up · brief finding' > "$C/screen-task-pane"
relay_boundary
mkdir "$C/race-bin"
printf '%s\n' '#!/bin/bash' \
  'if [ "$1 $2" = "agent list" ] && [ -d "$HERDSMAN_INBOX/herdr-mutation.lock" ]; then' \
  '  jq -c "{id:\"fixture-response\",result:{type:\"agent_list\",agents:([.[]]+[.[\"task-pane\"]|.pane_id=\"brief-pane\"|.agent_session.value=\"brief-session\"])}}" "$FIXTURE/agents.json"; exit 0' \
  'fi' "exec $SK/test/fakebin/herdr \"\$@\"" > "$C/race-bin/herdr"
chmod +x "$C/race-bin/herdr"
ENVX="RUN_FOR=1 INTERVAL=1 PATH=$C/race-bin:$CPATH" msg courier-pane card-watch.sh >/dev/null
check "Z4e a short-lived agent seen only by the send's recheck is recorded" 'brief-pane' "$(cat "$C/inbox/state/boundary-tainted" 2>&1)"
check "Z4f and no card was queued" '^0$' "$(nfiles "$C/inbox/$A/new" '*.md')"

# Z5: an ack retry never overwrites a different permanent disposition.
new_case ack-terminal
send >/dev/null; courier >/dev/null
token=$(msg receiver-pane message-read.sh "$O" | jq -r 'select(.type=="claim")|.token')
printf 'intake recorded\n' > "$C/record.txt"
msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt" >/dev/null
out=$(msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt"); st=$?
check "Z5 a retry with the matching terminal is idempotent" "^0 acknowledged $ID" "$st $out"
printf '{"id":"%s","state":"quarantined","evidence":"keep"}\n' "$ID" > "$C/inbox/ids/$ID/terminal.json"
before=$(digest "$C/inbox/ids/$ID/terminal.json")
out=$(msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt"); st=$?
check "Z5b a retry over a conflicting terminal is refused" '^2 .*terminal' "$st $out"
check "Z5c and the conflicting terminal is kept" "^$before\$" "$(digest "$C/inbox/ids/$ID/terminal.json")"
printf 'not json\n' > "$C/inbox/ids/$ID/terminal.json"
out=$(msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt"); st=$?
check "Z5d a corrupt terminal is held too" '^2 not json$' "$st $(cat "$C/inbox/ids/$ID/terminal.json")"

# Z6: a disposed sender notice stays disposed; an interrupted first publication is still repaired.
new_case notice-disposed
send >/dev/null
jq_edit '.from_identity.session.value="forged"' "$(OB)/new/$ID.md"
courier >/dev/null
check "Z6 the forged envelope is quarantined with a sender notice" '^yes$' "$(yn "$C/inbox/$A/notices/$ID.md.json")"
mv "$C/inbox/$A/notices/$ID.md.json" "$C/disposed-notice.json"
touch -t 202001010000 "$C/inbox/ids/$ID/terminal.json"; before=$(ls -l "$C/inbox/ids/$ID/terminal.json")
courier >/dev/null
check "Z6b the next sweep does not recreate a disposed notice" '^no yes$' "$(yn "$C/inbox/$A/notices/$ID.md.json") $(yn "$(OB)/quarantine/$ID.md")"
check "Z6c and does not rewrite the reconciled terminal" "^$before\$" "$(ls -l "$C/inbox/ids/$ID/terminal.json")"
rm -f "$C/inbox/ids/$ID/notice-published"
courier >/dev/null
check "Z6d a notice whose publication was not completed is repaired" '^yes$' "$(yn "$C/inbox/$A/notices/$ID.md.json")"

# Z7: two overlapping resume helpers send one resume per episode.
new_case resume-race
jq_edit '.["task-pane"]=(.["sender-pane"]|.pane_id="task-pane"|.agent="codex"|.agent_session.agent="codex"|.agent_session.value="task-session")' "$C/agents.json"
jq -n '{"im-task":{role:"task-agent",identity:{pane:"task-pane",session:{source:"hook",agent:"codex",kind:"id",value:"task-session"}}}}' > "$C/inbox/resume-targets.json"
printf 'Goal paused (/goal resume)\n' > "$C/screen-task-pane"; touch "$C/allow-resume"; relay_boundary
rkey=$(jq -c '.["im-task"].identity' "$C/inbox/resume-targets.json" | jq -Sc . | cksum | awk '{print $1 "-" $2}')
mkdir "$C/race-bin"
# The other helper records its intent between this helper's first intent check and its lock.
printf '%s\n' '#!/bin/bash' \
  'if [ "$*" = "$HERDSMAN_INBOX/herdr-mutation.lock" ]; then printf "other helper intent\n" > "$HERDSMAN_INBOX/state/resume-$RACE_KEY.sent"; fi' \
  'exec /bin/mkdir "$@"' > "$C/race-bin/mkdir"
chmod +x "$C/race-bin/mkdir"
out=$(ENVX="PATH=$C/race-bin:$CPATH RACE_KEY=$rkey" msg sender-pane resume-goal-when-idle.sh im-task)
check "Z7 the helper that lost the race holds" '^HELD reconcile the previous resume' "$out"
check "Z7b and sends no second resume" '^0$' "$(grep -c '^agent prompt' "$C/calls.log")"

# Z8: the courier rechecks the setup proof before each queue change; a stale proof moves nothing and alerts.
new_case courier-stale
send >/dev/null
mkdir "$C/race-bin"
printf '%s\n' '#!/bin/bash' \
  'if [ "$1 $2 $3" = "agent get sender-pane" ]; then printf "changed while the courier runs\n" >> "$FIXTURE/ignore-rule"; fi' \
  "exec $SK/test/fakebin/herdr \"\$@\"" > "$C/race-bin/herdr"
chmod +x "$C/race-bin/herdr"
courier "PATH=$C/race-bin:$CPATH" >/dev/null
check "Z8 a policy changed during the sweep moves nothing to ready" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
check "Z8b and the human is alerted" 'notification show Message setup proof stale' "$(cat "$C/calls.log")"
courier >/dev/null
check "Z8c a courier started on the stale proof refuses" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"

# Z9: hold and unhold publish a named hold under the shared input lock.
new_case hold
jq_edit '.["task-pane"]=(.["sender-pane"]|.pane_id="task-pane"|.agent="codex"|.agent_session.agent="codex"|.agent_session.value="task-session")' "$C/agents.json"
jq -n '{"im-task":{role:"task-agent",identity:{pane:"task-pane",session:{source:"hook",agent:"codex",kind:"id",value:"task-session"}}}}' > "$C/inbox/resume-targets.json"
printf 'Goal paused (/goal resume)\n' > "$C/screen-task-pane"; relay_boundary
out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh hold im-task 'user paused this task'); st=$?
check "Z9 hold records the named hold" '^0 HOLD im-task' "$st $out"
check "Z9b with the holder and the reason" '^user paused this task$' "$(jq -r --arg o "$O" '.holds[$o].reason' "$C/inbox/state/hold-im-task")"
check "Z9c the resume helper then holds" 'HELD user pause or freeze' "$(msg sender-pane resume-goal-when-idle.sh im-task)"
mkdir "$C/inbox/herdr-mutation.lock"
out=$(ENVX="HERDSMAN_NAME=$O LOCK_WAIT=1" msg receiver-pane message.sh unhold im-task); st=$?
check "Z9d unhold waits for the shared lock and refuses when busy" '^2 .*lock busy' "$st $out"
rmdir "$C/inbox/herdr-mutation.lock"
out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh unhold im-task); st=$?
check "Z9e unhold clears the hold" "^0 UNHOLD im-task by $O; no hold remains no\$" "$st $out $(yn "$C/inbox/state/hold-im-task")"
out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh hold '../x' reason); st=$?
check "Z9f an invalid hold name is refused" '^2 ' "$st $out"
out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh hold im-typo reason); st=$?
check "Z9f2 a hold for a name that is neither a peer nor a resume target is refused" '^2 .*unknown hold target' "$st $out"
# Z9g: a hold is cleared only by its owner; another coordinator's hold stays, and resume stays held while any hold stays.
ENVX="HERDSMAN_NAME=$A" msg sender-pane message.sh hold im-task 'the user paused this task' >/dev/null
out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh unhold im-task); st=$?
check "Z9g the orchestrator cannot clear the adjutant's user-pause hold" "^2 .*no hold by $O" "$st $out"
check "Z9h and the adjutant's hold stays" '^the user paused this task$' "$(jq -r --arg a "$A" '.holds[$a].reason' "$C/inbox/state/hold-im-task")"
ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh hold im-task 'stopping the task' >/dev/null
check "Z9i a second owner's hold does not overwrite the first" '^the user paused this task stopping the task$' \
  "$(jq -r --arg a "$A" --arg o "$O" '.holds[$a].reason + " " + .holds[$o].reason' "$C/inbox/state/hold-im-task")"
ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh unhold im-task >/dev/null
check "Z9j the orchestrator's unhold removes only its own hold" "^$A\$" "$(jq -r '.holds|keys|join(" ")' "$C/inbox/state/hold-im-task")"
out=$(msg sender-pane resume-goal-when-idle.sh im-task)
check "Z9k the resume stays held and names the remaining owner" "^HELD user pause or freeze.*$A" "$out"
check "Z9l and no resume was sent" '^0$' "$(grep -c '^agent prompt' "$C/calls.log")"
ENVX="HERDSMAN_NAME=$A" msg sender-pane message.sh unhold im-task >/dev/null
check "Z9m the last owner's unhold clears the record" '^no$' "$(yn "$C/inbox/state/hold-im-task")"
printf 'held by hand\n' > "$C/inbox/state/hold-im-task"
out=$(ENVX="HERDSMAN_NAME=$O" msg receiver-pane message.sh unhold im-task); st=$?
check "Z9n a hold record in another format is never changed" '^2 .*owner format.* held by hand$' "$st $out $(cat "$C/inbox/state/hold-im-task")"
check "Z9o and still holds the resume" '^HELD user pause or freeze' "$(msg sender-pane resume-goal-when-idle.sh im-task)"

# Z10: a deliberate hold is quiet in the watcher; only uncertain resume states alert.
new_case quiet-hold
jq_edit '.["task-pane"]=(.["sender-pane"]|.pane_id="task-pane"|.agent="codex"|.agent_session.agent="codex"|.agent_session.value="task-session")' "$C/agents.json"
jq -n '{"im-task":{role:"task-agent",identity:{pane:"task-pane",session:{source:"hook",agent:"codex",kind:"id",value:"task-session"}}}}' > "$C/inbox/resume-targets.json"
jq -n '[{pane:"receiver-pane",session:{source:"hook",agent:"claude",kind:"id",value:"receiver-session"}}]' > "$C/inbox/watch-targets.json"
printf 'Goal paused (/goal resume)\n' > "$C/screen-task-pane"; relay_boundary
touch "$C/inbox/state/route-paused"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
nocheck "Z10 a user pause sends no resume-held alert" 'notification show Goal resume held' "$(cat "$C/calls.log")"
check "Z10b the quiet hold is still visible in status" 'hold-or-intent: resume-held-im-task' "$(msg receiver-pane message-status.sh "$O")"

# Z11: a required section includes its child headings and ends at the next heading of the same or a higher level.
printf '# Tests\n## Unit\nPlain test detail.\n' > "$T/nested.md"
printf '# Tests\n# Other\nPlain test detail.\n' > "$T/sibling.md"
check "Z11 a required section with text under a child heading passes" '^0$' "$(leak "$T/nested.md" '# Tests' >/dev/null 2>&1; echo $?)"
check "Z11b a sibling heading still ends the section" '^1$' "$(leak "$T/sibling.md" '# Tests' >/dev/null 2>&1; echo $?)"

# Y: the second branch review fixes, each reproduced with the failing case first.
# Y1: the trusted interaction must be an affirmative answer bound to the exact action, target and draft.
new_case gate-answer
relay_boundary
printf 'reviewed text\n' > "$C/draft.txt"
APR="$C/inbox/approvals"; mkdir -p "$APR/interactions"
gojson y1 600; jq_edit '.approved=false|.answer="No, do not post"' "$APR/interactions/ask-y1.json"
out=$(consume y1); check "Y1 a negative ask-tool answer is refused" 'REFUSED .*not an affirmative.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson y2 600; jq_edit 'del(.approved)' "$APR/interactions/ask-y2.json"
out=$(consume y2); check "Y1b an answer with no approval is refused" 'REFUSED .*not an affirmative.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson y3 600; jq_edit '.action="merge"' "$APR/interactions/ask-y3.json"
out=$(consume y3); check "Y1c an answer that approves another action is refused" 'REFUSED .*approves another action.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson y4 600; jq_edit '.target="other-pr"' "$APR/interactions/ask-y4.json"
out=$(consume y4); check "Y1d an answer that approves another target is refused" 'REFUSED .*approves another action.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
gojson y5 600; jq_edit '.draft_hash="1-1"' "$APR/interactions/ask-y5.json"
out=$(consume y5); check "Y1e an answer that approves another draft is refused" 'REFUSED .*approves another action.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
check "Y1f and none of them consumed anything" '^0$' "$(nfiles "$APR/consumed" '*.json')"
gojson y6 600
out=$(consume y6); check "Y1g the bound affirmative answer is the positive control" '^CONSUME y6.*rc=0' "$(printf '%s' "$out" | tr '\n' ' ')"
# Y2: the gate rechecks the boundary under the input lock at consumption. An admission writer that runs during the
# authority check waits for that lock, so a consumption never follows an exposure record.
new_case gate-race
relay_boundary
printf 'reviewed text\n' > "$C/draft.txt"
APR="$C/inbox/approvals"; mkdir -p "$APR/interactions"
gojson y7 600
mkdir "$C/race-bin"
printf '%s\n' '#!/bin/bash' \
  'if [ "$1 $2" = "agent list" ] && [ ! -e "$FIXTURE/admitted" ]; then touch "$FIXTURE/admitted"' \
  '  admit() { s=no; [ ! -e "$HERDSMAN_INBOX/approvals/consumed/y7.json" ] || s=yes; printf "exposure\n" > "$HERDSMAN_INBOX/state/boundary-tainted"; rmdir "$HERDSMAN_INBOX/herdr-mutation.lock"; echo "$s" > "$FIXTURE/consumed-at-exposure"; }' \
  '  if mkdir "$HERDSMAN_INBOX/herdr-mutation.lock" 2>/dev/null; then admit' \
  '  else ( until mkdir "$HERDSMAN_INBOX/herdr-mutation.lock" 2>/dev/null; do sleep 0.1; done; admit ) </dev/null >/dev/null 2>&1 & fi' \
  'fi' "exec $SK/test/fakebin/herdr \"\$@\"" > "$C/race-bin/herdr"
chmod +x "$C/race-bin/herdr"
out=$(ENVX="PATH=$C/race-bin:$CPATH" consume y7)
i=0; while [ ! -f "$C/consumed-at-exposure" ] && [ "$i" -lt 100 ]; do sleep 0.1; i=$((i+1)); done
check "Y2 an exposure during the authority check never precedes a consumption" '^(CONSUME yes|HUMAN.*no)$' \
  "$(printf '%s' "$out" | head -1 | awk '{print $1}') $(cat "$C/consumed-at-exposure" 2>&1)"
# Y3: in adjutant mode the checkpointer sends input only while it holds the shared input lock.
cpcase ckpt-lock
mkdir "$C/inbox/herdr-mutation.lock"
rc=$(ckpt receiver-pane "HERDSMAN_ORCHESTRATOR=$O LOCK_WAIT=1")
check "Y3 a held input lock stops the checkpoint input" '^1 $' "$rc $(sent)"
check "Y3b the lock is not taken over and the log names it" '^yes .*input lock busy' "$(yn "$C/inbox/herdr-mutation.lock") $(cat "$C/ckpt.log")"
rmdir "$C/inbox/herdr-mutation.lock"
rc=$(ckpt receiver-pane "HERDSMAN_ORCHESTRATOR=$O")
check "Y3c with the lock free the inputs go out and the lock is released" '^0 agent prompt receiver-pane /save-memory.* no$' "$rc $(sent) $(yn "$C/inbox/herdr-mutation.lock")"
# Y4: a card relay observes Herdr outside the queue lock.
new_case send-observe
set_agents sender-pane sender-session receiver-pane receiver-session task-pane task-session
jq -n '[{pane:"task-pane",session:{source:"hook",agent:"claude",kind:"id",value:"task-session"}}]' > "$C/inbox/watch-targets.json"
printf '%s\n' '✦ Heads up · observe finding' > "$C/screen-task-pane"
relay_boundary
mkdir "$C/race-bin"
printf '%s\n' '#!/bin/bash' \
  'if [ "$1 $2" = "agent list" ] && [ -d "$HERDSMAN_INBOX/channel.lock" ]; then touch "$FIXTURE/list-under-queue-lock"; fi' \
  "exec $SK/test/fakebin/herdr \"\$@\"" > "$C/race-bin/herdr"
chmod +x "$C/race-bin/herdr"
ENVX="RUN_FOR=1 INTERVAL=1 PATH=$C/race-bin:$CPATH" msg courier-pane card-watch.sh >/dev/null
check "Y4 the card is relayed and no agent list ran under the queue lock" '^1 no$' "$(nfiles "$C/inbox/$A/new" '*.md') $(yn "$C/list-under-queue-lock")"
# Y5: the watcher writes the exposure record under the shared input lock.
new_case exposure-lock
set_agents sender-pane sender-session receiver-pane receiver-session task-pane task-session
jq -n '[{pane:"task-pane",session:{source:"hook",agent:"claude",kind:"id",value:"task-session"}}]' > "$C/inbox/watch-targets.json"
relay_boundary
jq_edit '.["rogue-pane"]=(.["task-pane"]|.pane_id="rogue-pane"|.agent_session.value="rogue-session")' "$C/agents.json"
mkdir "$C/race-bin"
printf '%s\n' '#!/bin/bash' \
  'if [ "$1" = "-f" ] && [ "${2##*/}" = "boundary-tainted" ] || [ "${3##*/}" = "boundary-tainted" ]; then [ -d "$HERDSMAN_INBOX/herdr-mutation.lock" ] && echo held > "$FIXTURE/exposure-barrier" || echo free > "$FIXTURE/exposure-barrier"; fi' \
  'exec /bin/mv "$@"' > "$C/race-bin/mv"
chmod +x "$C/race-bin/mv"
ENVX="RUN_FOR=1 INTERVAL=1 PATH=$C/race-bin:$CPATH" msg courier-pane card-watch.sh >/dev/null
check "Y5 the exposure record is published while the input lock is held" '^held$' "$(cat "$C/exposure-barrier" 2>&1)"
check "Y5b and the lock is released afterwards" '^no$' "$(yn "$C/inbox/herdr-mutation.lock")"
# Y6: the audit gives GAP for unsupported or malformed tool records and flags first-option writes, wrappers with
# options, effect-capable awk programs and shell writes outside the allowed roots.
tgap() { { jq -nc '{type:"user",sessionId:"coord-session",message:{content:"start"}}'; printf '%s\n' "$1"; } > "$TC/y6.jsonl"; aud claude "$TC/y6.jsonl" coord-session "$TC/memory/"; }
out=$(tgap "$(tu y1 send_email '{"to":"someone"}')"); check "Y6 an unknown tool is a gap" '^GAP unsupported tool send_email.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(tgap "$(tu y2 Bash '{"command":["git","push"]}')"); check "Y6b a shell call without a command string is a gap" '^GAP malformed tool-call arguments: call y2.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
out=$(tgap "$(tu y3 mcp__mail__frobnicate '{}')"); check "Y6c an MCP call that is neither a known read nor a write is a gap" '^GAP unsupported tool mcp__mail__frobnicate.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
{ jq -nc '{type:"session_meta",payload:{id:"codex-session"}}'
  jq -nc '{type:"response_item",payload:{type:"function_call",name:"exec",call_id:"x1",arguments:"{\"code\":\"tools.exec_command({cmd:\\\"git push origin HEAD\\\"})\"}"}}'
} > "$TC/y6-codex.jsonl"
out=$(aud codex "$TC/y6-codex.jsonl" codex-session); check "Y6d a Codex nested execution tool is a gap" '^GAP unsupported tool exec.*rc=2' "$(printf '%s' "$out" | tr '\n' ' ')"
{
  jq -nc '{type:"user",sessionId:"coord-session",message:{content:"start"}}'
  sh_call f1 'gh api -f body=hello repos/o/r/issues/1/comments'
  sh_call f2 'curl --data payload https://example.invalid/hook'
  sh_call f3 'gh api -fbody=hello repos/o/r/issues/1/comments'
  sh_call f4 'env -i /tmp/do-push.sh'
  sh_call f5 "awk 'BEGIN { system(\"git push\") }'"
  sh_call f6 'printf changed > /project/app.ts'
  sh_call f7 'echo x >> /project/notes.md'
  sh_call f8 'cp /tmp/a /project/app.ts'
  sh_call r1 'gh api repos/o/r/pulls'
  sh_call r2 'curl -s https://example.invalid/status'
  sh_call r3 "awk '{print \$1}' /x/list.txt"
  sh_call r4 'ls /x 2>/dev/null'
  sh_call r5 "printf note > $TC/memory/n.md"
  tu r6 mcp__tracker__get_issue '{"id":"1"}'
} > "$TC/y6-flags.jsonl"
out=$(aud claude "$TC/y6-flags.jsonl" coord-session "$TC/memory/")
for n in 1 2 3 4 5 6 7 8; do check "Y6e a direct write or effect-capable run is flagged: f$n" "call=f$n " "$out"; done
nocheck "Y6f reads, a null redirect and an allowed-root write stay clean" 'call=r[0-9]' "$out"
check "Y6g eight flags of fourteen calls" 'AUDIT flagged=8 calls=14.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"

# EV: the event hints through the fake event stream of test/fakebin/nc. Queue scans decide correctness; an event
# only wakes a scan early. SWEEP=60 is longer than each run, so a delivery inside the run came from an event.
evcase() { # name: a sent envelope held back in staged/ until the stream script moves it, and a socket path
  new_case "$1"; send >/dev/null; mkdir "$C/staged"; mv "$(OB)/new/$ID.md" "$C/staged/"; : > "$C/sock"; : > "$C/events"
}
evcourier() { courier "HERDR_SOCKET_PATH=$C/sock SWEEP=60 $*" >/dev/null; }
RING='{"event":"pane.updated","data":{"pane":{"pane_id":"sender-pane","tokens":{"message_out":"m000000000001"}}}}'
evcase ev-control; printf '%s\n' '@sleep 1' '@stage' > "$C/events"
evcourier RUN_FOR=4
check "EV0 without a ring the staged send waits for the next sweep" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
check "EV0b the courier subscribed to pane updates, closes and exits" '"events.subscribe".*pane.updated.*pane.closed.*pane.exited' "$(cat "$C/subscriptions.log")"
# Each sweep checks the recipient's live identity once, so the count of those calls counts sweeps.
evcase ev-ring-once; printf '%s\n' '@sleep 1' '@stage' "$RING" > "$C/events"
evcourier RUN_FOR=4; once=$(grep -c '^agent get receiver-pane' "$C/calls.log")
evcase ev-ring; printf '%s\n' '@sleep 1' '@stage' "$RING" "$RING" > "$C/events"
evcourier RUN_FOR=4
check "EV1 a ring wakes a sweep that delivers the send" '^no yes$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
check "EV1b a repeated ring for the same message wakes no second sweep" "^$once 1\$" "$(grep -c '^agent get receiver-pane' "$C/calls.log") $(nfiles "$C/inbox/state" 'ring-*')"
evcase ev-split
printf '%s\n' '@sleep 1' '@stage' '@part {"event":"pane.updated","data":{"pane":{"pane_id":"sender-pane",' '@sleep 10' \
  '"tokens":{"message_out":"m000000000001"}}}}' > "$C/events-1"
evcourier RUN_FOR=5 EVENT_READ_MAX=1
check "EV2 a split frame times out, reconnects and the rescan delivers" '^[2-9] no yes$' "$(cat "$C/nc-count") $(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
check "EV2b the fragment is never joined into a ring" '^0$' "$(nfiles "$C/inbox/state" 'ring-*')"
evcase ev-malformed; printf '%s\n' '@sleep 1' '@stage' 'this is not a frame' > "$C/events-1"
evcourier RUN_FOR=4
check "EV3 a malformed frame reconnects and the rescan delivers" '^[2-9] yes 0$' "$(cat "$C/nc-count") $(yn "$(OB)/ready/$ID.md") $(nfiles "$C/inbox/state" 'ring-*')"
evcase ev-flood; printf '%s\n' '@sleep 1' '@stage' '@flood 300' "$RING" > "$C/events"
evcourier RUN_FOR=20
check "EV4 a flood of unrelated events is read without reconnecting and the ring still delivers" '^1 yes$' "$(cat "$C/nc-count") $(yn "$(OB)/ready/$ID.md")"
# EV5: a stop signal closes the transport: no event folder, no transport child, no courier lock.
evcase ev-term
env -i PATH="$CPATH" HOME="$HOME" HERDSMAN_INBOX="$C/inbox" FIXTURE="$C" HERDR_PANE_ID=courier-pane HERDR_SOCKET_PATH="$C/sock" \
  HERDSMAN_PREFLIGHT="$C/inbox/setup.json" WATCH_REQUIRED=0 RUN_FOR=30 SWEEP=60 /bin/bash "$MS/message-courier.sh" >/dev/null 2>&1 &
cpid=$!
i=0; while [ ! -f "$C/nc-pid-1" ] && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i+1)); done
sleep 0.5; kill -TERM "$cpid"; wait "$cpid" 2>/dev/null
npid=$(cat "$C/nc-pid-1" 2>/dev/null); i=0; while kill -0 "$npid" 2>/dev/null && [ "$i" -lt 30 ]; do sleep 0.1; i=$((i+1)); done
check "EV5 after a stop signal the event folder and the transport child are gone" '^0 gone no$' \
  "$(nfiles "$C/inbox/state" '.events.*') $(if kill -0 "$npid" 2>/dev/null; then echo alive; else echo gone; fi) $(yn "$C/inbox/courier.lock")"
# EV6: after a kill signal no cleanup runs: the transport child still ends on end of input, and the owner record
# and the courier lock stay for inspection, so a new courier refuses to start.
evcase ev-kill
env -i PATH="$CPATH" HOME="$HOME" HERDSMAN_INBOX="$C/inbox" FIXTURE="$C" HERDR_PANE_ID=courier-pane HERDR_SOCKET_PATH="$C/sock" \
  HERDSMAN_PREFLIGHT="$C/inbox/setup.json" WATCH_REQUIRED=0 RUN_FOR=30 SWEEP=60 /bin/bash "$MS/message-courier.sh" >/dev/null 2>&1 &
cpid=$!
i=0; while [ ! -f "$C/nc-pid-1" ] && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i+1)); done
sleep 0.5; kill -KILL "$cpid"; wait "$cpid" 2>/dev/null
npid=$(cat "$C/nc-pid-1" 2>/dev/null); i=0; while kill -0 "$npid" 2>/dev/null && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i+1)); done
check "EV6 after a kill signal the transport child ends on end of input" '^gone$' "$(if kill -0 "$npid" 2>/dev/null; then echo alive; else echo gone; fi)"
check "EV6b the owner record names the stopped courier" "^$cpid\$" "$(cat "$C"/inbox/state/.events.*/owner.json 2>/dev/null | jq -r .owner_pid)"
check "EV6c and a new courier refuses the kept lock" 'courier lock exists' "$(courier "HERDR_SOCKET_PATH=$C/sock")"
# EV7: an event for the waiter's own pane wakes its scan before the recheck interval.
evcase ev-wait; printf '%s\n' '@sleep 1' '@ready' '{"event":"pane.updated","data":{"pane":{"pane_id":"receiver-pane","tokens":{"messages":"1"}}}}' > "$C/events"
out=$(ENVX="HERDR_SOCKET_PATH=$C/sock DEADLINE=6 RECHECK=60" msg receiver-pane message-wait.sh "$O")
check "EV7 the waiter wakes on its own pane's event" "^MESSAGE $O" "$out"
evcase ev-wait-other; printf '%s\n' '@sleep 1' '@ready' '{"event":"pane.updated","data":{"pane":{"pane_id":"other-pane","tokens":{"messages":"1"}}}}' > "$C/events"
out=$(ENVX="HERDR_SOCKET_PATH=$C/sock DEADLINE=4 RECHECK=60" msg receiver-pane message-wait.sh "$O")
check "EV7b another pane's event does not wake it before the deadline" '^TICK' "$out"

# RC: refusal and recovery paths that change no state, alert repeats, display lines and liveness.
# RC1: a missing argument or an unknown action prints the usage, exits 2 and creates no state.
new_case rc-args noenroll
for sc in message-send.sh message-read.sh message-ack.sh message-wait.sh resume-goal-when-idle.sh message-hold.sh message-register.sh; do
  out=$(msg sender-pane "$sc"); st=$?
  check "RC1 no arguments: $sc prints its usage and exits 2" '^2 .+' "$st $out"
done
out=$(msg sender-pane message-register.sh bogus x); st=$?
check "RC1b an unknown registry action is refused" '^2 unknown registry action' "$st $out"
out=$(msg sender-pane message-register.sh propose 'Bad Name' adjutant); st=$?
check "RC1c an invalid name is refused" '^2 propose <name>' "$st $out"
check "RC1d and none of them created state" '^no no$' "$(yn "$C/inbox/state") $(yn "$C/inbox/peers")"
# RC2: a FIFO, a folder, a link or an oversized body is refused before any allocation; the leak gate refuses a
# folder and a link; a FIFO queue entry is held with an alert and the courier still exits 0.
new_case rc-inputs
mkfifo "$C/body.fifo"; mkdir "$C/body.dir"; ln -s "$C/body.txt" "$C/body.link"
head -c 65537 /dev/zero | tr '\0' a > "$C/body.big"
for b in fifo dir link big; do
  out=$(msg sender-pane message-send.sh "$A" "$O" "$C/body.$b" brief); st=$?
  check "RC2 a $b body is refused" '^2 body must be a bounded readable regular file' "$st $out"
done
check "RC2b no ID was allocated" '^0$' "$(nfiles "$C/inbox/ids" 'm*')"
leak "$C/body.dir" >/dev/null 2>&1; a=$?; leak "$C/body.link" >/dev/null 2>&1
check "RC2c the leak gate refuses a folder and a link" '^2 2$' "$a $?"
mkfifo "$(OB)/new/m000000000009.md"
courier >/dev/null; st=$?
check "RC2d a FIFO queue entry is held and alerts, and the courier exits 0" '^0 yes .*Suspicious queue entry' "$st $(if [ -p "$(OB)/new/m000000000009.md" ]; then echo yes; else echo no; fi) $(cat "$C/calls.log" | tr '\n' ' ')"
# RC3: the read summary counts pending new entries and quarantine.
new_case rc-summary
send >/dev/null; courier >/dev/null
send >/dev/null; jq_edit '.from="route-intruder"' "$(OB)/new/m000000000002.md"; courier >/dev/null
send >/dev/null
check "RC3 the read summary counts one pending and one quarantined entry" '"type":"summary","pending_new":1,"quarantine":1' "$(msg receiver-pane message-read.sh "$O")"
# RC4: a missing, null or made-up lifecycle keeps the message in new; a known one delivers it.
new_case rc-life
send >/dev/null
for e in 'del(.["sender-pane"].agent_status)' '.["sender-pane"].agent_status=null' '.["sender-pane"].agent_status="sleeping"'; do
  jq_edit "$e" "$C/agents.json"; courier >/dev/null
  check "RC4 lifecycle ${e#*.agent_status}: the message stays in new" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
done
jq_edit '.["sender-pane"].agent_status="idle"' "$C/agents.json"; courier >/dev/null
check "RC4b a known lifecycle delivers it" '^no yes$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
# RC5: status shows the registry approval flags.
new_case rc-flags
check "RC5 an approved registry: approved and nothing pending" 'registry valid=yes approved=yes pending=no' "$(msg receiver-pane message-status.sh "$O")"
jq_edit '.["route-adjutant"].identity.session.value="new-session"' "$C/inbox/peers"
check "RC5b a pending change: not approved and pending" 'registry valid=yes approved=no pending=yes' "$(msg receiver-pane message-status.sh "$O")"
# RC6: an ID replayed after its ack is held as a duplicate and never delivered again.
new_case rc-replay
send >/dev/null; courier >/dev/null
token=$(msg receiver-pane message-read.sh "$O" | jq -r 'select(.type=="claim")|.token')
printf 'intake recorded\n' > "$C/record.txt"; msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt" >/dev/null
cp "$C/inbox/ids/$ID/envelope" "$(OB)/new/$ID.md"; courier >/dev/null
check "RC6 a replay after ack is held as a duplicate" '^yes no .*Duplicate message held' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md") $(cat "$C/calls.log" | tr '\n' ' ')"
# RC7: a quarantine survives courier restarts with one terminal and one alert.
new_case rc-quarantine
send >/dev/null; jq_edit '.from="route-intruder"' "$(OB)/new/$ID.md"
courier >/dev/null; courier >/dev/null; courier >/dev/null
check "RC7 three courier runs keep one quarantine, one terminal and one alert" '^1 quarantined 1$' \
  "$(nfiles "$(OB)/quarantine" '*.md') $(jq -r .state "$C/inbox/ids/$ID/terminal.json") $(grep -c 'notification show Messages quarantined' "$C/calls.log")"
# RC8: a quarantine alert that could not be shown stays pending and is retried once after its backoff.
new_case rc-alert-retry
touch "$C/toast-disabled"
send >/dev/null; jq_edit '.from="route-intruder"' "$(OB)/new/$ID.md"; courier >/dev/null
check "RC8 a disabled toast leaves the quarantine alert pending" '^1 0$' "$(nfiles "$C/inbox/alerts" 'quarantine-*.pending') $(nfiles "$C/inbox/alerts" 'quarantine-*.sent')"
rm "$C/toast-disabled"; touch -t 202001010000 "$C/inbox/alerts/"quarantine-*.attempt; courier >/dev/null
check "RC8b the retry after the backoff shows it once" '^0 1$' "$(nfiles "$C/inbox/alerts" 'quarantine-*.pending') $(nfiles "$C/inbox/alerts" 'quarantine-*.sent')"
n=$(grep -c 'notification show Messages quarantined' "$C/calls.log"); courier >/dev/null
check "RC8c and never again" "^$n\$" "$(grep -c 'notification show Messages quarantined' "$C/calls.log")"
# RC9: a courier outage alerts, recovery resolves it, and a second outage with toasts off stays pending.
new_case rc-outage
rm "$C/inbox/state/heartbeat"; send >/dev/null
check "RC9 the first outage alerts" 'notification show Message courier unavailable' "$(cat "$C/calls.log")"
touch "$C/inbox/state/heartbeat"; send >/dev/null
check "RC9b recovery resolves the shown alert" '^no no$' "$(yn "$C/inbox/alerts/courier-down.sent") $(yn "$C/inbox/alerts/courier-down.attempt")"
rm "$C/inbox/state/heartbeat"; touch "$C/toast-disabled"; send >/dev/null
check "RC9c a second outage with toasts off is pending again, not sent" '^yes no$' "$(yn "$C/inbox/alerts/courier-down.pending") $(yn "$C/inbox/alerts/courier-down.sent")"
# RC10: every pinned blocked marker at a line start is recorded; the same text quoted inside a line is not.
new_case rc-markers
i=0; pairs=; targets='[]'
for phrase in 'Do you want to proceed' 'Do you trust' 'Allow reads outside' 'Allow once' 'Allow always' 'Password for' \
  'Device not configured' 'Permission required' 'approval required' 'Switch to gpt' '❯ 1. Yes, proceed' 'Yes, and don' \
  'escalated execution requires' 'Queued follow-up inputs' 'Type your answer' 'Usage limit' 'Selected model is at capacity' \
  "This content can't be shown"; do
  i=$((i+1)); pairs="$pairs mark-$i session-$i"; printf '%s\n' 'output line' "$phrase" > "$C/screen-mark-$i"
  targets=$(printf '%s' "$targets" | jq -c --arg p "mark-$i" --arg s "session-$i" '.+[{pane:$p,session:{source:"hook",agent:"claude",kind:"id",value:$s}}]')
done
printf '%s\n' 'he said Do you want to proceed with it' 'a quoted "Permission required" line' > "$C/screen-quoted"
targets=$(printf '%s' "$targets" | jq -c '.+[{pane:"quoted",session:{source:"hook",agent:"claude",kind:"id",value:"quoted-session"}}]')
# shellcheck disable=SC2086
set_agents sender-pane sender-session receiver-pane receiver-session $pairs quoted quoted-session
printf '%s\n' "$targets" > "$C/inbox/watch-targets.json"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "RC10 each of the eighteen pinned markers is recorded and the quoted text is not" '^18$' "$(nfiles "$C/inbox/state" 'blocked-*')"
# RC11: a blocked prompt that changes is a new episode: the old alert resolves and the new one alerts.
new_case rc-episode
jq -n '[{pane:"receiver-pane",session:{source:"hook",agent:"claude",kind:"id",value:"receiver-session"}}]' > "$C/inbox/watch-targets.json"
printf 'Permission required\n' > "$C/screen-receiver-pane"
ENVX="RUN_FOR=2 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
first=$(cat "$C/inbox/state/"blocked-*)
printf 'Allow once\n' > "$C/screen-receiver-pane"
ENVX="RUN_FOR=2 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "RC11 the changed prompt is a new episode with its own alert" '^yes changed [2-9]$' \
  "$(if [ -e "$C/inbox/alerts/blocked-$first.sent" ]; then echo kept; else echo yes; fi) $(if [ "$(cat "$C/inbox/state/"blocked-*)" != "$first" ]; then echo changed; else echo same; fi) $(grep -c 'notification show Agent notice or blocked state' "$C/calls.log")"
# RC12: after a verified working turn the resume re-arms, and a later pause gets one new resume.
new_case rc-rearm
jq_edit '.["task-pane"]=(.["sender-pane"]|.pane_id="task-pane"|.agent="codex"|.agent_session.agent="codex"|.agent_session.value="task-session")' "$C/agents.json"
jq -n '{"im-task":{role:"task-agent",identity:{pane:"task-pane",session:{source:"hook",agent:"codex",kind:"id",value:"task-session"}}}}' > "$C/inbox/resume-targets.json"
printf 'Goal paused (/goal resume)\n' > "$C/screen-task-pane"; touch "$C/allow-resume"; relay_boundary
msg sender-pane resume-goal-when-idle.sh im-task >/dev/null
check "RC12 a working turn after the resume clears the intent" '^NOOP verified working' "$(msg sender-pane resume-goal-when-idle.sh im-task)"
printf 'Goal paused (/goal resume)\n' > "$C/screen-task-pane"
check "RC12b the next pause gets one new resume" '^SENT /goal resume' "$(msg sender-pane resume-goal-when-idle.sh im-task)"
check "RC12c two resumes in two episodes" '^2$' "$(grep -c '^agent prompt task-pane /goal resume$' "$C/calls.log")"

# GX1: a go that expires while the gate waits for the input lock or the authority check is refused.
new_case gate-expiry
relay_boundary
printf 'reviewed text\n' > "$C/draft.txt"
APR="$C/inbox/approvals"; mkdir -p "$APR/interactions"
gojson x1 2
mkdir "$C/race-bin"
printf '%s\n' '#!/bin/bash' '[ "$1 $2" != "agent list" ] || sleep 3' "exec $SK/test/fakebin/herdr \"\$@\"" > "$C/race-bin/herdr"
chmod +x "$C/race-bin/herdr"
out=$(ENVX="PATH=$C/race-bin:$CPATH" consume x1)
check "GX1 a go that expires during the authority check is refused" 'REFUSED go expired.*rc=1' "$(printf '%s' "$out" | tr '\n' ' ')"
check "GX1b and nothing is consumed" '^no$' "$(yn "$APR/consumed/x1.json")"
# GX2: a journal alert clears once the journal drains, even when no new card appears.
new_case journal-clear
jq -n '[{pane:"receiver-pane",session:{source:"hook",agent:"claude",kind:"id",value:"receiver-session"}}]' > "$C/inbox/watch-targets.json"
relay_boundary
jkey=$(jq -c '.[0]' "$C/inbox/watch-targets.json" | jq -Sc . | cksum | awk '{print $1 "-" $2}')
touch "$C/inbox/alerts/card-journal-$jkey.sent" "$C/inbox/alerts/card-journal-$jkey.attempt"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "GX2 a drained journal resolves its alert without a new card" '^no$' "$(yn "$C/inbox/alerts/card-journal-$jkey.sent")"

# V3R: design v3 regressions.
# V3R1: status keeps the safe queue view under a corrupt registry and under a stale setup proof.
new_case v3r-status
send >/dev/null
cp "$C/inbox/peers.approved" "$C/peers.base"; printf 'not json\n' > "$C/inbox/peers.approved"
check "V3R1 a corrupt registry still shows the queued message" "^$O .*new=1" "$(msg receiver-pane message-status.sh | grep "^$O ")"
cp "$C/peers.base" "$C/inbox/peers.approved"; jq_edit '.policy_verified=false' "$C/inbox/setup.json"
out=$(msg receiver-pane message-status.sh)
check "V3R1b a stale setup proof still shows the queue and flags the proof" "setup-proof-stale.*$O .*new=1" "$(printf '%s' "$out" | tr '\n' ' ')"
# V3R2: ack refuses a claim folder that was replaced by a link, and publishes no intake.
new_case v3r-claim-link
send >/dev/null; courier >/dev/null
token=$(msg receiver-pane message-read.sh "$O" | jq -r 'select(.type=="claim")|.token')
mv "$(OB)/claim" "$C/claim-copy"; ln -s "$C/claim-copy" "$(OB)/claim"
printf 'intake recorded\n' > "$C/record.txt"
out=$(msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt"); st=$?
check "V3R2 a linked claim folder cannot ack" '^2 wrong active reader claim' "$st $out"
check "V3R2b and no intake is published" '^no no$' "$(yn "$(OB)/ack/$ID.md.json") $(yn "$C/inbox/ids/$ID/terminal.json")"
# V3R3: a literal working marker wins over a stale paused-goal footer; nothing is typed.
new_case v3r-markers
jq_edit '.["task-pane"]=(.["sender-pane"]|.pane_id="task-pane"|.agent="codex"|.agent_session.agent="codex"|.agent_session.value="task-session")' "$C/agents.json"
jq -n '{"im-task":{role:"task-agent",identity:{pane:"task-pane",session:{source:"hook",agent:"codex",kind:"id",value:"task-session"}}}}' > "$C/inbox/resume-targets.json"
touch "$C/allow-resume"; relay_boundary
for m in 'Working' 'Pursuing goal' 'Goal active' 'Incubating' 'thinking' 'press esc to interrupt'; do
  printf '%s\n' "$m" 'Goal paused (/goal resume)' > "$C/screen-task-pane"
  check "V3R3 the active marker '$m' wins over a stale paused footer" '^NOOP verified working' "$(msg sender-pane resume-goal-when-idle.sh im-task)"
done
check "V3R3b and no input was sent" '^0$' "$(grep -c '^agent prompt' "$C/calls.log")"
# V3R4: an unchanged read-back after a resume is kept, holds the next try and alerts on the next interval.
new_case v3r-readback
jq_edit '.["task-pane"]=(.["sender-pane"]|.pane_id="task-pane"|.agent="codex"|.agent_session.agent="codex"|.agent_session.value="task-session")' "$C/agents.json"
jq -n '{"im-task":{role:"task-agent",identity:{pane:"task-pane",session:{source:"hook",agent:"codex",kind:"id",value:"task-session"}}}}' > "$C/inbox/resume-targets.json"
jq -n '[{pane:"receiver-pane",session:{source:"hook",agent:"claude",kind:"id",value:"receiver-session"}}]' > "$C/inbox/watch-targets.json"
printf 'Goal paused (/goal resume)\n' > "$C/screen-task-pane"; touch "$C/allow-prompt"; relay_boundary
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "V3R4 an unchanged read-back is kept and sends no second resume" '^1 yes$' "$(grep -c '^agent prompt task-pane' "$C/calls.log") $(nfiles "$C/inbox/state" 'resume-*.readback' | sed 's/^1$/yes/')"
check "V3R4b and the held resume alerts on the next interval" 'notification show Goal resume held' "$(cat "$C/calls.log")"
# V3R5: a watch-target file replaced during a run holds automation and alerts; no card is relayed.
new_case v3r-targets
jq -n '[{pane:"receiver-pane",session:{source:"hook",agent:"claude",kind:"id",value:"receiver-session"}}]' > "$C/inbox/watch-targets.json"
printf '%s\n' '✦ Heads up · target finding' > "$C/screen-receiver-pane"; relay_boundary
mkdir "$C/race-bin"
printf '%s\n' '#!/bin/bash' \
  'if [ "$1 $2" = "pane read" ] && [ ! -e "$FIXTURE/replaced" ]; then touch "$FIXTURE/replaced"; printf "[]\n" > "$HERDSMAN_INBOX/watch-targets.json"; fi' \
  "exec $SK/test/fakebin/herdr \"\$@\"" > "$C/race-bin/herdr"
chmod +x "$C/race-bin/herdr"
ENVX="RUN_FOR=1 INTERVAL=1 PATH=$C/race-bin:$CPATH" msg courier-pane card-watch.sh >/dev/null
check "V3R5 a replaced watch-target file holds automation and relays nothing" '^yes 0 .*notification show Automatic coordination held' \
  "$(yn "$C/inbox/state/automation-held") $(nfiles "$C/inbox/$A/new" '*.md') $(cat "$C/calls.log" | tr '\n' ' ')"
# V3R6: more than four cards in one capture alert; at most four are journaled; an absent poll re-arms a headline.
new_case v3r-cards
jq -n '[{pane:"receiver-pane",session:{source:"hook",agent:"claude",kind:"id",value:"receiver-session"}}]' > "$C/inbox/watch-targets.json"
printf '%s\n' '✦ Heads up · c1' '✦ Heads up · c2' '✦ Heads up · c3' '✦ Heads up · c4' '✦ Heads up · c5' '✦ Heads up · c6' > "$C/screen-receiver-pane"
relay_boundary
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "V3R6 six cards in one capture journal four and alert" '^4 .*notification show Side-agent cards need inspection' "$(nfiles "$C/inbox/card-events" '*.json') $(cat "$C/calls.log" | tr '\n' ' ')"
printf '%s\n' '✦ Heads up · again' > "$C/screen-receiver-pane"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
printf 'idle prompt\n' > "$C/screen-receiver-pane"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
printf '%s\n' '✦ Heads up · again' > "$C/screen-receiver-pane"
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "V3R6b the same headline after an absent poll is a new episode" '^2$' "$(grep -l 'again' "$C/inbox/card-events/"*.json | wc -l | tr -d ' ')"
# V3R7: a new entry wakes the waiter as pending only once it is older than one courier sweep.
new_case v3r-pending
courier >/dev/null; send >/dev/null
out=$(ENVX="DEADLINE=2 RECHECK=1" msg receiver-pane message-wait.sh "$O")
check "V3R7 a fresh new entry does not wake the waiter" '^TICK' "$out"
touch -t 202001010000 "$(OB)/new/m000000000001.md"
out=$(ENVX="DEADLINE=2 RECHECK=1" msg receiver-pane message-wait.sh "$O")
check "V3R7b an entry older than one sweep wakes it as pending" 'HEALTH pending delivery' "$out"

# V3R8: status never prints an active claim's token, and an actually empty active claim can be released.
new_case v3r-secret
out=$(msg receiver-pane message-read.sh "$O"); token=$(printf '%s\n' "$out" | jq -r 'select(.type=="claim")|.token')
check "V3R8 an empty read still gets a claim" '^.+$' "$token"
nocheck "V3R8b status never prints the claim token" "$token" "$(msg receiver-pane message-status.sh "$O")"
check "V3R8c the empty active claim is released" '"type":"released"' "$(msg receiver-pane message-read.sh "$O" "$token" --release)"
# V3R9: a relay the send refuses for another reason alerts as a failed relay, not as a budget overflow.
new_case v3r-relay-failed
set_agents sender-pane sender-session receiver-pane receiver-session task-pane task-session
jq -n '[{pane:"task-pane",session:{source:"hook",agent:"claude",kind:"id",value:"task-session"}}]' > "$C/inbox/watch-targets.json"
printf '%s\n' '✦ Heads up · failed finding' > "$C/screen-task-pane"
relay_boundary
set_agents sender-pane sender-session task-pane task-session
ENVX="RUN_FOR=1 INTERVAL=1" msg courier-pane card-watch.sh >/dev/null
check "V3R9 a refused send alerts as a failed relay" 'notification show Side-agent relay failed' "$(cat "$C/calls.log")"
nocheck "V3R9b and not as a budget overflow" 'Source relay budget' "$(cat "$C/calls.log")"
# EV8: a flood of other panes' events does not keep the waiter from waking on its own pane's event.
evcase ev-wait-flood; printf '%s\n' '@sleep 1' '@ready' '@flood 300' '{"event":"pane.updated","data":{"pane":{"pane_id":"receiver-pane","tokens":{"messages":"1"}}}}' > "$C/events"
out=$(ENVX="HERDR_SOCKET_PATH=$C/sock DEADLINE=20 RECHECK=60" msg receiver-pane message-wait.sh "$O")
check "EV8 the waiter wakes on its own event after a flood" "^MESSAGE $O" "$out"

# V3R10: an ack retry repairs a missing terminal record.
new_case v3r-terminal
send >/dev/null; courier >/dev/null
token=$(msg receiver-pane message-read.sh "$O" | jq -r 'select(.type=="claim")|.token')
printf 'intake recorded\n' > "$C/record.txt"; msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt" >/dev/null
rm "$C/inbox/ids/$ID/terminal.json"
out=$(msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt"); st=$?
check "V3R10 an ack retry recreates the missing terminal" "^0 acknowledged $ID handled\$" "$st $out $(jq -r .state "$C/inbox/ids/$ID/terminal.json")"
# V3R11: the sender's status shows its outgoing notice by headers only.
new_case v3r-outgoing
send >/dev/null; jq_edit '.from_identity.session.value="forged"' "$(OB)/new/$ID.md"; courier >/dev/null
out=$(msg sender-pane message-status.sh "$A")
check "V3R11 the sender's status lists the quarantine notice" "\"id\":\"$ID\".*\"state\":\"quarantined\".*\"authority\":\"notice-only\"" "$out"
nocheck "V3R11b and never the body" 'full brief' "$out"
# V3R12: every mistyped envelope field is quarantined.
for e in '.version="1"' '.time="soon"' '.kind="bogus"' '.reply_to="m1"' '.body=7' '.to_identity.session.kind="name"' '.extra=1'; do
  new_case "v3r-field-$(printf '%s' "$e" | cksum | awk '{print $1}')"
  send >/dev/null; jq_edit "$e" "$(OB)/new/$ID.md"; courier >/dev/null
  check "V3R12 a mistyped envelope field is quarantined: $e" '^yes no$' "$(yn "$(OB)/quarantine/$ID.md") $(yn "$(OB)/ready/$ID.md")"
done
# V3R13: an unavailable sender check repeated over two courier runs keeps the message in new; recovery delivers it.
new_case v3r-stale-agent
send >/dev/null; touch "$C/get-failure"
courier >/dev/null; courier >/dev/null
check "V3R13 two unavailable checks keep the message in new" '^yes no$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"
rm "$C/get-failure"; courier >/dev/null
check "V3R13b the next available check delivers it" '^no yes$' "$(yn "$(OB)/new/$ID.md") $(yn "$(OB)/ready/$ID.md")"

# V3R14: an alert incident has one owner: a resolution while another process owns the alert waits, and completes
# after the owner releases it.
new_case v3r-alert-owner
printf '{"title":"Message courier unavailable","body":"inspect"}\n' > "$C/inbox/alerts/courier-down.pending"
mkdir "$C/inbox/alerts/courier-down.lock"
send >/dev/null
check "V3R14 a resolution under another owner leaves the incident pending" '^yes no$' "$(yn "$C/inbox/alerts/courier-down.pending") $(yn "$C/inbox/alerts/courier-down.resolved")"
check "V3R14b and does not take the owner's lock" '^yes$' "$(yn "$C/inbox/alerts/courier-down.lock")"
rmdir "$C/inbox/alerts/courier-down.lock"
msg sender-pane message-send.sh "$A" "$O" "$C/body.txt" brief >/dev/null
check "V3R14c after the release the resolution completes" '^no yes$' "$(yn "$C/inbox/alerts/courier-down.pending") $(yn "$C/inbox/alerts/courier-down.resolved")"

# V3R15: a ready entry or a lock that vanishes before its age is read is skipped without arithmetic on an empty time.
new_case v3r-vanish
send >/dev/null; courier >/dev/null
touch -t 202001010000 "$(OB)/ready/$ID.md"; mkdir "$C/inbox/herdr-mutation.lock"; touch -t 202001010000 "$C/inbox/herdr-mutation.lock"
printf '%s\n' "/ready/$ID.md" 'herdr-mutation.lock' > "$C/stat-fail"
out=$(courier "PATH=$FB:$CPATH ALERT_AFTER=60"); st=$?
check "V3R15 the courier skips the vanished entry and exits 0" '^0 no$' "$st $(if printf '%s' "$out" | grep -qiE 'integer|syntax error|arithmetic'; then echo arith; else echo no; fi)"
nocheck "V3R15b and raises no unread alert from a missing age" 'notification show Unread messages' "$(cat "$C/calls.log")"
out=$(ENVX="PATH=$FB:$CPATH" msg receiver-pane message-status.sh "$O"); st=$?
check "V3R15c status skips the lock with a missing age" '^0 no$' "$st $(if printf '%s' "$out" | grep -qiE 'integer|syntax error|arithmetic|mutation-lock-stale'; then echo bad; else echo no; fi)"
out=$(ENVX="PATH=$FB:$CPATH DEADLINE=2 RECHECK=1" msg receiver-pane message-wait.sh "$O"); st=$?
nocheck "V3R15d the waiter has no arithmetic failure" 'integer|syntax error|arithmetic' "$out"

# V3R16: after a reader stops with an incomplete claim, the human moves the claim aside; the next reader adopts the
# unacknowledged message in cur and does not revive the acknowledged one.
new_case v3r-adopt
send >/dev/null; courier >/dev/null
token=$(msg receiver-pane message-read.sh "$O" | jq -r 'select(.type=="claim")|.token')
printf 'intake recorded\n' > "$C/record.txt"; msg receiver-pane message-ack.sh "$O" "$token" "$ID" "$C/record.txt" >/dev/null
send >/dev/null; courier >/dev/null; msg receiver-pane message-read.sh "$O" "$token" >/dev/null
rm "$(OB)/claim/owner"
out=$(msg receiver-pane message-read.sh "$O" "$token"); st=$?
check "V3R16 a claim without its owner is held, even with the recorded token" '^2 .*reader claim held' "$st $out"
mv "$(OB)/claim" "$C/stopped-claim"
out=$(msg receiver-pane message-read.sh "$O"); token2=$(printf '%s\n' "$out" | jq -r 'select(.type=="claim")|.token')
check "V3R16b the next reader adopts the unacknowledged message in cur" '"id":"m000000000002".*"authority":"data-only"' "$out"
nocheck "V3R16c and does not revive the acknowledged one" "\"id\":\"$ID\"" "$out"
out=$(msg receiver-pane message-ack.sh "$O" "$token2" m000000000002 "$C/record.txt"); st=$?
check "V3R16d the adopted message is acknowledged with the new claim" '^0 handled$' "$st $(jq -r .state "$C/inbox/ids/m000000000002/terminal.json")"

# N: outside the input cases every fake herdr call was a read or a display call; the input cases sent only their inputs.
READS='^(agent get|agent list|pane list|pane report-metadata|notification show|pane read|nc -U)( |$)'
nocheck "N1 no other call reached the fake herdr" '.' \
  "$(for f in "$T"/*/calls.log; do [ "$f" = "$T/resume/calls.log" ] || [ "$f" = "$T/rc-rearm/calls.log" ] || [ "$f" = "$T/v3r-readback/calls.log" ] || [ "${f#"$T"/ckpt-}" != "$f" ] || grep -v -E "$READS" "$f"; done)"
nocheck "N1b the resume case sent only the exact resume input" '.' \
  "$(cat "$T/resume/calls.log" "$T/rc-rearm/calls.log" "$T/v3r-readback/calls.log" | grep -v -E "$READS" | grep -v -x -e 'agent prompt task-pane /goal resume' -e 'tab focus fixture-tab')"
nocheck "N1c the checkpointer cases prompted only the orchestrator pane" '.' \
  "$(for f in "$T"/ckpt-*/calls.log; do grep -v -E "$READS" "$f" | grep -v -E '^(agent wait [^ ]+ .*|agent prompt receiver-pane (/save-memory|/compact))$'; done)"

echo "RESULT pass=$pass fail=$fail (work dir $T)"
[ "$fail" -eq 0 ]
