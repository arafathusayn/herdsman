#!/bin/bash
# herdsman reviewer watcher. bash 3 compatible. Copy to the scratchpad, set the three variables, run as a Monitor.
# Events on stdout: REVIEW <file>, BLOCKED <name> <text>, IDLE <name>.
# Either edit the defaults below or set HERDSMAN_REPORTS, HERDSMAN_STATE, HERDSMAN_REVIEWER_PANE and HERDSMAN_REVIEWER_NAME in the environment.
REPORTS=${HERDSMAN_REPORTS:-/ABSOLUTE/PATH/TO/writable-root/reports}
STATE=${HERDSMAN_STATE:-/ABSOLUTE/PATH/TO/scratchpad/watch-state}
PANE=${HERDSMAN_REVIEWER_PANE:-w2:pR}
NAME=${HERDSMAN_REVIEWER_NAME:-rv-1}
mkdir -p "$STATE"
BLOCK_RE='Password for|Device not configured|Allow reads outside|Do you want to proceed|Yes, and don|usage limit|rate limit|Permission required'
WORK_RE='esc to interrupt|thinking|Incubating|tokens|Pursuing goal'
while true; do
  for f in "$REPORTS"/review-*.md; do
    [ -f "$f" ] || continue
    key=$(basename "$f")
    if [ ! -f "$STATE/seen-$key" ]; then
      touch "$STATE/seen-$key"
      echo "REVIEW $f $(date '+%H:%M:%S')"
    fi
  done
  text=$(herdr pane read "$PANE" --source recent-unwrapped --lines 30 2>/dev/null)
  hash=$(printf '%s' "$text" | cksum | cut -d' ' -f1)
  prev=$(cat "$STATE/hash-$NAME" 2>/dev/null)
  printf '%s' "$hash" > "$STATE/hash-$NAME"
  if printf '%s' "$text" | grep -q -E "$BLOCK_RE"; then
    echo "BLOCKED $NAME $(printf '%s' "$text" | grep -o -E "$BLOCK_RE" | head -1) $(date '+%H:%M:%S')"
  elif ! printf '%s' "$text" | grep -q -E "$WORK_RE"; then
    if [ "$hash" != "$prev" ]; then
      echo "IDLE $NAME $(date '+%H:%M:%S')"
    fi
  fi
  sleep 90
done
