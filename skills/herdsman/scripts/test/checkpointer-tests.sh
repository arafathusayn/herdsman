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
[ "$1 $2" = "agent get" ] && printf '{"result":{"agent":{"agent":"%s","agent_session":{"kind":"%s","value":"%s"}}}}\n' \
  "${FAKE_AGENT:-claude}" "${FAKE_KIND:-id}" "${FAKE_SID:-fake}"
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

# Codex: rollouts live under $CODEX_HOME/sessions/YYYY/MM/DD/rollout-<time>-<session id>.jsonl, and the context is
# the input tokens of the last token_count event that carries usage (a later one can carry none).
tc() { printf '{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%s,"cached_input_tokens":0,"output_tokens":7,"reasoning_output_tokens":0,"total_tokens":%s},"total_token_usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"model_context_window":400000}}}\n' "$1" "$(($1 + 7))"; }
CX="$T/codex/sessions/2000/01/02"; mkdir -p "$CX"
SID=0a1b2c3d-0000-4000-8000-000000000001
{ echo '{"type":"session_meta","payload":{"id":"'"$SID"'"}}'; tc 1000; tc 300100
  echo '{"type":"event_msg","payload":{"type":"token_count","info":null}}'; } > "$CX/rollout-2000-01-02T01-02-03-$SID.jsonl"
crun() { run FAKE_AGENT=codex FAKE_SID="$SID" CODEX_HOME="$T/codex" "$@"; }
out=$(env CALLS="$T/calls" PATH="$T/bin:$PATH" HOME="$T/home" CODEX_HOME="$T/codex" FAKE_AGENT=codex FAKE_SID="$SID" TARGET=w9:p9 /bin/bash "$S" --probe)
check "codex probe finds the rollout of the session id" "$(printf '%s\n' "$out" | grep -c "rollout-2000-01-02T01-02-03-$SID.jsonl context=")" "1"
check "codex probe reads the last token_count with usage" "${out##*context=}" "300100"
check "codex probe says why" "$(printf '%s\n' "$out" | grep -c "transcript: the Codex rollout of the session id that Herdr reports ('$SID')")" "1"
crun; check "codex over limit: memory prompt without the slash command, then /compact" "$(prompts)" "Capture all learning|/compact|"
crun LIMIT=400000; check "codex under limit: memory prompt only" "$(prompts)" "Capture all learning|"
crun PROMPT=/save-now; check "codex with PROMPT set: sent as is" "$(prompts)" "/save-now|/compact|"
run FAKE_AGENT=codex FAKE_SID=missing CODEX_HOME="$T/codex"; rc=$?
check "codex with no rollout for the id: exit 1" "$rc" "1"
check "and no prompt is sent" "$(prompts)" ""
# Codex without CODEX_HOME looks under ~/.codex.
mkdir -p "$T/home3/.codex/sessions/2000/01/01"; { tc 60000; } > "$T/home3/.codex/sessions/2000/01/01/rollout-2000-01-01T09-00-00-$SID.jsonl"
out=$(env CALLS="$T/calls" PATH="$T/bin:$PATH" HOME="$T/home3" FAKE_AGENT=codex FAKE_SID="$SID" TARGET=w9:p9 /bin/bash "$S" --probe)
check "codex without CODEX_HOME reads ~/.codex/sessions" "${out##*context=}" "60000"
# A session reported as a path is read from that path.
out=$(env CALLS="$T/calls" PATH="$T/bin:$PATH" HOME="$T/home" FAKE_AGENT=codex FAKE_KIND=path FAKE_SID="$CX/rollout-2000-01-02T01-02-03-$SID.jsonl" TARGET=w9:p9 /bin/bash "$S" --probe)
check "codex session reported as a path" "${out##*context=}" "300100"
# TRANSCRIPT naming a Codex rollout works for a Claude-reported pane too: the record shape picks the reader.
out=$(env CALLS="$T/calls" PATH="$T/bin:$PATH" HOME="$T/home" TRANSCRIPT="$CX/rollout-2000-01-02T01-02-03-$SID.jsonl" TARGET=w9:p9 /bin/bash "$S" --probe)
check "TRANSCRIPT names a Codex rollout: its context is read" "${out##*context=}" "300100"

echo "RESULT pass=$pass fail=$fail (work dir $T)"
[ "$fail" = 0 ]
