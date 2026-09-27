#!/bin/bash
# Sanity tests for the herdsman watcher templates under macOS /bin/bash 3.2.
# Run: /bin/bash ~/.claude/skills/herdsman/scripts/test/run-tests.sh [work dir]
# A `sleep` stub exits the watcher after one pass, so every test is one poll iteration.
# A fake `herdr` on PATH serves pane text from files.
set -u
SK=$(cd "$(dirname "$0")/.." && pwd)
T=${1:-${TMPDIR:-/tmp}/herdsman-test-$$}
export PATH="$SK/test/fakebin:$PATH"
export FAKE_PANE_DIR="$T/panes"
export HERDSMAN_REPORTS="$T/reports"
export HERDSMAN_STATE="$T/state"
export HERDSMAN_WORKERS="wk-a wk-b"
export HERDSMAN_PANE_wk_a=w9:p1
export HERDSMAN_PANE_wk_b=w9:p2
export HERDSMAN_REVIEWER_PANE=w9:p3
export HERDSMAN_REVIEWER_NAME=rv-1
chmod +x "$SK/test/fakebin/herdr"
mkdir -p "$FAKE_PANE_DIR" "$HERDSMAN_REPORTS" "$HERDSMAN_STATE"
sleep() { exit 0; }
export -f sleep
pass=0; fail=0
check() { # name, expected-regex, actual
  if printf '%s' "$3" | grep -q -E "$2"; then pass=$((pass+1)); echo "PASS $1"; else fail=$((fail+1)); echo "FAIL $1: expected /$2/ got: $3"; fi
}
nocheck() { # name, forbidden-regex, actual
  if printf '%s' "$3" | grep -q -E "$2"; then fail=$((fail+1)); echo "FAIL $1: unexpected /$2/ in: $3"; else pass=$((pass+1)); echo "PASS $1"; fi
}
run_w() { /bin/bash "$SK/watch-workers.sh" 2>&1; }
run_r() { /bin/bash "$SK/watch-reviewer.sh" 2>&1; }

echo "bash: $(/bin/bash --version | head -1)"
/bin/bash -n "$SK/watch-workers.sh" && echo "PASS syntax watch-workers" || { echo "FAIL syntax watch-workers"; fail=$((fail+1)); }
/bin/bash -n "$SK/watch-reviewer.sh" && echo "PASS syntax watch-reviewer" || { echo "FAIL syntax watch-reviewer"; fail=$((fail+1)); }

# W1: working screens, first sight: no event
printf '%s\n' "• Working (3m 10s • esc to interrupt)" "› Ask Codex to do anything" > "$FAKE_PANE_DIR/w9:p1.txt"
printf '%s\n' "Pursuing goal (2m)" > "$FAKE_PANE_DIR/w9:p2.txt"
out=$(run_w); nocheck "W1 first working pass is silent" 'STALL|IDLE|BLOCKED|REPORT' "$out"
# W2: same screens again: STALL for both
out=$(run_w); check "W2 stall wk-a" 'STALL wk-a' "$out"; check "W2 stall wk-b" 'STALL wk-b' "$out"
# W3: password prompt: BLOCKED with the matched text
printf '%s\n' "Password for 'https://x@github.com': Device not configured" > "$FAKE_PANE_DIR/w9:p1.txt"
out=$(run_w); check "W3 blocked wk-a" 'BLOCKED wk-a Password for' "$out"
# W4: idle at the prompt with a changed screen: IDLE
printf '%s\n' "› Ask Codex to do anything" "/path/to/report.md" > "$FAKE_PANE_DIR/w9:p2.txt"
out=$(run_w); check "W4 idle wk-b" 'IDLE wk-b' "$out"
# W5: report a appears: REPORT once, never repeated
printf 'STATUS: complete\n' > "$HERDSMAN_REPORTS/a.md"
touch -t 202601010000 "$HERDSMAN_REPORTS/a.md"
out=$(run_w); check "W5 report a" "REPORT a $HERDSMAN_REPORTS/a.md" "$out"
out=$(run_w); nocheck "W5b report a not repeated" 'REPORT a' "$out"
# W6: report a rewritten in a fix round (new mtime): REPORT-UPDATED once
printf 'STATUS: complete\nhead: new\n' > "$HERDSMAN_REPORTS/a.md"
out=$(run_w); check "W6 report a updated" "REPORT-UPDATED a $HERDSMAN_REPORTS/a.md" "$out"
out=$(run_w); nocheck "W6b update not repeated" 'REPORT-UPDATED' "$out"
# W7: a pane with a report is still watched: queued Codex question is BLOCKED, once per screen change
printf '%s\n' "• Queued follow-up inputs" "  ? 1 question" > "$FAKE_PANE_DIR/w9:p1.txt"
out=$(run_w); check "W7 queued question blocked" 'BLOCKED wk-a Queued follow-up inputs' "$out"
out=$(run_w); nocheck "W7b unchanged blocked screen is silent" 'BLOCKED' "$out"
# W8: second report present: REPORT b, and the loop keeps running (exit comes from the sleep stub only)
printf 'STATUS: complete\n' > "$HERDSMAN_REPORTS/b.md"
out=$(run_w); rc=$?; check "W8 report b" 'REPORT b' "$out"; nocheck "W8b no ALL-REPORTS exit" 'ALL-REPORTS' "$out"

