#!/bin/bash
# One-shot health snapshot of every agent (implementers, the integrator, reviewers): state, model,
# background jobs with their ages, the foreground spinner, and recent file activity in the tree each agent writes to.
# Flags a job older than LIMIT_MIN minutes, a tree with no writes in 10 min, and a loaded machine.
# Run on every waiter wake (user rule: check every agent every ten minutes; background jobs must not take too long).
# HERDSMAN_STATUS_SPECS="im-a:w7:p14:/abs/repo int-1:w7:p15:/abs/int rv-1:w7:p17:" (name:pane:tree; empty tree = skip the write check)
LIMIT_MIN=${LIMIT_MIN:-30}
: "${HERDSMAN_STATUS_SPECS:?set HERDSMAN_STATUS_SPECS}"
date '+%H:%M:%S'
# Machine: a 5-minute load average above the core count means work waits for a CPU; under 10% free
# memory the machine swaps. Either one is the moment to run fewer heavy checks (scripts/run-check.sh).
if [ -r /proc/loadavg ]; then load=$(cut -d' ' -f2 /proc/loadavg)
else load=$(LC_ALL=C sysctl -n vm.loadavg 2>/dev/null | tr -d '{}' | awk '{print $2}'); fi
cpus=$(sysctl -n hw.ncpu 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null)
free=$(sysctl -n kern.memorystatus_level 2>/dev/null)
[ -z "$free" ] && [ -r /proc/meminfo ] && free=$(awk '/^MemTotal/{t=$2} /^MemAvailable/{a=$2} END{if (t) print int(a*100/t)}' /proc/meminfo)
echo "machine: load ${load:-?} (5 min) on ${cpus:-?} cores, memory free ${free:-?}%"
awk -v l="${load:-0}" -v c="${cpus:-0}" 'BEGIN{exit !(c > 0 && l > c)}' && echo "   <-- LOAD HIGH"
[ -n "$free" ] && [ "$free" -lt 10 ] && echo "   <-- MEMORY LOW"
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
    # Prune dependency and build caches, and stop counting at 50: the flag needs only "none".
    n=$(find "$tree" \( -name node_modules -o -name .git -o -name .next -o -name .turbo -o -name .venv -o -name target \) -prune \
      -o -type f -mmin -10 -print 2>/dev/null | head -n 50 | wc -l | tr -d ' ')
    flag=""; [ "$n" -eq 0 ] && flag="  <-- NO WRITES in 10m"; [ "$n" -eq 50 ] && n="50+"
    echo "   files changed in last 10m under $(basename "$tree"): $n$flag"
  fi
done
