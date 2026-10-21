#!/bin/bash
# Check what a route folder shows on disk, and print the checklist lines that are still open.
# It checks files and sections only; it cannot see Herdr, GitHub or an agent's work, so a pass here is no proof of those.
# Usage: route-check.sh <route folder> [plan|launch|close]
#   plan (default): the route file's sections, the git ignore, the contracts, run-check.sh.
#   launch: plan, plus no placeholder pane in the task table.
#   close: launch, plus no open checklist line.
# Prints "ok <check>", "MISSING <check>" and "OPEN <line>"; exits 1 on any MISSING (and on OPEN at close), 2 on usage.
R=${1:-}
STAGE=${2:-plan}
if [ -z "$R" ] || [ ! -d "$R" ]; then
  echo 'usage: route-check.sh <route folder> [plan|launch|close]' >&2
  exit 2
fi
case "$STAGE" in plan|launch|close) ;; *) echo 'usage: route-check.sh <route folder> [plan|launch|close]' >&2; exit 2 ;; esac
F="$R/00-route.md"
miss=0
ok() { echo "ok $1"; }
missing() { echo "MISSING $1"; miss=1; }

if [ ! -f "$F" ]; then
  missing "00-route.md"
  exit 1
fi
for h in 'Decisions' 'Must prove' 'Task table' 'Checklist' 'Run state'; do
  if grep -q "^## $h" "$F"; then ok "section $h"; else missing "section $h in 00-route.md"; fi
done
if git -C "$R" check-ignore -q "$R/00-route.md" 2>/dev/null; then ok "route folder ignored by git"; else missing "git ignore for the route folder (.herdsman/)"; fi

for c in "$R/10-shared-rules.md" "$R/20-reviewer.md"; do
  [ -f "$c" ] || continue
  if grep -q 'Must prove' "$c"; then ok "Must prove in ${c##*/}"; else missing "Must prove list in ${c##*/}"; fi
done
for c in "$R"/1[0-9]-task-*.md; do
  [ -f "$c" ] || continue
  if grep -q '^Due: ' "$c"; then ok "Due line in ${c##*/}"; else missing "Due line in ${c##*/}"; fi
done
if grep -l 'run-check\.sh' "$R"/[12][0-9]-*.md >/dev/null 2>&1; then
  if [ -x "$R/run-check.sh" ]; then ok "run-check.sh executable"; else missing "executable run-check.sh (a contract names it)"; fi
fi

if [ "$STAGE" != plan ]; then
  tbd=$(awk '/^## Task table/ {t=1; next} /^## / {t=0} t && /^\|/ && /\| *(tbd|TBD|\?|-) *\|? *$/' "$F")
  if [ -z "$tbd" ]; then ok "task table panes filled"; else missing "pane in the task table: $(printf '%s' "$tbd" | head -1)"; fi
fi

open=$(awk '/^## Checklist/ {c=1; next} /^## / {c=0} c && /^- \[ \]/' "$F")
if [ -n "$open" ]; then
  printf '%s\n' "$open" | while IFS= read -r l; do echo "OPEN ${l#- \[ \] }"; done
  [ "$STAGE" = close ] && miss=1
else
  ok "no open checklist line"
fi
exit $miss