# R1: reviewer working (Claude markers): silent
printf '%s\n' "· Incubating… (10s · thinking with xhigh effort)" > "$FAKE_PANE_DIR/w9:p3.txt"
out=$(run_r); nocheck "R1 working is silent" 'IDLE|BLOCKED|REVIEW' "$out"
# R2: permission prompt: BLOCKED
printf '%s\n' "Allow reads outside the working directories?" > "$FAKE_PANE_DIR/w9:p3.txt"
out=$(run_r); check "R2 blocked" 'BLOCKED rv-1 Allow reads outside' "$out"
# R3: idle with a changed screen: IDLE
printf '%s\n' "❯" "/path/review-31-1.md" > "$FAKE_PANE_DIR/w9:p3.txt"
out=$(run_r); check "R3 idle" 'IDLE rv-1' "$out"
# R4: same idle screen again: silent
out=$(run_r); nocheck "R4 unchanged idle is silent" 'IDLE' "$out"
# R5: review file: REVIEW once
printf 'VERDICT: pass\n' > "$HERDSMAN_REPORTS/review-31-1.md"
out=$(run_r); check "R5 review" "REVIEW $HERDSMAN_REPORTS/review-31-1.md" "$out"
out=$(run_r); nocheck "R5b review not repeated" 'REVIEW' "$out"
# R6: missing report folder does not print errors
export HERDSMAN_REPORTS="$T/no-such-dir"
out=$(run_r); nocheck "R6 no reports dir: no error text" 'No such file|error' "$out"

# One-shot waiter: fresh state, real sleep restored (the waiter exits on its own)
unset -f sleep
export HERDSMAN_REPORTS="$T/reports2"; export HERDSMAN_STATE="$T/state2"; mkdir -p "$HERDSMAN_REPORTS"
printf '%s\n' "• Working (1s • esc to interrupt)" > "$FAKE_PANE_DIR/w9:p1.txt"
printf '%s\n' "Pursuing goal (1m)" > "$FAKE_PANE_DIR/w9:p2.txt"
printf '%s\n' "· Incubating…" > "$FAKE_PANE_DIR/w9:p3.txt"
run_e() { DEADLINE=0 POLL=1 /bin/bash "$SK/wait-event.sh" 2>&1; }
# E1: nothing happening: TICK and exit 0
out=$(run_e); rc=$?; check "E1 tick" '^TICK no event' "$out"; if [ "$rc" -eq 0 ]; then pass=$((pass+1)); echo "PASS E1 exit 0"; else fail=$((fail+1)); echo "FAIL E1 exit $rc"; fi
# E2: a report appears: REPORT and exit, no TICK
printf 'STATUS: complete\n' > "$HERDSMAN_REPORTS/a.md"
out=$(run_e); check "E2 report" 'REPORT a ' "$out"; nocheck "E2b no tick with an event" 'TICK' "$out"
# E3: worker at Goal achieved with no report: GOAL-DONE-NO-REPORT once
printf '%s\n' "Goal achieved (20m)" > "$FAKE_PANE_DIR/w9:p2.txt"
out=$(run_e); check "E3 goal done no report" 'GOAL-DONE-NO-REPORT wk-b' "$out"
out=$(run_e); nocheck "E3b not repeated on the same screen" 'GOAL-DONE' "$out"
# E4: reviewer permission prompt: BLOCKED rv-1; E5: review file: REVIEW
printf '%s\n' "Allow reads outside the working directories?" > "$FAKE_PANE_DIR/w9:p3.txt"
out=$(run_e); check "E4 reviewer blocked" 'BLOCKED rv-1 Allow reads outside' "$out"
printf 'VERDICT: fix\n' > "$HERDSMAN_REPORTS/review-7-1.md"
out=$(run_e); check "E5 review" 'REVIEW .*review-7-1.md' "$out"
# E6: stall after three unchanged working polls (counter persists across runs)
printf '%s\n' "· Incubating…" > "$FAKE_PANE_DIR/w9:p3.txt"
printf '%s\n' "• Working (9m • esc to interrupt)" > "$FAKE_PANE_DIR/w9:p1.txt"
out=$(run_e); out=$(run_e); out=$(run_e); nocheck "E6a no stall after the change plus two unchanged polls" 'STALL' "$out"
out=$(run_e); check "E6b stall on the third unchanged poll" 'STALL wk-a' "$out"

echo "RESULT pass=$pass fail=$fail (work dir $T)"
[ "$fail" -eq 0 ]
