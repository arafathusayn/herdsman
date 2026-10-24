#!/bin/bash
# <from> <to> <body-file> <kind> [reply-to-id]. Standalone execution only.
. "$(dirname "$0")/message-lib.sh"
[ $# = 4 ] || [ $# = 5 ] || die 'send <from> <to> <body-file> <kind> [reply-to-id]'
FROM=$1; TO=$2; BODY=$3; KIND=$4; REPLY=${5:-}
name_ok "$FROM" && name_ok "$TO" || die 'invalid name'
bounded "$BODY" && [ "$(wc -c < "$BODY")" -le 65536 ] || die 'body must be a bounded readable regular file'
case "$KIND" in brief|observation|question|result|cancel|user_answer|receipt|side_agent_data) ;; *) die 'invalid kind' ;; esac
[ -z "$REPLY" ] || id_ok "$REPLY" || die 'invalid reply id'
root_init
self_peer "$FROM"
lock; commit_identity
recipient=$(peer "$TO") || die 'unknown peer'
if [ -n "$REPLY" ]; then regular "$ROOT/ids/$REPLY/envelope" || die 'unknown reply id'; fi
box_init "$TO"
backlog=0; service=0; source_count=0; source_identity=null
if [ "$KIND" = side_agent_data ]; then
  # Admission and exposure writers publish under the shared input lock. Hold it from this boundary check
  # through the enqueue, so an admission that wins the race prevents the relay. Release it before the doorbell.
  mutation_lock || die 'input lock busy; side-agent data not queued'
  boundary_ok || { record_exposure; die 'HUMAN boundary; side-agent data not queued'; }
  source_identity=$(jq -ce '.source|select(type=="object" and keys==["pane","session"])' "$BODY") || die 'side-agent source identity required'
fi
for f in "$ROOT/$TO/new/"*.md "$ROOT/$TO/ready/"*.md "$ROOT/$TO/cur/"*.md "$ROOT/$TO/quarantine/"*.md; do
  [ -e "$f" ] || continue
  ack_valid "$ROOT/$TO" "$(basename "$f" .md)" && continue
  if bounded "$f" && jq -e '.kind=="side_agent_data"' "$f" >/dev/null 2>&1; then
    service=$((service+1))
    if [ "$KIND" = side_agent_data ] && jq -e --argjson source "$source_identity" '.body|fromjson|.source==$source' "$f" >/dev/null 2>&1; then source_count=$((source_count+1)); fi
  else backlog=$((backlog+1)); fi
done
if [ "$KIND" = side_agent_data ]; then
  [ "$service" -lt 32 ] && [ "$source_count" -lt 8 ] || die 'side-agent budget full; human inspection required'
else [ "$backlog" -lt 1000 ] || die 'pending backlog full; inspect status'; fi
sequence=0
if [ -f "$ROOT/state/sequence" ]; then sequence=$(cat "$ROOT/state/sequence"); fi
[[ "$sequence" =~ ^(0|[1-9][0-9]{0,11})$ ]] && [ "$sequence" -lt 999999999999 ] || die 'invalid/exhausted sequence'
sequence=$((sequence+1)); write_text "$ROOT/state/sequence" "$sequence"
id=$(printf 'm%012d' "$sequence")
mkdir "$ROOT/ids/$id" || die 'duplicate allocation; reconcile sequence'
TMP=$(mktemp "$ROOT/$TO/tmp/.send.XXXXXX")
jq -n --arg id "$id" --arg from "$FROM" --arg to "$TO" --arg kind "$KIND" --arg reply "$REPLY" \
  --argjson 'fi' "$(printf '%s' "$SELF" | jq '.identity')" --argjson ti "$(printf '%s' "$recipient" | jq '.identity')" \
  --argjson time "$(now)" --rawfile body "$BODY" \
  '{version:1,id:$id,from:$from,to:$to,from_identity:$fi,to_identity:$ti,time:$time,kind:$kind,reply_to:$reply,body:$body}' > "$TMP"
bounded "$TMP" && jq -e 'type=="object" and (.body|type=="string")' "$TMP" >/dev/null || die 'encoded envelope too large or invalid; not queued'
cp "$TMP" "$ROOT/ids/$id/envelope"
move_unique "$TMP" "$ROOT/$TO/new/$id.md"; TMP=
[ -z "$MUTATION_OWNED" ] || mutation_unlock
unlock
if ! display_call pane report-metadata "$HERDR_PANE_ID" --source herdsman-message-out --token "message_out=$id" --ttl-ms 600000 >/dev/null; then
  printf 'WARNING ring failed; %s is queued, not delivered\n' "$id" >&2
fi
health || :
printf '%s queued; delivery requires receipt or status confirmation\n' "$id"
