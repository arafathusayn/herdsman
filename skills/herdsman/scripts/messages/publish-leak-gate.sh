#!/bin/bash
# Check only: <file> [exact-required-heading ...]. Manual semantic review remains.
set -eu
set -o pipefail
[ $# -ge 1 ] || { echo 'usage: publish-leak-gate.sh <file> [required heading ...]' >&2; exit 2; }
input=$1; shift
case "$input" in /*) ;; *) input="./$input" ;; esac
[ -f "$input" ] && [ ! -L "$input" ] && [ -r "$input" ] || { echo 'cannot read regular input' >&2; exit 2; }
pattern='/Users/|/home/|/private/|/tmp/|/var/|/opt/|/Volumes/|~/|\$HOME|\$\{HOME\}|herdsman|herdr|run-check|claude|codex|gpt[- ]|opus|sonnet|(^|[^[:alnum:]_])(im|rv|int)-[[:alnum:]_-]+|[[:alnum:]_]+=|(^|[[:space:](])/[[:alnum:]_.-]+/'
scan() {
  local result rc=0 line allowed
  result=$(grep -inE -e "$1" -- "$input") || rc=$?
  case "$rc" in
    0)
      while IFS= read -r line; do
        allowed=
        if [ -n "${LEAK_ALLOW_FILE:-}" ]; then
          [ -f "$LEAK_ALLOW_FILE" ] && [ ! -L "$LEAK_ALLOW_FILE" ] && [ -r "$LEAK_ALLOW_FILE" ] || { echo 'invalid exact-line allow list' >&2; exit 2; }
          grep -Fx -- "${line#*:}" "$LEAK_ALLOW_FILE" >/dev/null && allowed=1
        fi
        [ -n "$allowed" ] || { printf 'LEAK: preserve content while removing private wrappers:\n%s\n' "$line"; exit 1; }
      done < <(printf '%s\n' "$result") ;;
    1) ;;
    *) echo 'pattern or input error' >&2; exit 2 ;;
  esac
}
scan "$pattern"
[ -z "${LEAK_PATTERN:-}" ] || scan "$LEAK_PATTERN"
check_structure() {
  awk -v wanted="$1" '
    { s=$0; sub(/^[[:space:]]*/,"",s)
      if (s ~ /^```/ || s ~ /^~~~/) {
        c=substr(s,1,1); n=0; while(substr(s,n+1,1)==c) n++
        if (!opened) { opened=1; mark=c; width=n; content=0; next }
        rest=substr(s,n+1)
        if (c==mark && n>=width && rest ~ /^[[:space:]]*$/) {
          if (!content) bad=1; opened=0; next
        }
      }
      # A required section keeps its child headings; a heading of the same or a higher level ends it.
      if (!opened && /^#/) {
        level=match($0,/[^#]/)-1; if (level<1) level=length($0)
        if (active && level>wanted_level) next
        active=($0==wanted); if(active) { seen=1; wanted_level=level }; next }
      if (NF) { if(opened) content=1; if(active) body=1 }
    }
    END { exit (opened || bad || (wanted!="" && !(seen && body))) }' "$input"
}
check_structure '' || { echo 'EMPTY, incomplete, or unreadable code fence: restore content' >&2; exit 1; }
for heading in "$@"; do
  check_structure "$heading" || { echo "missing or empty required section: $heading" >&2; exit 1; }
done
echo 'pattern and structure checks passed; manual policy/read-back review required'
