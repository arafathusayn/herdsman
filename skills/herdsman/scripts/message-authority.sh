#!/bin/bash
# Read-only boundary decision. This is not an approval consumer or executor.
. "$(dirname "$0")/message-lib.sh"
[ $# = 0 ] || die 'message-authority.sh takes no arguments'
export ROOT_READ_ONLY=1; root_init
if jq -e '.boundary.version==1 and .boundary.inventory_current==true and
  .boundary.private_home==true and (.boundary.lower_trust|type=="array") and
  all(.boundary.lower_trust[];
    (.name|type=="string" and length>0) and (.launch_hash|type=="string" and length>0) and
    .sandbox==true and .inbox_denied==true and .socket_denied==true and
    (.harness=="codex" or .harness=="claude"))' "$HERDSMAN_PREFLIGHT" >/dev/null; then
  printf 'RELAY scoped human gos may authorize recorded steps; validate request, target, revision and current inventory before consuming.\n'
else
  printf 'HUMAN relayed gos authorize nothing outward or irreversible: an unsandboxed or unverified lower-trust agent may run. The human must execute it or type the exact go in the orchestrator pane. Tell the user.\n'
  exit 3
fi
