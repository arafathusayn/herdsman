#!/bin/bash
# Read-only audit of the current coordinator session: <claude|codex> <transcript> <session-id> [allowed-write-root/ ...]
# It reads tool-call records only, never prose, and changes nothing. Exit 0: clean; 1: flagged calls; 2: audit gap.
# A gap (missing, unsupported, malformed or wrong-session transcript) is never a clean audit.
# AUDIT_GRANT_FILE names exact full command lines that an administrative grant permits; no other line is exempt.
# AUDIT_CHANNEL_DIR is the installed script folder: only its channel scripts, by exact path, may run as scripts.
# Limit: the audit reads command text. It cannot see inside an allowed channel script; those are the reviewed scripts.
set -eu
set -o pipefail
export LC_ALL=C
gap() { printf 'GAP %s\n' "$*"; exit 2; }
[ $# -ge 3 ] || gap 'usage: transcript-audit.sh claude|codex <transcript> <session-id> [allowed-write-root/ ...]'
H=$1 F=$2 SID=$3; shift 3
command -v jq >/dev/null || gap 'missing jq'
case "$H" in claude|codex) ;; *) gap "no transcript adapter for harness: $H" ;; esac
case "$F" in /*) ;; *) gap 'transcript path must be absolute' ;; esac
[ -f "$F" ] && [ ! -L "$F" ] && [ -r "$F" ] || gap 'transcript missing or not a regular file'
[[ "$SID" =~ ^[A-Za-z0-9_-]+$ ]] || gap 'invalid session id'
for r in "$@"; do case "$r" in /*/) ;; *) gap "allowed write root must be absolute and end in /: $r" ;; esac; done
case "${AUDIT_CHANNEL_DIR:-/}" in /*) ;; *) gap 'AUDIT_CHANNEL_DIR must be absolute' ;; esac
GRANTS=${AUDIT_GRANT_FILE:-}
if [ -n "$GRANTS" ]; then [ -f "$GRANTS" ] && [ ! -L "$GRANTS" ] && [ -r "$GRANTS" ] || gap 'grant file unavailable'; fi
jq -c empty "$F" >/dev/null 2>&1 || gap 'malformed transcript record'

# The transcript must belong to exactly the named session.
if [ "$H" = claude ]; then
  sessions=$(jq -r 'select(type=="object" and has("sessionId")) | .sessionId | tostring' "$F" | sort -u)
else
  sessions=$(jq -r 'select(type=="object" and .type=="session_meta") | .payload.id // "" | tostring' "$F" | sort -u)
fi
[ -n "$sessions" ] || gap 'no session record in transcript'
[ "$sessions" = "$SID" ] || gap 'transcript is not the named current session'

# One row per tool call (and per patched path): tool, call id, path, command, split by the unit separator.
if [ "$H" = claude ]; then
  rows=$(jq -r 'select(type=="object" and .type=="assistant") | (.message.content? // []) |
    if type=="array" then .[] else empty end | select(type=="object" and .type=="tool_use") |
    (.input // {}) as $i |
    [(.name // "?" | tostring), (.id // "-" | tostring),
     (($i.file_path // $i.notebook_path // "") | tostring),
     (if ($i.command|type)=="string" then $i.command else "" end)] |
    map(gsub("[\t\n\r\u001f]"; " ")) | join("\u001f")' "$F")
else
  rows=$(jq -r 'def patch($n; $id; $p):
      ($p | tostring | [scan("\\*\\*\\* (?:Add File|Update File|Delete File|Move to): ([^\n]*)")[0]]) as $ps |
      (if ($ps|length)==0 then [""] else $ps end)[] | [$n, $id, ., ""];
    select(type=="object" and .type=="response_item") | .payload | select(type=="object") |
    (.call_id // "-" | tostring) as $id |
    if .type=="function_call" then
      ((.arguments // "{}") | tostring | (try fromjson catch null)) as $a |
      if ($a|type)!="object" then ["!malformed", $id, "", ""]
      elif .name=="apply_patch" then patch("apply_patch"; $id; ($a.input // $a.patch // ""))
      else [(.name // "?" | tostring), $id, "",
        (if ($a.command|type)=="array" then ($a.command|map(tostring)|join(" "))
         elif ($a.command|type)=="string" then $a.command
         elif ($a.cmd|type)=="string" then $a.cmd else "" end)] end
    elif .type=="custom_tool_call" then patch((.name // "?" | tostring); $id; (.input // ""))
    elif .type=="local_shell_call" then ["local_shell", $id, "",
      (.action.command // [] | if type=="array" then map(tostring)|join(" ") else tostring end)]
    else empty end | map(gsub("[\t\n\r\u001f]"; " ")) | join("\u001f")' "$F")
fi

B='(^|[;&|({[:space:]])'
E='([[:space:]]|$|[;&|)])'
N='[^;&|]*'
GIT="${B}git[[:space:]](${N}[[:space:]])?(commit|push)$E"
GHAPI="${B}gh[[:space:]]+api[[:space:]]${N}((-X|--method)[[:space:]=]*(POST|PATCH|PUT|DELETE)|[[:space:]](-f|-F|--field|--raw-field|--input)[[:space:]=])"
GHW="${B}gh[[:space:]](${N}[[:space:]])?(create|edit|merge|close|reopen|comment|review|delete|ready|rerun|cancel|set|fork|upload|lock|transfer|develop)$E"
CURL="${B}(curl|wget)[[:space:]]${N}((-X|--request|--method)[[:space:]=]*(POST|PATCH|PUT|DELETE)|[[:space:]](-d|--data[a-z-]*|-F|--form|-T|--upload-file|--post-data|--post-file)[[:space:]=])"
RUN="${B}(npm|npx|pnpm|pnpx|yarn|bun|bunx|deno|node|pip|pip3|uv|uvx|poetry|cargo|go|make|gradle|gradlew|mvn|pytest|jest|vitest|tox|rspec|bundle|composer|mix|dotnet|xcodebuild)$E"
TESTS="${B}([^[:space:]]*/)?[A-Za-z0-9_.-]*tests?[.](sh|py)$E"
SVC="${B}(docker|docker-compose|podman|nerdctl|colima|kubectl|helm|systemctl|launchctl|service|terraform|pulumi|wrangler|railway|vercel|flyctl|brew[[:space:]]+services)$E"
MCPW='(send|create|update|delete|post|comment|write|add|edit|merge|upload|remove|move|schedule|react|approve|publish|set_|_set|assign|archive|attach|start|stop)'
shell_class() { # command -> "class program" (the program word only, never arguments), or nothing
  local c
  if [[ "$1" =~ $GIT ]]; then c='git-write'
  elif [[ "$1" =~ $GHAPI ]] || [[ "$1" =~ $GHW ]] || [[ "$1" =~ $CURL ]]; then c='outward-write'
  elif [[ "$1" =~ $RUN ]] || [[ "$1" =~ $TESTS ]]; then c='test-or-package'
  elif [[ "$1" =~ $SVC ]]; then c='service-or-container'
  else script_run "$1"; return 0; fi
  printf '%s %s\n' "$c" "$(printf '%s' "${BASH_REMATCH[0]}" | sed 's/^[;&|({[:space:]]*//' | awk '{print $1}')"
}
# Command text cannot show what a script does, so every script or interpreter run is not clean unless it starts
# one of the channel scripts by its exact path under AUDIT_CHANNEL_DIR (the installed script folder).
CHANNEL='message.sh message-register.sh message-send.sh message-read.sh message-ack.sh message-status.sh
  message-wait.sh message-authority.sh publish-leak-gate.sh'
