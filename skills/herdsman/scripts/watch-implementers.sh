#!/bin/bash
# herdsman implementer and integrator watcher. bash 3 compatible (macOS /bin/bash): no associative arrays.
# Events on stdout: REPORT <task> <path> (first appearance), REPORT-UPDATED <task> <path> (mtime changed: a fix round),
# BLOCKED <name> <text> (once per screen change), STALL <name>, IDLE <name>.
# Panes stay watched after a report exists (fix rounds). The script never exits on its own: stop it with TaskStop at route end.
# Either edit the defaults below or set HERDSMAN_REPORTS, HERDSMAN_STATE, HERDSMAN_IMPLEMENTERS, HERDSMAN_INTEGRATOR (one name, optional)
# and HERDSMAN_PANE_<name> (dashes as underscores) in the environment.
REPORTS=${HERDSMAN_REPORTS:-/ABSOLUTE/PATH/TO/writable-root/reports}
STATE=${HERDSMAN_STATE:-/ABSOLUTE/PATH/TO/scratchpad/watch-state}
IMPLEMENTERS=${HERDSMAN_IMPLEMENTERS:-"im-a im-b im-c im-d"}
INTEGRATOR=${HERDSMAN_INTEGRATOR:-}
mkdir -p "$STATE"
# GNU and BSD (macOS) stat differ.
if stat -c %Y / >/dev/null 2>&1; then file_mtime() { stat -c %Y "$1" 2>/dev/null; }
else file_mtime() { stat -f %m "$1" 2>/dev/null; }; fi
pane_of() {
  var="HERDSMAN_PANE_$(printf '%s' "$1" | tr '-' '_')"
  env_pane=$(eval "printf '%s' \"\${$var:-}\"")
  if [ -n "$env_pane" ]; then echo "$env_pane"; return; fi
  case "$1" in
    im-a) echo w2:pA ;;
    im-b) echo w2:pB ;;
    im-c) echo w2:pC ;;
    im-d) echo w2:pD ;;
  esac
}
task_of() { echo "${1#im-}"; }
BLOCK_RE='Password for|Device not configured|Allow once|Allow always|Yes, proceed|approval required|Switch to gpt|Usage limit|Do you trust|Permission required|Queued follow-up inputs|Type your answer'
WORK_RE='esc to interrupt|Pursuing goal'
while true; do
  for name in $IMPLEMENTERS $INTEGRATOR; do
    task=$(task_of "$name")
    report="$REPORTS/$task.md"
    if [ -f "$report" ]; then
      mtime=$(file_mtime "$report")
      prevm=$(cat "$STATE/mtime-$task" 2>/dev/null)
      printf '%s' "$mtime" > "$STATE/mtime-$task"
      if [ ! -f "$STATE/report-$task" ]; then
        touch "$STATE/report-$task"
        echo "REPORT $task $report $(date '+%H:%M:%S')"
      elif [ -n "$prevm" ] && [ "$mtime" != "$prevm" ]; then
        echo "REPORT-UPDATED $task $report $(date '+%H:%M:%S')"
      fi
    fi
    text=$(herdr pane read "$(pane_of "$name")" --source recent-unwrapped --lines 40 2>/dev/null)
    hash=$(printf '%s' "$text" | cksum | cut -d' ' -f1)
    prev=$(cat "$STATE/hash-$name" 2>/dev/null)
    printf '%s' "$hash" > "$STATE/hash-$name"
    if printf '%s' "$text" | grep -q -E "$BLOCK_RE"; then
      if [ "$hash" != "$prev" ]; then
        echo "BLOCKED $name $(printf '%s' "$text" | grep -o -E "$BLOCK_RE" | head -1) $(date '+%H:%M:%S')"
      fi
    elif printf '%s' "$text" | grep -q -E "$WORK_RE"; then
      if [ "$hash" = "$prev" ]; then
        echo "STALL $name screen unchanged $(date '+%H:%M:%S')"
      fi
    else
      if [ "$hash" != "$prev" ]; then
        echo "IDLE $name $(date '+%H:%M:%S')"
      fi
    fi
  done
  sleep 120
done
