#!/bin/bash
# <me> <claim-token> <id> <processing-record-file>. Ack after durable intake.
. "$(dirname "$0")/message-lib.sh"
[ $# = 4 ] && name_ok "$1" && id_ok "$3" || die 'ack <me> <claim-token> <id> <processing-record-file>'
ME=$1; TOKEN=$2; ID=$3; RECORD=$4
bounded "$RECORD" && [ -s "$RECORD" ] || die 'nonempty processing record required'
root_init
self_peer "$ME"; lock; commit_identity; box_init "$ME"
box="$ROOT/$ME"
claim_valid "$box" && [ "$(cat "$box/claim/token")" = "$TOKEN" ] || die 'wrong active reader claim'
envelope "$box/cur/$ID.md" || die 'no claimed envelope'
jq -e --argjson peer "$SELF" '.to_identity==$peer.identity' "$box/cur/$ID.md" >/dev/null || die 'recipient generation mismatch'
if ! ack_valid "$box" "$ID"; then
atomic "$box/ack/$ID.md.json" jq -n --arg id "$ID" --arg claim "$TOKEN" --argjson time "$(now)" --rawfile record "$RECORD" \
  --arg sum "$(digest "$box/cur/$ID.md")" '{id:$id,claim:$claim,time:$time,processing_record:$record,envelope_checksum:$sum}'
ack_valid "$box" "$ID" || die 'ack validation failed; message remains unhandled'
fi
atomic "$ROOT/ids/$ID/terminal.json" jq -n --arg id "$ID" --arg sum "$(digest "$box/cur/$ID.md")" '{id:$id,state:"handled",envelope_checksum:$sum}'
printf 'acknowledged %s\n' "$ID"