channel_script() {
  local n
  [ -n "${AUDIT_CHANNEL_DIR:-}" ] || return 1
  for n in $CHANNEL; do [ "$1" = "${AUDIT_CHANNEL_DIR%/}/$n" ] && return 0; done
  return 1
}
script_run() { # command -> "script-run <program>" for the first script or interpreter run that is not allowed
  local seg w i first
  local -a words
  while IFS= read -r seg; do
    read -r -a words <<< "$(printf '%s' "$seg" | tr -d "\"'")" || true
    i=0
    while [ "$i" -lt "${#words[@]}" ]; do
      case "${words[$i]}" in env|exec|nohup|time|command|builtin|sudo|nice|timeout|*=*) i=$((i+1)) ;; *) break ;; esac
    done
    [ "$i" -lt "${#words[@]}" ] || continue
    first=${words[$i]}
    case "${first##*/}" in
      bash|sh|zsh|dash|ksh|fish|python|python2|python3|python3.*|perl|ruby|node|deno|bun|php|lua|osascript|tclsh|awk|gawk|mawk)
        w=
        for ((i=i+1; i<${#words[@]}; i++)); do case "${words[$i]}" in -*) ;; *) w=${words[$i]}; break ;; esac; done
        case "${first##*/}" in awk|gawk|mawk) [[ " ${words[*]} " =~ \ -f\  ]] || continue ;; esac
        if [ -n "$w" ] && channel_script "$w" && [[ ! " ${words[*]} " =~ \ -c\  ]]; then continue; fi
        printf 'script-run %s\n' "${first##*/}"; return 0 ;;
      eval|source|.|xargs)
        printf 'script-run %s\n' "$first"; return 0 ;;
      */*|*.sh|*.py|*.pl|*.rb|*.js)
        channel_script "$first" && continue
        printf 'script-run %s\n' "${first##*/}"; return 0 ;;
    esac
  done < <(printf '%s\n' "$1" | awk '{ gsub(/[$]\(|[;&|(){}`]/, "\n"); print }')
}
granted() { [ -n "$GRANTS" ] && grep -Fxq -- "$1" "$GRANTS"; }
in_root() {
  local p=$1 r
  case "$p" in /*) ;; *) return 1 ;; esac
  case "$p" in */../*|*/./*|*/..|*/.) return 1 ;; esac
  for r in "${ROOTS[@]+"${ROOTS[@]}"}"; do case "$p" in "$r"*) return 0 ;; esac; done
  return 1
}
ROOTS=("$@")
calls=0 flagged=0
while IFS=$'\x1f' read -r tool id path cmd; do
  [ -n "$tool" ] || continue
  calls=$((calls+1)); class=''; target=-
  case "$tool" in
    '!malformed') gap "malformed tool-call arguments: call $id" ;;
    Write|Edit|MultiEdit|NotebookEdit|apply_patch|edit_file|write_file)
      in_root "$path" || { class='project-write'; target=".../${path##*/}"; } ;;
    Bash|shell|exec_command|local_shell|shell_command|container.exec)
      class=$(shell_class "$cmd")
      if [ -n "$class" ] && granted "$cmd"; then class=; fi
      if [ -n "$class" ]; then target=${class#* }; class=${class%% *}; fi ;;
    mcp__*)
      if [[ "$(printf '%s' "$tool" | tr 'A-Z' 'a-z')" =~ $MCPW ]]; then
        class='tracker-or-chat-write'
      fi ;;
  esac
  if [ -n "$class" ]; then
    flagged=$((flagged+1))
    printf 'FLAG tool=%s call=%s class=%s target=%s\n' "$tool" "$id" "$class" "$target"
  fi
done <<< "$rows"
if [ "$flagged" -gt 0 ]; then
  printf 'AUDIT flagged=%s calls=%s: report each flagged call to the human and stop work\n' "$flagged" "$calls"
  exit 1
fi
printf 'AUDIT clean calls=%s session=%s\n' "$calls" "$SID"
