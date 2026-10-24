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
# Only a registered peer or an approved resume target can be held; a misspelled name would guard nothing.
peer "$NAME" >/dev/null || { strict_json "$ROOT/resume-targets.json" && jq -e --arg n "$NAME" 'has($n)' "$ROOT/resume-targets.json" >/dev/null; } ||
  die "unknown hold target: $NAME"
mutation_lock || die 'input lock busy; retry with backoff, never auto-remove'
commit_identity
# One record per target, one entry per owner. An owner sets or clears only its own entry; another owner's hold
# stays, and the resume helper holds while any entry stays. A record in another format is never changed.
file="$ROOT/state/hold-$NAME"; safe_leaf "$file"; current='{"holds":{}}'
if [ -e "$file" ]; then
  strict_json "$file" 2>/dev/null && jq -e 'keys==["holds"] and (.holds|type=="object") and
    all(.holds[]; keys==["reason","time"] and (.reason|type=="string") and (.time|type=="number"))' "$file" >/dev/null ||
    die 'hold record is not in the owner format; human inspection required'
  current=$(jq -c . "$file")
fi
if [ "$ACTION" = hold ]; then
  atomic "$file" jq -n --argjson cur "$current" --arg by "$ME" --arg reason "$REASON" --argjson time "$(now)" '$cur|.holds[$by]={reason:$reason,time:$time}'
  printf 'HOLD %s by %s\n' "$NAME" "$ME"
else
  printf '%s' "$current" | jq -e --arg by "$ME" '.holds|has($by)' >/dev/null || die "no hold by $ME for $NAME; another owner's hold stays"
  current=$(printf '%s' "$current" | jq -c --arg by "$ME" 'del(.holds[$by])')
  if printf '%s' "$current" | jq -e '.holds=={}' >/dev/null; then rm -f "$file"
  else atomic "$file" jq -n --argjson cur "$current" '$cur'; fi
  owners=$(printf '%s' "$current" | jq -r '.holds|keys|join(" ")')
  if [ -n "$owners" ]; then printf 'UNHOLD %s by %s; still held by: %s\n' "$NAME" "$ME" "$owners"
  else printf 'UNHOLD %s by %s; no hold remains\n' "$NAME" "$ME"; fi
fi
mutation_unlock
