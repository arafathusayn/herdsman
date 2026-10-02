#!/bin/bash
# Tests for scripts/checkpointer.sh --once and --probe with a fake herdr and a fake transcript.
# Run from bash or zsh: zsh <skill>/scripts/test/checkpointer-tests.sh (or /bin/bash, sh, ./checkpointer-tests.sh)
# Started from zsh or sh, re-run under /bin/bash (the line is valid in all three).
[ -n "${BASH_VERSION:-}" ] || exec /bin/bash "$0" "$@"
S="$(cd "$(dirname "$0")/.." && pwd)/checkpointer.sh"
T=$(mktemp -d "${TMPDIR:-/tmp}/herdsman-cp-test-XXXXXX")
mkdir -p "$T/bin" "$T/home/.claude/projects/p"
cat > "$T/bin/herdr" <<'EOF'
#!/bin/bash
echo "$*" >> "$CALLS"
[ "$1 $2" = "agent get" ] && echo '{"result":{"agent":{"agent_session":{"value":"fake"}}}}'
exit 0
EOF
chmod +x "$T/bin/herdr"
# Context of the last main-thread turn: 10 + 90 + 300000 = 300100 tokens.
echo '{"type":"assistant","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":90,"cache_read_input_tokens":300000}}}' > "$T/home/.claude/projects/p/fake.jsonl"

pass=0; fail=0
check() { if [ "$2" = "$3" ]; then echo "PASS $1"; pass=$((pass+1)); else echo "FAIL $1: got [$2] want [$3]"; fail=$((fail+1)); fi; }
run() { : > "$T/calls"; env CALLS="$T/calls" PATH="$T/bin:$PATH" HOME="$T/home" LOG="$T/log" TARGET=w9:p9 "$@" /bin/bash "$S" --once > /dev/null; }
prompts() { grep '^agent prompt' "$T/calls" | sed 's/^agent prompt w9:p9 //' | cut -c1-20 | tr '\n' '|'; }

bash -n "$S"; check "syntax" "$?" "0"

run; check "over limit: memory prompt then /compact" "$(prompts)" "/memory-with-dag Cap|/compact|"
run LIMIT=400000; check "under limit: memory prompt only" "$(prompts)" "/memory-with-dag Cap|"
run PROMPT=/save-now COMPACT=/squash; check "custom prompt and compact command" "$(prompts)" "/save-now|/squash|"
run LIMIT=400000; check "waits for idle or done, never blocked" "$(grep -c -- '--until idle --until done' "$T/calls")" "2"

out=$(env CALLS="$T/calls" PATH="$T/bin:$PATH" HOME="$T/home" TARGET=w9:p9 /bin/bash "$S" --probe)
check "probe reads context" "${out##*context=}" "300100"

# The pane-run form from zsh, the macOS login shell: a quoted value with a space reaches the script whole.
if command -v zsh >/dev/null 2>&1; then
  : > "$T/calls"
  env -i CALLS="$T/calls" PATH="$T/bin:$PATH" HOME="$T/home" LOG="$T/log" zsh -f -c "TARGET=w9:p9 PROMPT='/save-memory now' LIMIT=1 /bin/bash '$S' --once" > /dev/null
  check "zsh pane-run form keeps a quoted prompt" "$(prompts)" "/save-memory now|/compact|"
else
  echo "SKIP zsh form: zsh is not installed"
fi

echo "RESULT pass=$pass fail=$fail (work dir $T)"
[ "$fail" = 0 ]
