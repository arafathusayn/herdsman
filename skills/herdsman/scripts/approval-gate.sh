#!/bin/bash
# Executor adapter for relayed gos. It performs no effect itself; the owning executor acts only after CONSUME.
#   consume <go-file> <action> <target> <revision> <draft-file|->   check every binding, then record consumption
#   complete <request-id> <outcome>                                   record the outcome after the effect
# Private state under <inbox>/approvals: interactions/<ref>.json (the adjutant's recorded ask-tool answers, each with
# asked_by: the approved adjutant's name, so an answer to the orchestrator's own ask is refused; this gate trusts them
# and does not prove them against the transcript),
# cancels/<request-id>.json, consumed/<request-id>.json and action.lock.
# Exit 0: CONSUME or completed; 1: refused, a new go is needed; 2: error or busy lock; 3: HUMAN boundary;
# 4: DUPLICATE, the recorded outcome stands; 5: RECONCILE, a consumption has no outcome: inspect external state first.
. "$(dirname "$0")/messages/message-lib.sh"
refuse() { printf 'REFUSED %s\n' "$*"; exit 1; }
ref_ok() { [[ "$1" =~ ^[A-Za-z0-9_-]{1,64}$ ]]; }
export ROOT_READ_ONLY=1; root_init
[ -z "$PROOF_STALE" ] || die 'sandbox policy or protected installation proof stale; repeat human setup'
AP="$ROOT/approvals" GATE_OWNED=
for d in "$AP" "$AP/interactions" "$AP/cancels" "$AP/consumed"; do
  [ ! -L "$d" ] || die "symlink approval state: $d"; mkdir -p "$d"
done
gate_cleanup() { if [ -n "$GATE_OWNED" ]; then rm -f "$AP/action.lock/pid"; rmdir "$AP/action.lock"; fi; cleanup; }
trap gate_cleanup EXIT
gate_lock() {
  mkdir "$AP/action.lock" 2>/dev/null || die 'action lock busy; retry later, never auto-remove'
  GATE_OWNED=1; printf '%s\n' "$$" > "$AP/action.lock/pid"
}
consumed_record() { # request action target revision draft state outcome
  jq -n --arg r "$1" --arg a "$2" --arg t "$3" --arg v "$4" --arg d "$5" --arg s "$6" --arg o "$7" --argjson at "$(now)" \
    '{request_id:$r,action:$a,target:$t,revision:$v,draft_hash:$d,state:$s,outcome:$o,at:$at}'
}

