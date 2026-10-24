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
# The terminal is permanent: create it when absent, accept the matching one, and hold any other bytes unchanged.
terminal="$ROOT/ids/$ID/terminal.json"; sum=$(digest "$box/cur/$ID.md"); terminal_done=
if [ -e "$terminal" ] || [ -L "$terminal" ]; then
  strict_json "$terminal" && jq -e --arg id "$ID" --arg sum "$sum" '.=={id:$id,state:"handled",envelope_checksum:$sum}' "$terminal" >/dev/null ||
    die 'conflicting or invalid terminal disposition held for inspection; not overwritten'
  terminal_done=1
fi
if ! ack_valid "$box" "$ID"; then
atomic "$box/ack/$ID.md.json" jq -n --arg id "$ID" --arg claim "$TOKEN" --argjson time "$(now)" --rawfile record "$RECORD" \
  --arg sum "$(digest "$box/cur/$ID.md")" '{id:$id,claim:$claim,time:$time,processing_record:$record,envelope_checksum:$sum}'
ack_valid "$box" "$ID" || die 'ack validation failed; message remains unhandled'
fi
[ -n "$terminal_done" ] || atomic "$terminal" jq -n --arg id "$ID" --arg sum "$sum" '{id:$id,state:"handled",envelope_checksum:$sum}'
printf 'acknowledged %s\n' "$ID"
