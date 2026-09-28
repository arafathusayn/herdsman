#!/bin/bash
# herdsman one-shot waiter (bash 3). THE wake mechanism of the loop.
# Run it with the Bash tool as run_in_background: true, timeout: 600000. It exits on the FIRST event pass
# (printing every event of that pass) or after DEADLINE seconds with a TICK line; a finished background
# command re-invokes the orchestrator, which handles the events and re-arms the same command.
# Monitor tasks do not wake the session between turns; this does.
# Events: REPORT <task> <path> | REPORT-UPDATED <task> <path> | REVIEW <path> | BLOCKED <agent> <text>
#         | GOAL-DONE-NO-REPORT <agent> | STALL <agent> (screen unchanged for 3 polls while working)
#         | OVERDUE <agent> (past its due time and its report not written since the due time was set) | TICK
# Configure with HERDSMAN_REPORTS, HERDSMAN_STATE, HERDSMAN_WORKERS, HERDSMAN_PANE_<name> (dashes as underscores),
# HERDSMAN_DUE_<name> (epoch seconds, e.g. `date -v+45M +%s`; optional, one per worker),
# HERDSMAN_REVIEWER_PANE, HERDSMAN_REVIEWER_NAME, DEADLINE (default 540), POLL (default 60), or edit the defaults.
# DEADLINE is how long this waiter runs before TICK; a worker's time box is HERDSMAN_DUE_<name>.
REPORTS=${HERDSMAN_REPORTS:-/ABSOLUTE/PATH/TO/writable-root/reports}
STATE=${HERDSMAN_STATE:-/ABSOLUTE/PATH/TO/scratchpad/watch-state}
WORKERS=${HERDSMAN_WORKERS:-"wk-a wk-b wk-c wk-d"}
RPANE=${HERDSMAN_REVIEWER_PANE:-w2:pR}
RNAME=${HERDSMAN_REVIEWER_NAME:-rv-1}
DEADLINE=${DEADLINE:-540}
POLL=${POLL:-60}
mkdir -p "$STATE"
pane_of() {
  var="HERDSMAN_PANE_$(printf '%s' "$1" | tr '-' '_')"
  env_pane=$(eval "printf '%s' \"\${$var:-}\"")
  if [ -n "$env_pane" ]; then echo "$env_pane"; return; fi
  case "$1" in
    wk-a) echo w2:pA ;;
    wk-b) echo w2:pB ;;
    wk-c) echo w2:pC ;;
    wk-d) echo w2:pD ;;
  esac
}
task_of() { echo "${1#wk-}"; }
BLOCK_RE='Password for|Device not configured|Allow once|Allow always|Yes, proceed|approval required|Switch to gpt|Usage limit|Queued follow-up inputs|Type your answer|Allow reads outside|Do you want to proceed|Permission required|Do you trust'
WORK_RE='esc to interrupt|Pursuing goal|Incubating|thinking'
start=$(date +%s)
while true; do
  events=""
  now=$(date '+%H:%M:%S')
  for name in $WORKERS; do
    task=$(task_of "$name")
    report="$REPORTS/$task.md"
    has_report=0
    if [ -f "$report" ]; then
      has_report=1
      mtime=$(stat -f %m "$report" 2>/dev/null)
      prevm=$(cat "$STATE/mtime-$task" 2>/dev/null)
      printf '%s' "$mtime" > "$STATE/mtime-$task"
      if [ ! -f "$STATE/report-$task" ]; then
        touch "$STATE/report-$task"
        events="$events
REPORT $task $report $now"
      elif [ -n "$prevm" ] && [ "$mtime" != "$prevm" ]; then
        events="$events
