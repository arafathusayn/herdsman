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
# A Codex target has no /memory-with-dag command: unless PROMPT is set, it gets the same text without the leading command.
PROMPT_SET=${PROMPT:+1}
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
# A Codex pane (Herdr reports agent "codex") keeps its session as a rollout file named after the session id under
# ${CODEX_HOME:-~/.codex}/sessions/YYYY/MM/DD/ (older Codex versions wrote them straight into sessions/); a session
# reported as a path is that file. Codex has no folder per project, so it gives no hint.
# It sets TARGET_AGENT (the harness Herdr reports), TRANSCRIPT_FILE (empty when none), TRANSCRIPT_WHY (the reason; --probe prints it) and TRANSCRIPT_HINT.
transcript() {
  local info sid kind named dir newest
  TRANSCRIPT_FILE=""; TRANSCRIPT_HINT=""
  info=$(herdr agent get "$TARGET" 2>/dev/null)
  TARGET_AGENT=$(printf '%s' "$info" | jq -r '.result.agent.agent // empty' 2>/dev/null)
  if [ -n "${TRANSCRIPT:-}" ]; then
    if [ -f "$TRANSCRIPT" ]; then
      TRANSCRIPT_FILE=$TRANSCRIPT
      TRANSCRIPT_WHY="TRANSCRIPT is set; the file is used whatever the session id says"
    else
      TRANSCRIPT_WHY="TRANSCRIPT is set but the file does not exist: $TRANSCRIPT"
    fi
    return
  fi
  sid=$(printf '%s' "$info" | jq -r '.result.agent.agent_session.value' 2>/dev/null)
  if [ "$TARGET_AGENT" = codex ]; then
    kind=$(printf '%s' "$info" | jq -r '.result.agent.agent_session.kind // "id"' 2>/dev/null)
    dir="${CODEX_HOME:-$HOME/.codex}/sessions"
    if [ "$kind" = path ]; then named=$sid
    else named=$(ls -t "$dir"/*/*/*/rollout-*-"$sid".jsonl "$dir"/rollout-*-"$sid".jsonl 2>/dev/null | head -1); fi
    if [ -n "$sid" ] && [ -f "$named" ]; then
      TRANSCRIPT_FILE=$named
      TRANSCRIPT_WHY="the Codex rollout of the session id that Herdr reports ('$sid')"
    else
      TRANSCRIPT_WHY="no Codex rollout for session id '$sid' under $dir"
    fi
    return
  fi
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

# Claude Code: context = input + cache creation + cache read of the last main-thread assistant turn.
# Codex: context = input tokens (cached input included) of the last token_count event that carries usage.
# The record shape picks the reader, so TRANSCRIPT can name either kind of file.
context_tokens() {
  tail -n 400 "$1" | jq -r 'if .type=="assistant" and .message.usage and (.isSidechain|not) then
      .message.usage | (.input_tokens + (.cache_creation_input_tokens//0) + (.cache_read_input_tokens//0))
    elif .type=="event_msg" and .payload.type=="token_count" and .payload.info.last_token_usage then
      .payload.info.last_token_usage.input_tokens
    else empty end' 2>/dev/null | tail -1
}

# The memory prompt for the target: Codex gets the default without its leading slash command.
memory_prompt() {
  if [ "$TARGET_AGENT" = codex ] && [ -z "$PROMPT_SET" ]; then printf '%s' "${DEFAULT_PROMPT#/* }"
  else printf '%s' "$PROMPT"; fi
}

# Ordinary mode has no guard. In adjutant mode the target must still be the approved orchestrator, never the adjutant.
guard() {
  local out
  [ -z "${HERDSMAN_ORCHESTRATOR:-}" ] && return 0
  out=$(/bin/bash "$(dirname "$0")/checkpointer-guard.sh" "$HERDSMAN_ORCHESTRATOR" "$TARGET" 2>&1) && return 0
  log "identity check failed, no input sent: $out"; return 1
}

# In adjutant mode each identity check and its input run under the shared input lock of the private inbox, so they
# never interleave with a goal resume, a hold or an admission. A busy lock sends nothing; it is never removed.
LOCK_WAIT=${LOCK_WAIT:-6}; INPUT_OWNED=
input_lock() {
  local tries=0
  [ -n "${HERDSMAN_ORCHESTRATOR:-}" ] || return 0
  while ! mkdir "${HERDSMAN_INBOX:?set HERDSMAN_INBOX in adjutant mode}/herdr-mutation.lock" 2>/dev/null; do
    tries=$((tries+1)); [ "$tries" -lt "$LOCK_WAIT" ] || { log "input lock busy, no input sent"; return 1; }; sleep 1
  done
  INPUT_OWNED=1; printf '%s\n' "$$" > "$HERDSMAN_INBOX/herdr-mutation.lock/pid"
}
input_unlock() {
  [ -n "$INPUT_OWNED" ] || return 0
  rm -f "$HERDSMAN_INBOX/herdr-mutation.lock/pid"; rmdir "$HERDSMAN_INBOX/herdr-mutation.lock"; INPUT_OWNED=
}
trap input_unlock EXIT

# Blocked means a permission dialog; typing into it would answer it, so wait for idle or done only.
wait_idle() { herdr agent wait "$TARGET" --until idle --until 'done' >/dev/null; }

# One checkpoint: memory prompt, then compaction when the context is at or over LIMIT.
cycle() {
  wait_idle
  input_lock || return 1
  guard || { input_unlock; return 1; }
  log "sending memory checkpoint"
  herdr agent prompt "$TARGET" "$(memory_prompt)" >/dev/null || { input_unlock; log "prompt failed; retry next interval"; return 1; }
  input_unlock
  herdr agent wait "$TARGET" --until working --timeout 30000 >/dev/null 2>&1
  wait_idle
  tokens=$(context_tokens "$1")
  log "memory checkpoint done; context=${tokens:-?}"
  if [ -n "$tokens" ] && [ "$tokens" -ge "$LIMIT" ]; then
    input_lock || return 1
    guard || { input_unlock; return 1; }
    log "sending $COMPACT"
    herdr agent prompt "$TARGET" "$COMPACT" >/dev/null || :
    input_unlock
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
