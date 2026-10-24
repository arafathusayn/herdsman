#!/bin/bash
# Checkpointer for the orchestrator pane.
# Every INTERVAL s: if the orchestrator did work since the last checkpoint, wait until it is idle,
# send PROMPT (a memory checkpoint), wait for that turn, then send COMPACT when context >= LIMIT tokens.
# Run it in its own pane: herdr pane run <pane> "TARGET=<orchestrator pane> /bin/bash <skill>/scripts/checkpointer.sh"
# Probe (no prompts sent; prints which transcript it chose and why, and a hint when a newer one exists):
#   TARGET=<pane> /bin/bash checkpointer.sh --probe
# One cycle now, then exit:  TARGET=<pane> /bin/bash checkpointer.sh --once
# Set TRANSCRIPT=<path> in any of these forms to name the transcript when the probe shows that the session id is stale.
# Adjutant mode: set HERDSMAN_ORCHESTRATOR=<enrolled name> with HERDSMAN_INBOX and HERDSMAN_PREFLIGHT. Right before each
# input, checkpointer-guard.sh then checks TARGET against the approved message registry; a failed check sends nothing.
TARGET="${TARGET:?set TARGET to the orchestrator pane id, e.g. w7:p1}"
INTERVAL="${INTERVAL:-900}"
LIMIT="${LIMIT:-250000}"
LOG="${LOG:-${TMPDIR:-/tmp}/checkpointer.log}"
COMPACT="${COMPACT:-/compact}"
# Kept in its own variable: an apostrophe inside a "${PROMPT:-...}" default breaks bash quoting.
DEFAULT_PROMPT='/memory-with-dag Capture all learnings as lessons in your local memory and if needed in kb. Keep index memory file lean, each entry should have timestamps and expand from index root using DAG like structure and knowledge graph. Do it now. Focus on reproduction of the understaning of the current state for this project because I'"'"'m going to compact your context.'
PROMPT="${PROMPT:-$DEFAULT_PROMPT}"

# GNU and BSD (macOS) stat differ.
if stat -c %s / >/dev/null 2>&1; then file_size() { stat -c %s "$1"; }
else file_size() { stat -f %z "$1"; }; fi

log() { echo "$(date '+%H:%M:%S') $*" | tee -a "$LOG"; }

