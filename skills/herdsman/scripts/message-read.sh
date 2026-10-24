#!/bin/bash
# <me> [claim-token] [--release]. Print manifests, never bodies.
. "$(dirname "$0")/message-lib.sh"
[ $# -ge 1 ] && [ $# -le 3 ] && name_ok "$1" || die 'read <me> [claim-token] [--release]'
ME=$1; TOKEN=${2:-}; ACTION=${3:-}
[ -z "$ACTION" ] || [ "$ACTION" = --release ] || die 'unknown read option'
root_init
lock; self_peer "$ME"; box_init "$ME"
box="$ROOT/$ME"
[ ! -L "$box/claim" ] || die 'claim symlink'
if [ -d "$box/claim" ]; then
  regular "$box/claim/token" && regular "$box/claim/owner" && [ -n "$TOKEN" ] && [ "$TOKEN" = "$(cat "$box/claim/token")" ] || die 'reader claim held; recover with the recorded token, do not start another reader'
else
  [ -z "$TOKEN" ] && [ -z "$ACTION" ] || die 'no such active claim'
  claim_stage=$(mktemp -d "$box/tmp/.claim.XXXXXX"); TOKEN=$(basename "$claim_stage")
  write_text "$claim_stage/token" "$TOKEN"
  write_text "$claim_stage/owner" "${HERDR_PANE_ID}:$$"
  mv -n "$claim_stage" "$box/claim"
  [ ! -d "$claim_stage" ] && regular "$box/claim/token" || die 'claim publication failed'
fi
if [ "$ACTION" = --release ]; then
  [ -z "$(unacked "$box")" ] || die 'acknowledge all delivered work before releasing claim'
  released=$(mktemp -d "$box/tmp/.released.XXXXXX"); rmdir "$released"
  mv -n "$box/claim" "$released"
  [ ! -d "$box/claim" ] || die 'claim release failed'
  rm "$released/token" "$released/owner"; rmdir "$released"
  printf '{"type":"released"}\n'; exit 0
fi
for f in "$box/ready/"*.md; do
  [ -e "$f" ] || continue
  envelope "$f" || die 'invalid ready envelope'
  [ ! -e "$ROOT/ids/$(basename "$f" .md)/terminal.json" ] || die 'terminal message replay held; reconcile archive'
  jq -e --argjson peer "$SELF" '.to_identity==$peer.identity' "$f" >/dev/null || die 'recipient generation mismatch'
  write_text "$ROOT/ids/$(basename "$f" .md)/claim" "$TOKEN"
  move_unique "$f" "$box/cur/$(basename "$f")"
done
jq -nc --arg token "$TOKEN" '{type:"claim",token:$token}'
for f in "$box/cur/"*.md; do
  [ -e "$f" ] || continue
  ack_valid "$box" "$(basename "$f" .md)" && continue
  [ ! -e "$ROOT/ids/$(basename "$f" .md)/terminal.json" ] || die 'terminal cur replay held; reconcile archive'
  envelope "$f" || die 'invalid cur envelope'
  jq -e --argjson peer "$SELF" '.to_identity==$peer.identity' "$f" >/dev/null || die 'recipient generation mismatch'
  jq -c --arg path "$f" --arg claim "$TOKEN" '{type:"manifest",id,from,to,kind,time,reply_to,path:$path,claim:$claim,authority:"data-only"}' "$f"
done
jq -nc --argjson pending "$(count_state "$box/new")" --argjson quarantine "$(count_state "$box/quarantine")" '{type:"summary",pending_new:$pending,quarantine:$quarantine}'