REPORT-UPDATED $task $report $now"
      fi
    fi
    # Workers ignore stop times written in their own briefs, so the waiter enforces the due time.
    # The baseline is the report's mtime when this due time was first seen, so a fix round whose
    # report already exists is overdue until the report changes, and a new due time re-arms.
    duevar="HERDSMAN_DUE_$(printf '%s' "$name" | tr '-' '_')"
    due=$(eval "printf '%s' \"\${$duevar:-}\"")
    if [ -n "$due" ]; then
      cur=$(stat -f %m "$report" 2>/dev/null || echo none)
      if [ "$(cat "$STATE/due-$name" 2>/dev/null)" != "$due" ]; then
        printf '%s' "$due" > "$STATE/due-$name"
        printf '%s' "$cur" > "$STATE/duebase-$name"
        rm -f "$STATE/overdue-$name"
      fi
      if [ "$(date +%s)" -ge "$due" ] && [ ! -f "$STATE/overdue-$name" ] \
        && [ "$cur" = "$(cat "$STATE/duebase-$name" 2>/dev/null)" ]; then
        touch "$STATE/overdue-$name"
        events="$events
OVERDUE $name due $(date -r "$due" '+%H:%M') no report since the due time was set $now"
      fi
    fi
    text=$(herdr pane read "$(pane_of "$name")" --source recent-unwrapped --lines 40 2>/dev/null)
    hash=$(printf '%s' "$text" | cksum | cut -d' ' -f1)
    prev=$(cat "$STATE/hash-$name" 2>/dev/null)
    printf '%s' "$hash" > "$STATE/hash-$name"
    if printf '%s' "$text" | grep -q -E "$BLOCK_RE"; then
      if [ "$hash" != "$prev" ]; then
        events="$events
BLOCKED $name $(printf '%s' "$text" | grep -o -E "$BLOCK_RE" | head -1) $now"
      fi
      printf '0' > "$STATE/stall-$name"
    elif printf '%s' "$text" | grep -q -E "$WORK_RE"; then
      rm -f "$STATE/goaldone-$name"
      if [ "$hash" = "$prev" ]; then
        c=$(cat "$STATE/stall-$name" 2>/dev/null); c=$((${c:-0}+1)); printf '%s' "$c" > "$STATE/stall-$name"
        if [ "$c" -ge 3 ]; then
          printf '0' > "$STATE/stall-$name"
          events="$events
STALL $name screen unchanged for 3 polls $now"
        fi
      else
        printf '0' > "$STATE/stall-$name"
      fi
    else
      printf '0' > "$STATE/stall-$name"
      if [ "$has_report" -eq 0 ] && printf '%s' "$text" | grep -q 'Goal achieved'; then
        if [ ! -f "$STATE/goaldone-$name" ]; then
          touch "$STATE/goaldone-$name"
          events="$events
GOAL-DONE-NO-REPORT $name $now"
        fi
      fi
    fi
  done
  for f in "$REPORTS"/review-*.md; do
    [ -f "$f" ] || continue
    key=$(basename "$f")
    if [ ! -f "$STATE/seen-$key" ]; then
      touch "$STATE/seen-$key"
      events="$events
REVIEW $f $now"
    fi
  done
  text=$(herdr pane read "$RPANE" --source recent-unwrapped --lines 30 2>/dev/null)
  hash=$(printf '%s' "$text" | cksum | cut -d' ' -f1)
  prev=$(cat "$STATE/hash-$RNAME" 2>/dev/null)
  printf '%s' "$hash" > "$STATE/hash-$RNAME"
  if printf '%s' "$text" | grep -q -E "$BLOCK_RE" && [ "$hash" != "$prev" ]; then
    events="$events
BLOCKED $RNAME $(printf '%s' "$text" | grep -o -E "$BLOCK_RE" | head -1) $now"
  fi
  if [ -n "$events" ]; then
    printf '%s\n' "$events" | sed '/^$/d'
    exit 0
  fi
  elapsed=$(( $(date +%s) - start ))
  if [ "$elapsed" -ge "$DEADLINE" ]; then
    echo "TICK no event in ${elapsed}s $now"
    exit 0
  fi
  sleep "$POLL"
done
