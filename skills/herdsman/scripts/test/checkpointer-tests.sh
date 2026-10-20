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
[ "$1 $2" = "agent get" ] && printf '{"result":{"agent":{"agent_session":{"value":"%s"}}}}\n' "${FAKE_SID:-fake}"
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

# Another session of the same project folder writes the newest file while the orchestrator waits. The named file
# (session id "fake", 5000 tokens, the oldest) must be read, not the newest file (300100 tokens): no /compact.
P2="$T/home2/.claude/projects/p"; mkdir -p "$P2"
usage() { printf '{"type":"assistant","message":{"usage":{"input_tokens":%s,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}\n' "$2" > "$1"; }
usage "$P2/fake.jsonl" 5000; touch -t 202601010000 "$P2/fake.jsonl"
usage "$P2/mid.jsonl" 6000; touch -t 202601020000 "$P2/mid.jsonl"
usage "$P2/top.jsonl" 300100
run HOME="$T/home2"; check "a newer file of another session: the named file is read, no /compact" "$(prompts)" "/memory-with-dag Cap|"
# TRANSCRIPT names the file to read, whatever the session id says.
run HOME="$T/home2" TRANSCRIPT="$P2/mid.jsonl"; check "TRANSCRIPT names an older file: no /compact from the newest file" "$(prompts)" "/memory-with-dag Cap|"
run HOME="$T/home2" TRANSCRIPT="$P2/top.jsonl"; check "TRANSCRIPT names a file over the limit: /compact" "$(prompts)" "/memory-with-dag Cap|/compact|"
run HOME="$T/home2" TRANSCRIPT="$P2/missing.jsonl"; rc=$?
check "TRANSCRIPT names a missing file: exit 1" "$rc" "1"
check "and no prompt is sent" "$(prompts)" ""
# A session id with no file: no transcript, even when the working folder of the pane holds a file over the limit.
W2="$T/work"; mkdir -p "$W2"
derived=$(cd "$W2" && pwd | sed 's/[^A-Za-z0-9]/-/g')
mkdir -p "$T/home2/.claude/projects/$derived"; usage "$T/home2/.claude/projects/$derived/lone.jsonl" 300100
(cd "$W2" && run HOME="$T/home2" FAKE_SID=gone); rc=$?
check "no file for the id: exit 1, although the working folder holds a file" "$rc" "1"
check "and no prompt is sent from that file" "$(prompts)" ""

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
