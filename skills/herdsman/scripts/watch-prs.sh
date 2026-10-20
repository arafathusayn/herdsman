#!/bin/bash
# herdsman pull request watcher. Run ONLY as a harness background task (a detached process
# never wakes the orchestrator), with the harness's 30-minute limit as the timeout:
#   HERDSMAN_PRS="<pr> <pr>" HERDSMAN_REPO=org/repo HERDSMAN_PR_STATE=<scratch dir> \
#   HERDSMAN_GH_ACCOUNT=<gh account> /bin/bash <skill>/scripts/watch-prs.sh
# Polls every HERDSMAN_PR_INTERVAL seconds (default 120) and exits on the first change on any
# watched pull request: a new issue comment, inline comment or review (compared by id, so a push
# that moves lines does not re-emit), a new head commit, a state change, or a check whose result
# changes. The first run per pull request only records a baseline. Exits with TICK after
# HERDSMAN_PR_DEADLINE seconds (default 1500). Re-arm after every wake.
# Note: review bots often post an "in progress" comment first and edit it later. The new id
# fires at once; the bot's check turning SUCCESS is the sign that the edited review is final.
PRS="${HERDSMAN_PRS:?set HERDSMAN_PRS}"
REPO="${HERDSMAN_REPO:?set HERDSMAN_REPO}"
STATE="${HERDSMAN_PR_STATE:?set HERDSMAN_PR_STATE}"
INTERVAL="${HERDSMAN_PR_INTERVAL:-120}"
DEADLINE="${HERDSMAN_PR_DEADLINE:-1500}"
export PATH="$HOME/.local/bin:/opt/homebrew/bin:$PATH"
mkdir -p "$STATE"
if [ -n "${HERDSMAN_GH_ACCOUNT:-}" ]; then
  GH_TOKEN=$(gh auth token --user "$HERDSMAN_GH_ACCOUNT") || { echo "PR-WATCH-ERROR no gh token for $HERDSMAN_GH_ACCOUNT"; exit 1; }
  export GH_TOKEN
fi

snapshot() {
  local n=$1 ic rc rv view
  ic=$(gh api "repos/$REPO/issues/$n/comments" --paginate --jq '.[] | "comment \(.id) \(.user.login)"') || return 1
  rc=$(gh api "repos/$REPO/pulls/$n/comments" --paginate --jq '.[] | "inline \(.id) \(.user.login) \(.path)"') || return 1
  rv=$(gh api "repos/$REPO/pulls/$n/reviews" --paginate --jq '.[] | "review \(.id) \(.user.login) \(.state)"') || return 1
  view=$(gh pr view "$n" --repo "$REPO" --json state,headRefOid,statusCheckRollup \
    --jq '"state \(.state)", "head \(.headRefOid[0:8])", (.statusCheckRollup[] | "check \(.name // .context)=\(.conclusion // .state // .status)")') || return 1
  printf '%s\n%s\n%s\n%s\n' "$ic" "$rc" "$rv" "$view" | grep -v '^$' | sort
}

start=$(date +%s)
while :; do
  events=""
  for n in $PRS; do
    new=$(snapshot "$n") || continue
    old_file="$STATE/pr-$n.txt"
    if [ ! -f "$old_file" ]; then
      printf '%s\n' "$new" > "$old_file"
      continue
    fi
    added=$(printf '%s\n' "$new" | comm -13 "$old_file" -)
    if [ -n "$added" ]; then
      printf '%s\n' "$new" > "$old_file"
      # Wake only on what needs a decision: comments, reviews, heads, state, failed checks,
      # and the checks named in HERDSMAN_PR_WAKE_CHECKS reaching SUCCESS. Pending and other
      # passing checks are recorded silently (each transition used to cost a whole turn).
      wake_checks="${HERDSMAN_PR_WAKE_CHECKS:-claude-review|test}"
      wanted=$(printf '%s\n' "$added" | grep -E -v '^check ' ; printf '%s\n' "$added" | grep -E "^check .*=(FAILURE|CANCELLED|TIMED_OUT|ACTION_REQUIRED|STARTUP_FAILURE|ERROR)\$|^check (${wake_checks})=SUCCESS\$")
      wanted=$(printf '%s\n' "$wanted" | grep -v '^$')
      if [ -n "$wanted" ]; then
        events="$events$(printf '%s\n' "$wanted" | sed "s/^/PR $n NEW /")
"
      fi
    fi
  done
  if [ -n "$events" ]; then
    printf '%s' "$events"
    echo "at $(date +%H:%M:%S)"
    exit 0
  fi
  if [ $(( $(date +%s) - start )) -ge "$DEADLINE" ]; then
    echo "TICK no PR change in ${DEADLINE}s $(date +%H:%M:%S)"
    exit 0
  fi
  sleep "$INTERVAL"
done
