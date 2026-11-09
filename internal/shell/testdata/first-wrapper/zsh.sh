herdsman() {
  local directive code line target
  directive="$(mktemp "${TMPDIR:-/tmp}/herdsman-directive.XXXXXX")" || return 1
  HERDSMAN_DIRECTIVE_FILE="$directive" command herdsman "$@"
  code=$?
  while IFS= read -r line; do
    case "$line" in /*) target="$line" ;; esac
  done < "$directive"
  rm -f "$directive"
  if [ -n "$target" ] && [ -d "$target" ]; then
    cd -- "$target" || return 1
  fi
  return "$code"
}
