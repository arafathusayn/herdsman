#!/bin/bash
# Checkpointer for the orchestrator pane (user request 2026-09-27).
# Every INTERVAL s: if the orchestrator did work since the last checkpoint, wait until it is idle,
# send /memory-with-dag, wait for that turn, then send /compact when context >= LIMIT tokens.
# Run it in its own pane: herdr pane run <pane> "TARGET=<orchestrator pane> /bin/bash <skill>/scripts/checkpointer.sh"
# Probe (no prompts sent): TARGET=<pane> /bin/bash checkpointer.sh --probe
TARGET="${TARGET:?set TARGET to the orchestrator pane id, e.g. w7:p1}"
INTERVAL="${INTERVAL:-900}"
LIMIT="${LIMIT:-250000}"
LOG="${LOG:-${TMPDIR:-/tmp}/checkpointer.log}"
PROMPT='/memory-with-dag Capture all learnings as lessons in your local memory and if needed in kb. Keep index memory file lean, each entry should have timestamps and expand from index root using DAG like structure and knowledge graph. Do it now. Focus on reproduction of the understaning of the current state for this project because I'"'"'m going to compact your context.'

log() { echo "$(date '+%H:%M:%S') $*" | tee -a "$LOG"; }

# The transcript is found from the session id Herdr reports, so it follows a resumed session.
transcript() {
  local sid
  sid=$(herdr agent get "$TARGET" | jq -r '.result.agent.agent_session.value')
  ls -t "$HOME"/.claude/projects/*/"$sid".jsonl 2>/dev/null | head -1
}

# Context = input + cache creation + cache read of the last main-thread assistant turn.
context_tokens() {
  tail -n 400 "$1" | jq -r 'select(.type=="assistant" and .message.usage and (.isSidechain|not))
    | .message.usage | (.input_tokens + (.cache_creation_input_tokens//0) + (.cache_read_input_tokens//0))' | tail -1
}

# Blocked means a permission dialog; typing into it would answer it, so wait for idle or done only.
wait_idle() { herdr agent wait "$TARGET" --until idle --until done >/dev/null; }

if [ "$1" = --probe ]; then f=$(transcript); echo "$f context=$(context_tokens "$f")"; exit 0; fi

last_size=0
log "checkpointer on $TARGET: every ${INTERVAL}s, compact at >= $LIMIT tokens"
while true; do
  sleep "$INTERVAL"
  f=$(transcript)
  [ -z "$f" ] && { log "no transcript found; skip"; continue; }
  size=$(stat -f %z "$f")
  if [ "$size" = "$last_size" ]; then log "no activity since last checkpoint; skip"; continue; fi
  wait_idle
  log "sending /memory-with-dag"
  herdr agent prompt "$TARGET" "$PROMPT" >/dev/null || { log "prompt failed; retry next interval"; continue; }
  herdr agent wait "$TARGET" --until working --timeout 30000 >/dev/null 2>&1
  wait_idle
  tokens=$(context_tokens "$f")
  log "memory checkpoint done; context=${tokens:-?}"
  if [ -n "$tokens" ] && [ "$tokens" -ge "$LIMIT" ]; then
    log "sending /compact"
    herdr agent prompt "$TARGET" "/compact" >/dev/null
    herdr agent wait "$TARGET" --until working --timeout 30000 >/dev/null 2>&1
    wait_idle
    log "compact done; context=$(context_tokens "$f")"
  fi
  last_size=$(stat -f %z "$f")
done
