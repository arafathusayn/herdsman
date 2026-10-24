#!/bin/bash
# Read-only decision; protected human setup and live inventory are prerequisites.
. "$(dirname "$0")/message-lib.sh"
[ $# = 0 ] || die 'message-authority.sh takes no arguments'
export ROOT_READ_ONLY=1; root_init
if boundary_ok; then
  printf 'RELAY current protected setup; bind each human go before use.\n'
else
  printf 'HUMAN relayed gos authorize nothing outward or irreversible. Human executes the action; automatic resume and card relay are held. Tell the user.\n'
  exit 3
fi
