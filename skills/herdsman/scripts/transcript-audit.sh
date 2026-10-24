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
# Mutation options count at the start of the argument list too, in spaced, equal and compact forms.
GHAPI="${B}gh[[:space:]]+api[[:space:]](${N}[[:space:]])?((-X|--method)[[:space:]=]*(POST|PATCH|PUT|DELETE)|(-f|-F|--field|--raw-field|--input)([[:space:]=]|$)|-[fF][^[:space:]])"
GHW="${B}gh[[:space:]](${N}[[:space:]])?(create|edit|merge|close|reopen|comment|review|delete|ready|rerun|cancel|set|fork|upload|lock|transfer|develop)$E"
# A short-option cluster counts when it holds a data, form or upload option, or ends in -X with a write method.
CURL="${B}(curl|wget)[[:space:]](${N}[[:space:]])?(-[a-zA-Z]*X[[:space:]=]*(POST|PATCH|PUT|DELETE)|(--request|--method)[[:space:]=]*(POST|PATCH|PUT|DELETE)|(--data[a-z-]*|--json|--form[a-z-]*|--upload-file|--post-data|--post-file)([[:space:]=]|$)|-[a-zA-Z]*[dFT])"
# An awk program that can run commands or write files is a script run; plain text processing is not.
AWKRUN="${B}(awk|gawk|mawk|nawk)[[:space:]]"
AWKFX='system[[:space:]]*[(]|getline|print[^;}]*[>|]'
# Shell file mutations. Their targets are not parsed, so they are flagged wherever they point.
WRITECMD="${B}(cp|mv|rm|mkdir|rmdir|touch|ln|chmod|chown|tee|install|rsync|truncate|dd|patch|unzip|tar)[[:space:]]"
SEDI="${B}(sed|perl)[[:space:]](${N}[[:space:]])?-[a-zA-Z]*i"
RUN="${B}(npm|npx|pnpm|pnpx|yarn|bun|bunx|deno|node|pip|pip3|uv|uvx|poetry|cargo|go|make|gradle|gradlew|mvn|pytest|jest|vitest|tox|rspec|bundle|composer|mix|dotnet|xcodebuild)$E"
TESTS="${B}([^[:space:]]*/)?[A-Za-z0-9_.-]*tests?[.](sh|py)$E"
SVC="${B}(docker|docker-compose|podman|nerdctl|colima|kubectl|helm|systemctl|launchctl|service|terraform|pulumi|wrangler|railway|vercel|flyctl|brew[[:space:]]+services)$E"
MCPW='(send|create|update|delete|post|comment|write|add|edit|merge|upload|remove|move|schedule|react|approve|publish|set_|_set|assign|archive|attach|start|stop)'
# A program started from a system command folder is classified by its name; quotes never hide a program name.
SYSDIR='(/usr/local/bin|/opt/homebrew/bin|/usr/bin|/usr/sbin|/bin|/sbin)'
normalized() { printf '%s' "$1" | tr -d "\"'" | sed -E "s#(^|[;&|({[:space:]])$SYSDIR/#\1#g"; }
shell_class() { # command -> "class program" (the program word only, never arguments), or nothing
  local c n
  n=$(normalized "$1")
  if [[ "$n" =~ $GIT ]]; then c='git-write'
  elif [[ "$n" =~ $GHAPI ]] || [[ "$n" =~ $GHW ]] || [[ "$n" =~ $CURL ]]; then c='outward-write'
  elif [[ "$n" =~ $RUN ]] || [[ "$n" =~ $TESTS ]]; then c='test-or-package'
  elif [[ "$n" =~ $SVC ]]; then c='service-or-container'
  elif [[ "$n" =~ $AWKRUN ]] && [[ "$n" =~ $AWKFX ]]; then printf 'script-run awk\n'; return 0
  elif shell_write "$1"; then return 0
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
  local seg w i first prog wrapped
  local -a words
  while IFS= read -r seg; do
    read -r -a words <<< "$(printf '%s' "$seg" | tr -d "\"'")" || true
    i=0; wrapped=
    # A wrapper with an option or a count hides what it starts: flag it instead of guessing its argument grammar.
    while [ "$i" -lt "${#words[@]}" ]; do
      case "${words[$i]}" in
        env|exec|nohup|time|command|builtin|sudo|nice|timeout|doas|stdbuf|ionice|chroot|setsid) wrapped=${words[$i]}; i=$((i+1)) ;;
        *=*) i=$((i+1)) ;;
        -*|[0-9]*) [ -z "$wrapped" ] || { printf 'script-run %s\n' "$wrapped"; return 0; }; break ;;
        *) break ;;
      esac
    done
    [ "$i" -lt "${#words[@]}" ] || continue
    first=${words[$i]} prog=${words[$i]}
    # A program from a system command folder runs by its name; any other path is a script run.
    if [[ "$first" =~ ^$SYSDIR/([^/]+)$ ]]; then prog=${BASH_REMATCH[2]}; fi
    case "$prog" in
      bash|sh|zsh|dash|ksh|fish|python|python2|python3|python3.*|perl|ruby|node|deno|bun|php|lua|osascript|tclsh|awk|gawk|mawk)
        w=
        for ((i=i+1; i<${#words[@]}; i++)); do case "${words[$i]}" in -*) ;; *) w=${words[$i]}; break ;; esac; done
        case "$prog" in awk|gawk|mawk) [[ " ${words[*]} " =~ \ -f\  ]] || continue ;; esac
        if [ -n "$w" ] && channel_script "$w" && [[ ! " ${words[*]} " =~ \ -c\  ]]; then continue; fi
        printf 'script-run %s\n' "$prog"; return 0 ;;
      eval|source|.|xargs)
        printf 'script-run %s\n' "$prog"; return 0 ;;
      */*|*.sh|*.py|*.pl|*.rb|*.js)
        channel_script "$first" && continue
        printf 'script-run %s\n' "${prog##*/}"; return 0 ;;
    esac
  # Single-quoted text runs nothing in the shell (an awk program, a pattern), so a quoted text with spaces or shell
  # punctuation never starts a segment. A quoted single word, such as a script path, stays.
  done < <(printf '%s\n' "$1" | sed "s/'[^' ]*[ {};|&()][^']*'/Q/g" | awk '{ gsub(/[$]\(|[;&|(){}`]/, "\n"); print }')
}
shell_write() { # command -> "project-write <target>" for a redirect outside the allowed roots or a file mutation
  local rest t op n
  rest=$(printf '%s' "$1" | tr -d "\"'")
  # >, >>, >| and >&: a >& followed by a descriptor number or - copies a stream; any other target is a file.
  while [[ "$rest" =~ \>(\>|\||\&)?[[:space:]]*([^[:space:]\;\&\|\(\)]+) ]]; do
    op=${BASH_REMATCH[1]} t=${BASH_REMATCH[2]}; rest=${rest#*"${BASH_REMATCH[0]}"}
    if [ "$op" = '&' ]; then case "$t" in -|[0-9]|[0-9][0-9]) continue ;; esac; fi
    case "$t" in /dev/null|/dev/stdout|/dev/stderr|/dev/tty) continue ;; esac
    in_root "$t" || { printf 'project-write .../%s\n' "${t##*/}"; return 0; }
  done
  n=$(normalized "$1")
  if [[ "$n" =~ $WRITECMD ]] || [[ "$n" =~ $SEDI ]]; then
    printf 'project-write %s\n' "$(printf '%s' "${BASH_REMATCH[0]}" | sed 's/^[;&|({[:space:]]*//' | awk '{print $1}')"; return 0
  fi
  return 1
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
    Read|Grep|Glob|LS|NotebookRead|WebFetch|WebSearch|TodoWrite|ToolSearch|AskUserQuestion|Skill|BashOutput|TaskOutput|update_plan|view_image) ;;
    Bash|shell|exec_command|local_shell|shell_command|container.exec)
      [ -n "$cmd" ] || gap "malformed tool-call arguments: call $id"
      class=$(shell_class "$cmd")
      if [ -n "$class" ] && granted "$cmd"; then class=; fi
      if [ -n "$class" ]; then target=${class#* }; class=${class%% *}; fi ;;
    mcp__*)
      if [[ "$(printf '%s' "$tool" | tr 'A-Z' 'a-z')" =~ $MCPW ]]; then
        class='tracker-or-chat-write'
      elif ! [[ "_$(printf '%s' "${tool##*__}" | tr 'A-Z' 'a-z')_" =~ _(get|list|read|search|query|fetch|view|find|describe|show|count|lookup|retrieve|status|info)_ ]]; then
        gap "unsupported tool $tool (call $id)"
      fi ;;
    # Every other record is a tool this audit cannot interpret, so it is never clean.
    *) gap "unsupported tool $tool (call $id)" ;;
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