case "${1:-}" in
  consume)
    [ $# = 6 ] || die 'usage: approval-gate.sh consume <go-file> <action> <target> <revision> <draft-file|->'
    GO=$2 ACTION=$3 TARGET=$4 REVISION=$5 DRAFT=$6
    strict_json "$GO" || refuse 'go is not strict JSON'
    jq -e 'type=="object" and .version==1 and .type=="user_go" and
      (.expires_at|type=="number" and floor==.) and
      ([.question_id,.request_id,.action,.target,.revision,.draft_hash,.text,.human_interaction] |
        all(type=="string" and length>0))' "$GO" >/dev/null || refuse 'go fields missing or mistyped'
    field() { jq -r --arg k "$1" '.[$k]' "$GO"; }
    REQ=$(field request_id); REF=$(field human_interaction)
    ref_ok "$REQ" || refuse 'invalid request id'
    ref_ok "$REF" || refuse 'invalid interaction reference'
    [ "$(field action)" = "$ACTION" ] || refuse 'action differs from the go'
    [ "$(field target)" = "$TARGET" ] || refuse 'target differs from the go'
    [ "$(field revision)" = "$REVISION" ] || refuse 'revision differs from the go'
    if [ "$DRAFT" = - ]; then HASH=none; else regular "$DRAFT" || refuse 'draft unavailable'; HASH=$(digest "$DRAFT"); fi
    [ "$(field draft_hash)" = "$HASH" ] || refuse 'draft differs from the reviewed draft'
    [ "$(jq -r '.expires_at' "$GO")" -gt "$(now)" ] || refuse 'go expired'
    I="$AP/interactions/$REF.json"
    strict_json "$I" || refuse 'no trusted interaction record for this go'
    jq -e --slurpfile g "$GO" '.source=="ask-tool" and .question_id==$g[0].question_id and
      .request_id==$g[0].request_id and .revision==$g[0].revision' "$I" >/dev/null ||
      refuse 'trusted interaction does not match this go'
    # Correlation alone is not consent: the recorded answer must approve exactly this action, target and draft.
    jq -e '.approved==true' "$I" >/dev/null || refuse 'trusted interaction is not an affirmative answer'
    jq -e --slurpfile g "$GO" '.action==$g[0].action and .target==$g[0].target and .draft_hash==$g[0].draft_hash' "$I" >/dev/null ||
      refuse 'trusted interaction approves another action, target or draft'
    # An inbox means a route with an adjutant: every question goes through it, so the go is the approved adjutant's
    # ask-tool answer, and an answer to the orchestrator's own ask is no go.
    valid_registry "$ROOT/peers.approved" || refuse 'no approved pair to name the adjutant'
    ADJ=$(jq -r 'to_entries[] | select(.value.role=="adjutant") | .key' "$ROOT/peers.approved")
    [ -n "$ADJ" ] || refuse 'no approved adjutant'
    if ! jq -e --arg a "$ADJ" '.asked_by==$a' "$I" >/dev/null; then
      jq -e --slurpfile p "$ROOT/peers.approved" '.asked_by as $b | $p[0][$b|tostring].role=="orchestrator"' "$I" >/dev/null &&
        refuse "the orchestrator's own ask is no go in a route with an adjutant; ask again through the adjutant"
      refuse 'trusted interaction was not asked by the approved adjutant'
    fi
    gate_lock
    # Admission and exposure writers use the shared input lock. Check the boundary under it and record the
    # consumption before releasing it, so no admission falls between the check and the consumption.
    mutation_lock || die 'input lock busy; retry later, never auto-remove'
    # One check in this process, the lock owner: an agent that this observation saw is recorded from the same
    # observation, so a retry after that agent exits stays held until human setup.
    if ! boundary_ok; then
      record_exposure
      human_authority; exit 3
    fi
    # The go can expire while the gate waits for the lock or the authority check: check it again right here.
    [ "$(jq -r '.expires_at' "$GO")" -gt "$(now)" ] || refuse 'go expired'
    safe_leaf "$AP/cancels/$REQ.json"
    [ ! -e "$AP/cancels/$REQ.json" ] || refuse 'request cancelled'
    R="$AP/consumed/$REQ.json"
    if [ -e "$R" ] || [ -L "$R" ]; then
      strict_json "$R" || die 'consumed record unreadable; inspect it'
      jq -e --arg a "$ACTION" --arg t "$TARGET" --arg v "$REVISION" --arg d "$HASH" \
        '.action==$a and .target==$t and .revision==$v and .draft_hash==$d' "$R" >/dev/null ||
        refuse 'request already consumed with another binding; a changed draft or target needs a new go'
      if [ "$(jq -r .state "$R")" = 'done' ]; then printf 'DUPLICATE %s outcome=%s\n' "$REQ" "$(jq -r .outcome "$R")"; exit 4; fi
      printf 'RECONCILE %s consumed without an outcome: inspect external state before any retry\n' "$REQ"; exit 5
    fi
    atomic "$R" consumed_record "$REQ" "$ACTION" "$TARGET" "$REVISION" "$HASH" consuming '' || die 'cannot record consumption'
    mutation_unlock
    printf 'CONSUME %s: act once, then run complete\n' "$REQ" ;;
  complete)
    [ $# = 3 ] || die 'usage: approval-gate.sh complete <request-id> <outcome>'
    ref_ok "$2" || die 'invalid request id'; ref_ok "$3" || die 'invalid outcome'
    gate_lock
    R="$AP/consumed/$2.json"
    strict_json "$R" || die 'no consumption recorded for this request'
    [ "$(jq -r .state "$R")" = consuming ] || die 'outcome already recorded'
    rec=$(jq -c --arg o "$3" '.state="done" | .outcome=$o' "$R")
    write_text "$R" "$rec" || die 'cannot record outcome'
    printf 'COMPLETED %s outcome=%s\n' "$2" "$3" ;;
  *) die 'usage: approval-gate.sh consume <go-file> <action> <target> <revision> <draft-file|-> | complete <request-id> <outcome>' ;;
esac
