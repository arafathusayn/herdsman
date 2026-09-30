#!/bin/bash
# One-shot health snapshot of every agent (implementers, the integrator, reviewers): state, model,
# background jobs with their ages, the foreground spinner, and recent file activity in the tree each agent writes to.
# Flags a job older than LIMIT_MIN minutes, or a tree with no writes in 10 min.
# Run on every waiter wake (user rule: check every agent every ten minutes; background jobs must not take too long).
# HERDSMAN_STATUS_SPECS="im-a:w7:p14:/abs/repo int-1:w7:p15:/abs/int rv-1:w7:p17:" (name:pane:tree; empty tree = skip the write check)
LIMIT_MIN=${LIMIT_MIN:-30}
: "${HERDSMAN_STATUS_SPECS:?set HERDSMAN_STATUS_SPECS}"
date '+%H:%M:%S'
for spec in $HERDSMAN_STATUS_SPECS; do
  name=${spec%%:*}; rest=${spec#*:}; pane=${rest%:*}; tree=${rest##*:}
  state=$(herdr agent get "$name" 2>/dev/null | grep -o '"agent_status":"[a-z]*"' | cut -d'"' -f4)
  screen=$(herdr pane read "$pane" --source visible --lines 60 2>/dev/null)
  model=$(printf '%s\n' "$screen" | grep -o 'muse-spark-[0-9.a-z-]*\|Opus [0-9.]*\|gpt-[0-9a-z.-]*' | tail -1)
  echo "== $name ($pane) state=$state model=$model"
  # Background tasks: footer rows like "└ ◆ <label>  running  23m 45s" or "... 1h 02m".
  printf '%s\n' "$screen" | grep -E '(running|workflow).*[0-9]+(m|h) ?[0-9]*s?' | tail -3 | while IFS= read -r row; do
    h=$(printf '%s' "$row" | grep -oE '[0-9]+h' | tr -d h); m=$(printf '%s' "$row" | grep -oE '[0-9]+m' | tail -1 | tr -d m)
    mins=$(( ${h:-0} * 60 + ${m:-0} ))
    flag=""; [ "$mins" -ge "$LIMIT_MIN" ] && flag="  <-- OVER ${LIMIT_MIN}m"
    echo "   bg: $(printf '%s' "$row" | sed 's/^[^A-Za-z]*//' | cut -c1-70) [${mins}m]$flag"
  done
  printf '%s\n' "$screen" | grep -E 'Thinking|Calling tools|esc to interrupt' | tail -1 | sed 's/^/   fg: /'
  if [ -n "$tree" ]; then
    n=$(find "$tree" -path "$tree/node_modules" -prune -o -path '*/node_modules' -prune -o -path "$tree/.git" -prune -o -type f -mmin -10 -print 2>/dev/null | wc -l | tr -d ' ')
    flag=""; [ "$n" -eq 0 ] && flag="  <-- NO WRITES in 10m"
    echo "   files changed in last 10m under $(basename "$tree"): $n$flag"
  fi
done
