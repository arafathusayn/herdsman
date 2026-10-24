#!/bin/bash
# Shared runtime. Source from the installed script directory, never execute.
set -eu
set -o pipefail
umask 077
export LC_ALL=C
die() { printf '%s\n' "$*" >&2; exit 2; }
name_ok() { [[ "$1" =~ ^[a-z][a-z0-9_-]{0,31}$ ]]; }
id_ok() { [[ "$1" =~ ^m[0-9]{12}$ ]]; }
uint() { [[ "$2" =~ ^(0|[1-9][0-9]{0,7})$ ]] && [ "$2" -ge "$3" ] && [ "$2" -le "$4" ] || die "invalid $1: $2"; }
regular() { [ -f "$1" ] && [ ! -L "$1" ] && [ -r "$1" ]; }
bounded() { regular "$1" && [ "$(wc -c < "$1")" -le 131072 ]; }
strict_json() { bounded "$1" && jq --stream -se 'map(select(length==2)|.[0]) | length==(unique|length)' "$1" >/dev/null; }
safe_leaf() { [ ! -e "$1" ] && [ ! -L "$1" ] || regular "$1" || die "nonregular state leaf: $1"; }
safe_touch() { safe_leaf "$1"; touch "$1"; }
same_identity() { [ "$(printf '%s' "$1" | jq -Sc '{pane,session:{source:.session.source,agent:.session.agent,kind:.session.kind,value:.session.value}}')" = "$(printf '%s' "$2" | jq -Sc '{pane,session:{source:.session.source,agent:.session.agent,kind:.session.kind,value:.session.value}}')" ]; }
now() { date +%s; }
mtime() { stat -c %Y "$1" 2>/dev/null || stat -f %m "$1"; }
digest() { cksum < "$1" | awk '{print $1 "-" $2}'; }
root_init() {
  local supplied_health=${HEALTH_AFTER:-} stored_health
  HEALTH_AFTER=${HEALTH_AFTER:-180}; uint HEALTH_AFTER "$HEALTH_AFTER" 1 86400
  LOCK_WAIT=${LOCK_WAIT:-6}; uint LOCK_WAIT "$LOCK_WAIT" 1 60
  ROOT=${HERDSMAN_INBOX:?set HERDSMAN_INBOX}
  case "$ROOT" in /*) ;; *) die 'inbox must be absolute' ;; esac
  [ -d "$ROOT" ] && [ ! -L "$ROOT" ] || die 'pre-create a private inbox directory'
  [ "$(cd "$ROOT" && pwd -P)" = "$ROOT" ] || die 'inbox must be canonical'
  [ -O "$ROOT" ] && [ -w "$ROOT" ] || die 'inbox must be owned and writable'
  [ "$(stat -c %a "$ROOT" 2>/dev/null || stat -f %Lp "$ROOT")" = 700 ] || die 'inbox mode must be 700'
  for tool in herdr jq nc; do command -v "$tool" >/dev/null || die "missing $tool"; done
  jq -en '"idle"|IN("idle","working")' >/dev/null && printf '{}' | jq --stream -se 'length>0' >/dev/null || die 'jq needs streaming and IN support'
  for d in state alerts ids enrolled card-events courier.lock watch.lock channel.lock herdr-mutation.lock; do
    if [ -e "$ROOT/$d" ] || [ -L "$ROOT/$d" ]; then
      [ ! -L "$ROOT/$d" ] && [ -d "$ROOT/$d" ] && [ -z "$(find "$ROOT/$d" -type l -print)" ] || die 'symlink or invalid administrative state'
    fi
  done
  for d in peers peers.approved courier.log courier.log.previous; do safe_leaf "$ROOT/$d"; done
  if [ -e "$ROOT/state/timing.json" ]; then
    strict_json "$ROOT/state/timing.json" || die 'invalid shared timing configuration'
    stored_health=$(jq -er '.health' "$ROOT/state/timing.json") || die 'missing shared health threshold'
    uint shared-health "$stored_health" 1 86400
    [ -z "$supplied_health" ] || [ "$supplied_health" = "$stored_health" ] || die 'health threshold conflicts with shared timing'
    HEALTH_AFTER=$stored_health
  fi
  # Operator creates this outside the inbox only after real exclusion/tracked-file
  # checks. No Git dependency or Git call is hidden in the channel runtime.
  local proof rule expected actual
  proof=${HERDSMAN_PREFLIGHT:?set the externally prepared inbox preflight proof}
  strict_json "$proof" || die 'missing strict preflight proof'
  jq -e --arg root "$ROOT" '.inbox==$root and .excluded==true and .tracked_files==0 and
    (.rules|type=="array" and length>0) and all(.rules[];
    (.path|type=="string" and startswith("/")) and (.checksum|type=="string"))' "$proof" >/dev/null || die 'inbox exclusion not verified'
  while IFS= read -r rule; do
    expected=$(printf '%s' "$rule" | jq -r '.checksum')
    rule=$(printf '%s' "$rule" | jq -r '.path')
    regular "$rule" || die 'ignore rule file unavailable'
    actual=$(digest "$rule")
    [ "$actual" = "$expected" ] || die 'ignore rules changed; repeat external preflight'
  done < <(jq -c '.rules[]' "$proof")
  for d in state ids enrolled alerts; do
    [ ! -L "$ROOT/$d" ] || die "symlink: $d"
    if [ "${ROOT_READ_ONLY:-0}" = 1 ]; then [ -d "$ROOT/$d" ] || die 'channel not initialized'; else mkdir -p "$ROOT/$d"; fi
  done
  LOCK_OWNED=; TMP=; COURIER_OWNED=; WATCH_OWNED=; MUTATION_OWNED=
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  trap 'exit 129' HUP
}
cleanup() {
  trap - EXIT
  if declare -F events_close >/dev/null; then events_close; fi
  [ -z "${TMP:-}" ] || rm -f "$TMP"
  if [ -n "${MUTATION_OWNED:-}" ]; then rm -f "$ROOT/herdr-mutation.lock/pid"; rmdir "$ROOT/herdr-mutation.lock"; fi
  if [ -n "${LOCK_OWNED:-}" ]; then rm -f "$ROOT/channel.lock/pid"; rmdir "$ROOT/channel.lock"; fi
  if [ -n "${COURIER_OWNED:-}" ]; then rm -f "$ROOT/courier.lock/pid"; rmdir "$ROOT/courier.lock"; fi
  if [ -n "${WATCH_OWNED:-}" ]; then rm -f "$ROOT/watch.lock/pid"; rmdir "$ROOT/watch.lock"; fi
}
try_lock() {
  local attempts=0
  while ! mkdir "$ROOT/channel.lock" 2>/dev/null; do
    attempts=$((attempts + 1)); [ "$attempts" -lt "$LOCK_WAIT" ] || return 1
    sleep 1
  done
  LOCK_OWNED=1
  printf '%s\n' "$$" > "$ROOT/channel.lock/pid"
}
lock() { try_lock || die 'channel lock busy; retry with backoff, never auto-remove'; }
unlock() { rm "$ROOT/channel.lock/pid"; rmdir "$ROOT/channel.lock"; LOCK_OWNED=; }
display_call() {
  # Narrow exception: serialized display metadata and notifications only.
  local tries=0 status=0
  case "$1 $2" in 'pane report-metadata'|'notification show') ;; *) die 'display automation cannot type or change layout' ;; esac
  while ! mkdir "$ROOT/herdr-mutation.lock" 2>/dev/null; do
    tries=$((tries+1)); [ "$tries" -lt 6 ] || return 1; sleep 1
  done
  MUTATION_OWNED=1; printf '%s\n' "$$" > "$ROOT/herdr-mutation.lock/pid" || die 'cannot record display owner'
  herdr "$@" </dev/null || status=$?
  rm "$ROOT/herdr-mutation.lock/pid" && rmdir "$ROOT/herdr-mutation.lock" || die 'cannot release display owner'
  MUTATION_OWNED=
  return "$status"
}
atomic() {
  # Run the complete producer before committing. Never pipe into this function.
  local target=$1; shift
  safe_leaf "$target"
  TMP=$(mktemp "$ROOT/state/.write.XXXXXX")
  "$@" > "$TMP" || { rm -f "$TMP"; TMP=; return 1; }
  [ -s "$TMP" ] || { rm -f "$TMP"; TMP=; return 1; }
  case "$target" in *.json|*/peers|*/peers.approved) strict_json "$TMP" || { rm -f "$TMP"; TMP=; return 1; } ;; esac
  mv -f "$TMP" "$target" && [ ! -e "$TMP" ] && regular "$target" || return 1
  TMP=
}
write_text() { atomic "$1" printf '%s\n' "$2"; }
move_unique() {
  [ ! -e "$2" ] && [ ! -L "$2" ] || die "destination exists: $2"
  mv -n "$1" "$2"
  [ ! -e "$1" ] && regular "$2" || die "move failed: $1"
}
box_init() {
  name_ok "$1" || die 'invalid message name'
  local d
  [ ! -L "$ROOT/$1" ] || die 'mailbox symlink'
  for d in tmp new ready cur quarantine ack notices; do
    [ ! -L "$ROOT/$1/$d" ] || die 'mailbox state symlink'
    mkdir -p "$ROOT/$1/$d"
  done
}
valid_registry() {
  strict_json "$1" || return 1
  jq -e 'type=="object" and length<=2 and all(to_entries[];
    (.key|test("^[a-z][a-z0-9_-]{0,31}$")) and
    (.value|keys==["identity","role"]) and
    (.value.role=="adjutant" or .value.role=="orchestrator") and
    (.value.identity|keys==["pane","session"]) and
    (.value.identity.pane|type=="string" and length>0) and
    (.value.identity.session|keys==["agent","kind","source","value"]) and
    (.value.identity.session.kind=="id") and
    (.value.identity.session.value|type=="string" and test("^[A-Za-z0-9_-]+$")) and
    (.value.identity.session.source|type=="string" and length>0) and
    (.value.identity.session.agent|type=="string" and length>0)) and
    (([.[].role]|unique|length)==length) and
    (([.[].identity.pane]|unique|length)==length) and
    (([.[].identity.session]|unique|length)==length)' "$1" >/dev/null
}
registry() {
  valid_registry "$ROOT/peers" && valid_registry "$ROOT/peers.approved" || return 1
  cmp -s "$ROOT/peers" "$ROOT/peers.approved"
}
peer() { jq -ce --arg n "$1" '.[$n] // empty' "$ROOT/peers.approved"; }
live_identity() {
  # 0: usable observation; 3: pane conclusively gone; 4: unavailable/indeterminate.
  local raw
  if ! raw=$(herdr agent get "$1" </dev/null); then
    if printf '%s' "$raw" | jq -e '.error.code=="pane_not_found" or .error.code=="agent_not_found"' >/dev/null 2>&1; then return 3; fi
    printf 'agent get unavailable: %.300s\n' "$raw" >&2; return 4
  fi
  printf '%s' "$raw" | jq -ce --arg p "$1" '.result.agent | select(
    .pane_id==$p and (.agent|type=="string" and length>0) and
    .interactive_ready==true and .launch_pending==false and (.agent_status|IN("idle","working","blocked","done")) and
    (.terminal_id|type=="string") and (.workspace_id|type=="string") and (.tab_id|type=="string") and
    (.focused|type=="boolean") and (.revision|type=="number" and floor==. and .>=0) and
    .agent_session.kind=="id" and
    (.agent_session.value|type=="string" and test("^[A-Za-z0-9_-]+$")) and
    (.agent_session.source|type=="string" and length>0) and
    (.agent_session.agent|type=="string" and length>0)) |
    {pane:.pane_id,session:.agent_session}' | jq -Sc . || return 4
}
self_peer() {
  registry || die 'registry change awaiting human approval'
  SELF=$(peer "$1") || die 'unknown peer'
  local observed expected
  expected=$(printf '%s' "$SELF" | jq -c '.identity')
  observed=$(live_identity "${HERDR_PANE_ID:?set own pane}") || die 'own identity unavailable'
  same_identity "$observed" "$expected" || die 'own pane/session changed; enroll a new generation'
}
log_line() {
  local line="$1"
  local log="$ROOT/state/diagnostic-$$.log"
  safe_leaf "$log"; safe_leaf "$log.previous"
  # Rotate only diagnostics; never queue or audit records.
  if [ -f "$log" ] && [ "$(wc -c < "$log")" -gt 1048576 ]; then
    mv -f "$log" "$log.previous"
  fi
  printf '%s %s\n' "$(now)" "$line" >> "$log" || die 'cannot record courier diagnostics'
}
notify() {
  local key="$1" title="$2" body="$3"
  safe_leaf "$ROOT/alerts/$key.sent"; safe_leaf "$ROOT/alerts/$key.pending"
  [ ! -f "$ROOT/alerts/$key.sent" ] || return 0
  atomic "$ROOT/alerts/$key.pending" jq -n --arg title "$title" --arg body "$body" '{title:$title,body:$body}' || return 1
  alert_attempt "$key"
}
alert_attempt() {
  local key=$1 stamp reply age title body
  stamp="$ROOT/alerts/$key.attempt"; safe_leaf "$stamp"
  strict_json "$ROOT/alerts/$key.pending" || return 1
  if [ -f "$stamp" ]; then age=$(( $(now) - $(mtime "$stamp") )); [ "$age" -ge 5 ] || return 0; fi
  safe_touch "$stamp"
  title=$(jq -er '.title|select(type=="string")' "$ROOT/alerts/$key.pending") || return 1
  body=$(jq -er '.body|select(type=="string")' "$ROOT/alerts/$key.pending") || return 1
  if reply=$(display_call notification show "$title" --body "$body" --sound request) &&
     printf '%s' "$reply" | jq -e '.result.shown==true' >/dev/null 2>&1; then
    safe_touch "$ROOT/alerts/$key.sent"
    rm "$ROOT/alerts/$key.pending"
  else log_line "ALERT PENDING $key"; fi
}
retry_alerts() {
  local f
  for f in "$ROOT/alerts/"*.pending; do
    [ -e "$f" ] || continue
    ( alert_attempt "$(basename "$f" .pending)" ) || printf 'HEALTH alert retry failed\n' >&2
  done
}
resolve_alert() {
  local key=$1 copy
  # Preserve a failed notification as a historical retry, independent of health.
  if regular "$ROOT/alerts/$key.pending"; then
    copy=$(mktemp "$ROOT/alerts/resolved-$key.XXXXXX")
    rm "$copy"
    move_unique "$ROOT/alerts/$key.pending" "$copy.pending"
    if regular "$ROOT/alerts/$key.attempt"; then mv "$ROOT/alerts/$key.attempt" "$copy.attempt"; fi
  fi
  rm -f "$ROOT/alerts/$key.sent" "$ROOT/alerts/$key.attempt"
}
envelope() {
  bounded "$1" || return 1
  jq --stream -se 'map(select(length==2)|.[0]) | length==(unique|length)' "$1" >/dev/null || return 1
  jq -e --arg file "$(basename "$1" .md)" '
    keys==["body","from","from_identity","id","kind","reply_to","time","to","to_identity","version"] and
    .version==1 and .id==$file and (.id|test("^m[0-9]{12}$")) and
    (.from|test("^[a-z][a-z0-9_-]{0,31}$")) and (.to|test("^[a-z][a-z0-9_-]{0,31}$")) and
    (.kind|IN("brief","observation","question","result","cancel","user_answer","receipt")) and
    (.reply_to=="" or (.reply_to|test("^m[0-9]{12}$"))) and
    (.time|type=="number" and floor==. and .>=0) and
    (.body|type=="string" and length<=65536) and
    all(.from_identity,.to_identity; type=="object" and keys==["pane","session"] and
      (.pane|type=="string" and length>0) and (.session|type=="object") and
      (.session|keys==["agent","kind","source","value"]) and .session.kind=="id" and
      (.session.value|type=="string" and test("^[A-Za-z0-9_-]+$")) and
      (.session.agent|type=="string" and length>0) and (.session.source|type=="string" and length>0))' "$1" >/dev/null
}
count_state() {
  local f n=0
  for f in "$1/"*.md; do [ -e "$f" ] || [ -L "$f" ] || continue; n=$((n+1)); done
  printf '%s\n' "$n"
}
unacked() {
  local f
  for f in "$1/ready/"*.md "$1/cur/"*.md; do
    [ -e "$f" ] || continue
    ack_valid "$1" "$(basename "$f" .md)" || printf '%s\n' "$f"
  done
}
ack_valid() {
  local file="$1/ack/$2.md.json"
  strict_json "$file" || return 1
  jq -e --arg id "$2" '.id==$id and (.claim|type=="string" and length>0) and
    (.time|type=="number") and (.processing_record|type=="string" and length>0) and
    (.envelope_checksum|type=="string")' "$file" >/dev/null || return 1
  bounded "$1/cur/$2.md" || return 1
  [ "$(jq -r .envelope_checksum "$file")" = "$(digest "$1/cur/$2.md")" ] || return 1
  regular "$ROOT/ids/$2/claim" && [ "$(jq -r .claim "$file")" = "$(cat "$ROOT/ids/$2/claim")" ]
}
health_snapshot() {
  local role stamp age bad=0
  for role in courier watch; do
    # Every installed setup includes the watcher. Fixture-only opt-out is explicit.
    [ "$role" != watch ] || [ "${WATCH_REQUIRED:-1}" = 1 ] || continue
    stamp="$ROOT/state/heartbeat"; [ "$role" != watch ] || stamp="$ROOT/state/watch-heartbeat"
    age=999999; regular "$stamp" && age=$(( $(now)-$(mtime "$stamp") ))
    if [ "$age" -gt "$HEALTH_AFTER" ]; then printf '%s-down\n' "$role"; bad=1; fi
  done
  if [ -d "$ROOT/herdr-mutation.lock" ] && [ $(( $(now)-$(mtime "$ROOT/herdr-mutation.lock") )) -gt "$HEALTH_AFTER" ]; then printf 'mutation-lock-stale\n'; bad=1; fi
  return "$bad"
}
health() {
  local flags role
  flags=$(health_snapshot) || :
  for role in courier watch; do
    if printf '%s\n' "$flags" | grep -qx "$role-down"; then
      notify "$role-down" "Message $role unavailable" 'Inspect heartbeat and queues; restart only under the recorded grant.'
    else resolve_alert "$role-down"; fi
  done
  [ -z "$flags" ] || { printf 'HEALTH %s\n' "$flags" >&2; return 1; }
}
