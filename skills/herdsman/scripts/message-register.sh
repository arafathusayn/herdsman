#!/bin/bash
# propose <name> <role> | approve <checksum> | retire <name> <checksum>
# approve/retire are human-run registry administration, never a message action.
. "$(dirname "$0")/message-lib.sh"
[ $# -ge 1 ] || die 'usage: message-register.sh propose|approve|retire ...'
action=$1; shift
case "$action" in
  propose)
    [ $# = 2 ] && name_ok "$1" || die 'propose <name> adjutant|orchestrator'
    case "$2" in adjutant|orchestrator) ;; *) die 'invalid role' ;; esac
    ;;
  approve) [ $# = 1 ] && [[ "$1" =~ ^[0-9]+-[0-9]+$ ]] || die 'approve <inspected-checksum>' ;;
  retire) [ $# = 2 ] && name_ok "$1" && [[ "$2" =~ ^[0-9]+-[0-9]+$ ]] || die 'retire <name> <inspected-checksum>' ;;
  *) die 'unknown registry action' ;;
esac
root_init
lock
if [ ! -e "$ROOT/peers" ]; then write_text "$ROOT/peers" '{}'; fi
if [ ! -e "$ROOT/peers.approved" ]; then write_text "$ROOT/peers.approved" '{}'; fi
valid_registry "$ROOT/peers" || die 'invalid pending registry; inspect without committing it'
case "$action" in
  propose)
    [ $# = 2 ] || die 'propose <name> adjutant|orchestrator'
    name=$1; role=$2
    [ ! -e "$ROOT/enrolled/$name.retired" ] || die 'retired name; use a fresh generation'
    name_ok "$name" || die 'invalid name'
    case "$role" in adjutant|orchestrator) ;; *) die 'invalid role' ;; esac
    identity=$(live_identity "${HERDR_PANE_ID:?set own pane}") || die 'cannot enroll current session'
    candidate=$(jq -nc --arg r "$role" --argjson i "$identity" '{role:$r,identity:$i}')
    if [ -e "$ROOT/enrolled/$name.json" ]; then
      jq -e --argjson candidate "$candidate" '.==$candidate' "$ROOT/enrolled/$name.json" >/dev/null || die 'name is immutable; use a fresh generation name'
    fi
    jq -e --arg n "$name" --arg r "$role" --argjson i "$identity" '
      all(to_entries[]; .key==$n or (.value.role!=$r and .value.identity.pane!=$i.pane and .value.identity.session!=$i.session))' "$ROOT/peers" >/dev/null || die 'role/session already enrolled'
    write_text "$ROOT/enrolled/$name.json" "$candidate"
    atomic "$ROOT/peers" jq --arg n "$name" --argjson c "$candidate" '.[$n]=$c' "$ROOT/peers"
    box_init "$name"
    printf 'pending registry %s\n' "$(digest "$ROOT/peers")"
    ;;
  approve)
    [ $# = 1 ] && [ "$1" = "$(digest "$ROOT/peers")" ] || die 'approve requires the inspected pending checksum'
    jq -e 'length==2 and ([.[].role]|sort)==["adjutant","orchestrator"]' "$ROOT/peers" >/dev/null || die 'approve exactly one complete pair'
    atomic "$ROOT/peers.approved" cat "$ROOT/peers"
    printf 'registry approved; this grants no action authorization\n'
    ;;
  retire)
    [ $# = 2 ] && name_ok "$1" && [ "$2" = "$(digest "$ROOT/peers")" ] || die 'retire <name> <inspected-checksum>'
    peer "$1" >/dev/null || die 'unknown retirement name'
    write_text "$ROOT/enrolled/$1.retired" retired
    atomic "$ROOT/peers" jq --arg n "$1" 'del(.[$n])' "$ROOT/peers"
    printf 'retired; old mailbox remains for human reconciliation\n'
    ;;
  *) die 'unknown registry action' ;;
esac
