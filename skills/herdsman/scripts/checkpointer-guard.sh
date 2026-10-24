#!/bin/bash
# <orchestrator-name> <pane>. Read-only check that checkpointer.sh runs right before each input in adjutant mode.
# Exit 0 only when the approved registry names this pane as the orchestrator and its live session still matches.
. "$(dirname "$0")/message-lib.sh"
[ $# = 2 ] || die 'usage: checkpointer-guard.sh <orchestrator-name> <pane>'
name_ok "$1" || die 'invalid orchestrator name'
export ROOT_READ_ONLY=1; root_init
registry || die 'registry change awaiting human approval; no input'
entry=$(peer "$1") || die 'unknown orchestrator name; no input'
[ "$(printf '%s' "$entry" | jq -r .role)" = orchestrator ] || die 'target is not the approved orchestrator; never type into the adjutant'
expected=$(printf '%s' "$entry" | jq -c .identity)
[ "$(printf '%s' "$expected" | jq -r .pane)" = "$2" ] || die 'pane is not the approved orchestrator pane; no input'
rc=0; observed=$(live_identity "$2") || rc=$?
[ "$rc" != 3 ] || die 'orchestrator pane closed; no input'
[ "$rc" = 0 ] || die 'orchestrator identity unavailable; no input'
same_identity "$observed" "$expected" || die 'orchestrator session changed; enroll a new generation before checkpoints'
printf 'OK %s %s\n' "$1" "$2"