# The transcript is found from the session id Herdr reports, so it follows a resumed session.
# Herdr learns that id from a start hook, saves it with the layout and restores it after a restart, so the id
# can name a transcript that the running session never wrote. The named file is always used when it exists. Another
# session of the same project folder can write a newer file while this orchestrator waits, so a newer file is a
# hint only: --probe prints it and the script never reads it. With no named file there is no transcript, and
# the script skips (the hint then names the newest .jsonl of the folder that matches the working folder of this pane).
# TRANSCRIPT=<path> overrides the choice: use it when --probe shows that the id is stale.
# It sets TRANSCRIPT_FILE (empty when none), TRANSCRIPT_WHY (the reason; --probe prints it) and TRANSCRIPT_HINT.
transcript() {
  local sid named dir newest
  TRANSCRIPT_FILE=""; TRANSCRIPT_HINT=""
  if [ -n "${TRANSCRIPT:-}" ]; then
    if [ -f "$TRANSCRIPT" ]; then
      TRANSCRIPT_FILE=$TRANSCRIPT
      TRANSCRIPT_WHY="TRANSCRIPT is set; the file is used whatever the session id says"
    else
      TRANSCRIPT_WHY="TRANSCRIPT is set but the file does not exist: $TRANSCRIPT"
    fi
    return
  fi
  sid=$(herdr agent get "$TARGET" | jq -r '.result.agent.agent_session.value')
  named=$(ls -t "$HOME"/.claude/projects/*/"$sid".jsonl 2>/dev/null | head -1)
  if [ -n "$named" ]; then dir=$(dirname "$named")
  else dir="$HOME/.claude/projects/$(printf '%s' "$PWD" | sed 's/[^A-Za-z0-9]/-/g')"; fi
  newest=$(ls -t "$dir"/*.jsonl 2>/dev/null | head -1)
  if [ -n "$named" ]; then
    TRANSCRIPT_FILE=$named
    TRANSCRIPT_WHY="the file of the session id that Herdr reports ('$sid')"
    if [ "$newest" -nt "$named" ]; then
      TRANSCRIPT_HINT="a newer transcript exists: $newest; if the id is stale, set TRANSCRIPT=<path>"
    fi
  elif [ -z "$newest" ]; then
    TRANSCRIPT_WHY="no file for session id '$sid' and no .jsonl in $dir"
  else
    TRANSCRIPT_WHY="no file for session id '$sid'"
    TRANSCRIPT_HINT="the newest transcript of $dir is $newest; if the id is stale, set TRANSCRIPT=<path>"
  fi
}

# Context = input + cache creation + cache read of the last main-thread assistant turn.
context_tokens() {
  tail -n 400 "$1" | jq -r 'select(.type=="assistant" and .message.usage and (.isSidechain|not))
    | .message.usage | (.input_tokens + (.cache_creation_input_tokens//0) + (.cache_read_input_tokens//0))' | tail -1
}

# Ordinary mode has no guard. In adjutant mode the target must still be the approved orchestrator, never the adjutant.
guard() {
  local out
  [ -z "${HERDSMAN_ORCHESTRATOR:-}" ] && return 0
  out=$(/bin/bash "$(dirname "$0")/checkpointer-guard.sh" "$HERDSMAN_ORCHESTRATOR" "$TARGET" 2>&1) && return 0
  log "identity check failed, no input sent: $out"; return 1
}

# Blocked means a permission dialog; typing into it would answer it, so wait for idle or done only.
wait_idle() { herdr agent wait "$TARGET" --until idle --until 'done' >/dev/null; }

# One checkpoint: memory prompt, then compaction when the context is at or over LIMIT.
cycle() {
  wait_idle
  guard || return 1
  log "sending memory checkpoint"
  herdr agent prompt "$TARGET" "$PROMPT" >/dev/null || { log "prompt failed; retry next interval"; return 1; }
  herdr agent wait "$TARGET" --until working --timeout 30000 >/dev/null 2>&1
  wait_idle
  tokens=$(context_tokens "$1")
  log "memory checkpoint done; context=${tokens:-?}"
  if [ -n "$tokens" ] && [ "$tokens" -ge "$LIMIT" ]; then
    guard || return 1
    log "sending $COMPACT"
    herdr agent prompt "$TARGET" "$COMPACT" >/dev/null
    herdr agent wait "$TARGET" --until working --timeout 30000 >/dev/null 2>&1
    wait_idle
    log "compact done; context=$(context_tokens "$1")"
  fi
}

if [ "$1" = --probe ]; then
  transcript; f=$TRANSCRIPT_FILE
  echo "transcript: $TRANSCRIPT_WHY"
  [ -n "$TRANSCRIPT_HINT" ] && echo "hint: $TRANSCRIPT_HINT"
  [ -n "$f" ] && echo "$f context=$(context_tokens "$f")"
  exit 0
fi
if [ "$1" = --once ]; then
  transcript; f=$TRANSCRIPT_FILE
  [ -z "$f" ] && { log "no transcript found"; exit 1; }
  cycle "$f"; exit $?
fi

last_size=0
log "checkpointer on $TARGET: every ${INTERVAL}s, compact at >= $LIMIT tokens"
while true; do
  sleep "$INTERVAL"
  transcript; f=$TRANSCRIPT_FILE
  [ -z "$f" ] && { log "no transcript found; skip"; continue; }
  size=$(file_size "$f")
  if [ "$size" = "$last_size" ]; then log "no activity since last checkpoint; skip"; continue; fi
  cycle "$f" || continue
  last_size=$(file_size "$f")
done
