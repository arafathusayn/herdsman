#!/bin/bash
# [me]. Metadata only, including orphan boxes and messages sent by me.
. "$(dirname "$0")/message-lib.sh"
[ $# -le 1 ] || die 'status [me]'
ME=${1:-}
[ -z "$ME" ] || name_ok "$ME" || die 'invalid name'
export ROOT_READ_ONLY=1; root_init
health_snapshot || :
for role in courier watch; do
  pid=none; age=unknown
  if regular "$ROOT/$role.lock/pid"; then pid=$(cat "$ROOT/$role.lock/pid"); fi
  stamp="$ROOT/state/heartbeat"; [ "$role" != watch ] || stamp="$ROOT/state/watch-heartbeat"
  [ ! -f "$stamp" ] || age=$(( $(now)-$(mtime "$stamp") ))
  printf '%s pid=%s heartbeat-age=%s; PID existence alone is not proof of health\n' "$role" "$pid" "$age"
done
valid=no; approved=no
if valid_registry "$ROOT/peers" && valid_registry "$ROOT/peers.approved"; then valid=yes; registry && approved=yes; fi
printf 'registry valid=%s approved=%s pending=%s\n' "$valid" "$approved" "$(if [ "$approved" = yes ]; then printf no; else printf yes; fi)"
jq -c 'to_entries[]|{name:.key,role:.value.role,pane:.value.identity.pane,session:.value.identity.session}' "$ROOT/peers.approved"
for box in "$ROOT/"*; do
  [ -d "$box/new" ] || continue
  [ ! -L "$box" ] || die 'mailbox symlink'
  orphan=yes; peer "$(basename "$box")" >/dev/null 2>&1 && orphan=no
  printf '%s orphan=%s new=%s ready=%s cur=%s quarantine=%s\n' "$(basename "$box")" "$orphan" "$(count_state "$box/new")" "$(count_state "$box/ready")" "$(count_state "$box/cur")" "$(count_state "$box/quarantine")"
  total=0
  while IFS= read -r pending; do [ -z "$pending" ] || total=$((total+1)); done < <(unacked "$box")
  printf 'unacknowledged=%s claim=%s\n' "$total" "$(if [ -f "$box/claim/token" ]; then printf held; elif [ -d "$box/claim" ]; then printf incomplete; else printf none; fi)"
  for state in new ready cur quarantine; do
    [ ! -L "$box/$state" ] || die 'state symlink'
    for f in "$box/$state/"*.md; do
      [ -e "$f" ] || [ -L "$f" ] || continue
      if envelope "$f"; then
        ack=false; ack_valid "$box" "$(basename "$f" .md)" && ack=true
        jq -c --arg state "$state" --arg me "$ME" --argjson ack "$ack" 'select($me=="" or .from==$me or .to==$me)|{id,from,to,kind,reply_to,state:$state,acknowledged:$ack}' "$f"
      else printf 'suspicious entry in %s/%s; no body displayed\n' "$(basename "$box")" "$state"; fi
    done
  done
  for f in "$box/tmp/"* "$box/tmp/".[!.]*; do [ ! -e "$f" ] || printf 'partial write: %s\n' "$f"; done
  for f in "$box/notices/"*.json; do [ ! -f "$f" ] || jq -c '{id,to,state,authority}' "$f"; done
done
for f in "$ROOT/alerts/"*.pending; do regular "$f" && cat "$f"; done
:
for f in "$ROOT/state/"diagnostic-*.log; do [ ! -f "$f" ] || printf 'diagnostic log: %s\n' "$f"; done
