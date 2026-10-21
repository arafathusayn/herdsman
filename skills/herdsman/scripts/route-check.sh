#!/bin/bash
# Check what a route folder shows on disk, and print the checklist lines that are still open.
# It checks files and lines only; it cannot see Herdr, GitHub or an agent's work, so a pass here is no proof of those.
# The files it requires follow the route's own checklist: a kept line names the route shape (for example C2.5 keeps
# the contracts of a route with implementers), so a review-only route is not asked for task contracts.
# Usage: route-check.sh <route folder> [plan|launch|close]
#   plan (default): the route file's sections and checklist lines, the git ignore, the files the kept lines require.
#   launch: plan, plus every task-table row names a pane.
#   close: launch, plus no open checklist line.
# Prints "ok <check>", "MISSING <check>", "BAD <line>" and "OPEN <line>"; exits 1 on any MISSING or BAD (and on OPEN
# at close), 2 on usage.
R=${1:-}
STAGE=${2:-plan}
usage() { echo 'usage: route-check.sh <route folder> [plan|launch|close]' >&2; exit 2; }
[ -n "$R" ] && [ -d "$R" ] || usage
case "$STAGE" in plan|launch|close) ;; *) usage ;; esac
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

# Checklist lines: "- [ ]" open; "- [x] ... -- <evidence>" done; "- [-] ... -- <reason>" dropped. Anything else is BAD.
lines=$(awk '/^## Checklist/ {c=1; next} /^## / {c=0} c && /^- \[/' "$F")
if [ -z "$lines" ]; then
  missing "checklist lines in the Checklist section"
else
  bad=$(printf '%s\n' "$lines" | awk '
    /^- \[ \] / {next}
    /^- \[[x-]\] / { n=index($0, " -- "); if (n > 0 && substr($0, n+4) ~ /[^ ]/) next }
    {print}')
  if [ -n "$bad" ]; then
    printf '%s\n' "$bad" | while IFS= read -r l; do echo "BAD $l (done needs \" -- <evidence>\", dropped needs \" -- <reason>\")"; done
    miss=1
  else
    ok "checklist lines well formed"
  fi
fi
has() { printf '%s\n' "$lines" | grep -q "^- \[.\] $1 "; }

# Files that the kept checklist lines require.
need() { # path, check name
  if [ -e "$1" ]; then ok "$2"; else missing "$2"; fi
}
if has C2.5; then
  need "$R/10-shared-rules.md" "10-shared-rules.md (C2.5)"
  set -- "$R"/1[0-9]-task-*.md
  if [ -e "$1" ]; then ok "task contracts (C2.5)"; else missing "a 1n-task-<x>.md contract (C2.5)"; fi
fi
has C2.6 && need "$R/1n-task-i.md" "1n-task-i.md (C2.6)"
if has C2.7; then
  set -- "$R"/20-*.md
  if [ -e "$1" ]; then ok "reviewer contract or brief (C2.7)"; else missing "20-reviewer.md or the review brief (C2.7)"; fi
fi
for c in "$R/10-shared-rules.md" "$R/20-reviewer.md"; do
  [ -f "$c" ] || continue
  if grep -q 'Must prove' "$c"; then ok "Must prove in ${c##*/}"; else missing "Must prove list in ${c##*/}"; fi
done
for c in "$R"/1[0-9]-task-*.md; do
  [ -f "$c" ] || continue
  if grep -q '^Due: ' "$c"; then ok "Due line in ${c##*/}"; else missing "Due line in ${c##*/}"; fi
done
if has C2.8 || grep -l 'run-check\.sh' "$R"/[12][0-9]-*.md >/dev/null 2>&1; then
  if [ -x "$R/run-check.sh" ]; then ok "run-check.sh executable"; else missing "executable run-check.sh (C2.8 or a contract names it)"; fi
fi

if [ "$STAGE" != plan ]; then
  # Find the Pane column from the header row, then require a real pane in every task row.
  panes=$(awk -F'|' '
    /^## Task table/ {t=1; next} /^## / {t=0}
    !t || !/^\|/ {next}
    !col { for (i=2; i<NF; i++) { h=$i; gsub(/^ +| +$/, "", h); if (h=="Pane") col=i } ; if (!col) {print "NOHEADER"; exit} ; next }
    /^\| *-+ *\|/ {next}
    { rows++; p=$col; gsub(/^ +| +$/, "", p); if (p=="" || p=="tbd" || p=="TBD" || p=="?" || p=="-") print "PLACEHOLDER " $0 }
    END { if (col && !rows) print "NOROWS" }' "$F")
  case "$panes" in
    "") ok "task table panes filled" ;;
    NOHEADER*) missing "a Pane column in the task table header" ;;
    NOROWS*) missing "task rows in the task table" ;;
    *) missing "pane in the task table: $(printf '%s' "$panes" | head -1 | sed 's/^PLACEHOLDER //')" ;;
  esac
fi

open=$(printf '%s\n' "$lines" | awk '/^- \[ \] /')
if [ -n "$open" ]; then
  printf '%s\n' "$open" | while IFS= read -r l; do echo "OPEN ${l#- \[ \] }"; done
  [ "$STAGE" = close ] && miss=1
elif [ -n "$lines" ]; then
  ok "no open checklist line"
fi
exit $miss
