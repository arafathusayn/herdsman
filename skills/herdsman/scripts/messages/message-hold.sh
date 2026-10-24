#!/bin/bash
# <me> hold <name> <reason> | <me> unhold <name>. A named hold stops automatic goal resume for that target.
# The orchestrator holds before a stop; the adjutant holds on a user pause. Writers use the shared input lock.
. "$(dirname "$0")/message-lib.sh"
case "${2:-}" in hold) [ $# = 4 ] ;; unhold) [ $# = 3 ] ;; *) false ;; esac && name_ok "$1" && name_ok "$3" ||
  die 'hold: <me> hold <name> <reason> | <me> unhold <name>'
ME=$1; ACTION=$2; NAME=$3; REASON=${4:-}
[ "${#REASON}" -le 300 ] || die 'hold reason longer than 300 characters'
root_init
self_peer "$ME"
mutation_lock || die 'input lock busy; retry with backoff, never auto-remove'
commit_identity
if [ "$ACTION" = hold ]; then
  atomic "$ROOT/state/hold-$NAME" jq -n --arg by "$ME" --arg reason "$REASON" --argjson time "$(now)" '{by:$by,reason:$reason,time:$time}'
  printf 'HOLD %s by %s\n' "$NAME" "$ME"
else
  safe_leaf "$ROOT/state/hold-$NAME"; rm -f "$ROOT/state/hold-$NAME"
  printf 'UNHOLD %s by %s\n' "$NAME" "$ME"
fi
mutation_unlock
