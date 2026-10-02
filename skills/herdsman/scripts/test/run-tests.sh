#!/bin/bash
# Sanity tests for the herdsman scripts under macOS /bin/bash 3.2 with BSD tools, and under GNU tools.
# Run from bash or zsh: zsh <skill>/scripts/test/run-tests.sh [work dir] (or /bin/bash, sh, ./run-tests.sh)
# A `sleep` stub exits the watcher after one pass, so every test is one poll iteration.
# A fake `herdr` on PATH serves pane text from files. The Z tests start the scripts from zsh, as a macOS shell does.
# Started from zsh or sh, re-run under /bin/bash (the line is valid in all three).
[ -n "${BASH_VERSION:-}" ] || exec /bin/bash "$0" "$@"
set -u
SK=$(cd "$(dirname "$0")/.." && pwd)
T=${1:-${TMPDIR:-/tmp}/herdsman-test-$$}
export PATH="$SK/test/fakebin:$PATH"
export FAKE_PANE_DIR="$T/panes"
export HERDSMAN_REPORTS="$T/reports"
export HERDSMAN_STATE="$T/state"
export HERDSMAN_IMPLEMENTERS="im-a im-b"
export HERDSMAN_PANE_im_a=w9:p1
export HERDSMAN_PANE_im_b=w9:p2
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
run_w() { /bin/bash "$SK/watch-implementers.sh" 2>&1; }
run_r() { /bin/bash "$SK/watch-reviewer.sh" 2>&1; }

echo "bash: $(/bin/bash --version | head -1)"
/bin/bash -n "$SK/watch-implementers.sh" && echo "PASS syntax watch-implementers" || { echo "FAIL syntax watch-implementers"; fail=$((fail+1)); }
/bin/bash -n "$SK/watch-reviewer.sh" && echo "PASS syntax watch-reviewer" || { echo "FAIL syntax watch-reviewer"; fail=$((fail+1)); }

# W1: working screens, first sight: no event
printf '%s\n' "• Working (3m 10s • esc to interrupt)" "› Ask Codex to do anything" > "$FAKE_PANE_DIR/w9:p1.txt"
printf '%s\n' "Pursuing goal (2m)" > "$FAKE_PANE_DIR/w9:p2.txt"
out=$(run_w); nocheck "W1 first working pass is silent" 'STALL|IDLE|BLOCKED|REPORT' "$out"
# W2: same screens again: STALL for both
out=$(run_w); check "W2 stall im-a" 'STALL im-a' "$out"; check "W2 stall im-b" 'STALL im-b' "$out"
# W3: password prompt: BLOCKED with the matched text
printf '%s\n' "Password for 'https://x@github.com': Device not configured" > "$FAKE_PANE_DIR/w9:p1.txt"
out=$(run_w); check "W3 blocked im-a" 'BLOCKED im-a Password for' "$out"
# W4: idle at the prompt with a changed screen: IDLE
printf '%s\n' "› Ask Codex to do anything" "/path/to/report.md" > "$FAKE_PANE_DIR/w9:p2.txt"
out=$(run_w); check "W4 idle im-b" 'IDLE im-b' "$out"
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
out=$(run_w); check "W7 queued question blocked" 'BLOCKED im-a Queued follow-up inputs' "$out"
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
out=$(run_r); nocheck "R2b unchanged blocked screen is silent" 'BLOCKED' "$out"
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
# E3: implementer at Goal achieved with no report: GOAL-DONE-NO-REPORT once
printf '%s\n' "Goal achieved (20m)" > "$FAKE_PANE_DIR/w9:p2.txt"
out=$(run_e); check "E3 goal done no report" 'GOAL-DONE-NO-REPORT im-b' "$out"
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
out=$(run_e); check "E6b stall on the third unchanged poll" 'STALL im-a' "$out"
# E7: due time in the future: no OVERDUE
export HERDSMAN_DUE_im_b=$(( $(date +%s) + 3600 ))
out=$(run_e); nocheck "E7 not overdue before the due time" 'OVERDUE' "$out"
# E8: due time passed, no report for im-b: OVERDUE once
export HERDSMAN_DUE_im_b=$(( $(date +%s) - 60 ))
out=$(run_e); check "E8 overdue without a report" 'OVERDUE im-b due [0-9][0-9]:[0-9][0-9]' "$out"
out=$(run_e); nocheck "E8b overdue not repeated" 'OVERDUE' "$out"
# E9: fix round: report a already exists when the due time is set, due passes unchanged: OVERDUE im-a
export HERDSMAN_DUE_im_a=$(( $(date +%s) - 60 ))
out=$(run_e); check "E9 overdue with a stale existing report" 'OVERDUE im-a' "$out"
# E10: report written after the due time was set: no OVERDUE even past the due time
unset HERDSMAN_DUE_im_a
export HERDSMAN_DUE_im_b=$(( $(date +%s) + 2 ))
out=$(run_e); nocheck "E10a armed, not yet due" 'OVERDUE' "$out"
printf 'STATUS: done\n' > "$HERDSMAN_REPORTS/b.md"
/bin/sleep 3
out=$(run_e); check "E10b report event" 'REPORT b ' "$out"; nocheck "E10c no overdue after the report" 'OVERDUE' "$out"
unset HERDSMAN_DUE_im_b
# E11: the integrator's task report: REPORT i, and the implementers' old reports stay silent
export HERDSMAN_INTEGRATOR=int-1 HERDSMAN_PANE_int_1=w9:p4 HERDSMAN_TASK_int_1=i
out=$(run_e); nocheck "E11a integrator watched, no report yet" 'REPORT' "$out"
printf 'STATUS: complete\n' > "$HERDSMAN_REPORTS/i.md"
out=$(run_e); check "E11b integrator report" "REPORT i $HERDSMAN_REPORTS/i.md" "$out"
unset HERDSMAN_INTEGRATOR HERDSMAN_PANE_int_1 HERDSMAN_TASK_int_1
# E13: health check: silent on the first arm, then each flag of agent-status.sh once, until it clears
printf '%s\n' '#!/bin/bash' 'cat "$STUB_OUT"' > "$T/status-stub.sh"
export STUB_OUT="$T/status-out" HERDSMAN_STATUS_SPECS="im-a:w9:p1:$T" HERDSMAN_STATUS_SCRIPT="$T/status-stub.sh" HEALTH_EVERY=0
printf '%s\n' '12:00:00' 'machine: load 9.00 (5 min) on 8 cores, memory free 50%' '   <-- LOAD HIGH' \
  '== im-a (w9:p1) state= model=' '   files changed in last 10m under t: 0  <-- NO WRITES in 10m' > "$STUB_OUT"
out=$(run_e); nocheck "E13a first arm only starts the clock" 'HEALTH' "$out"
out=$(run_e); check "E13b machine flag" 'HEALTH machine LOAD HIGH ' "$out"; check "E13c agent flag" 'HEALTH im-a NO WRITES in 10m ' "$out"
out=$(run_e); nocheck "E13d same flags are not repeated" 'HEALTH' "$out"
printf '%s\n' '12:10:00' 'machine: load 2.00 (5 min) on 8 cores, memory free 50%' '== im-a (w9:p1) state= model=' \
  '   files changed in last 10m under t: 0  <-- NO WRITES in 10m' > "$STUB_OUT"
out=$(run_e); nocheck "E13e a cleared flag is silent" 'HEALTH' "$out"
printf '%s\n' '12:20:00' 'machine: load 9.00 (5 min) on 8 cores, memory free 50%' '   <-- LOAD HIGH' '== im-a (w9:p1) state= model=' \
  '   files changed in last 10m under t: 0  <-- NO WRITES in 10m' > "$STUB_OUT"
out=$(run_e); check "E13f a flag that returns is reported again" 'HEALTH machine LOAD HIGH' "$out"; nocheck "E13g the unchanged flag stays silent" 'HEALTH im-a' "$out"
unset STUB_OUT HERDSMAN_STATUS_SPECS HERDSMAN_STATUS_SCRIPT HEALTH_EVERY

# A1: agent-status.sh prints the machine line and flags a tree with no recent writes
mkdir -p "$T/quiet-tree"; touch -t 202601010000 "$T/quiet-tree" 2>/dev/null
out=$(HERDSMAN_STATUS_SPECS="im-a:w9:p1:$T/quiet-tree" /bin/bash "$SK/agent-status.sh" 2>&1)
check "A1 machine line" '^machine: load [0-9.]+ \(5 min\) on [0-9]+ cores, memory free [0-9?]+%' "$(printf '%s\n' "$out" | grep '^machine')"
check "A1b quiet tree flagged" 'under quiet-tree: 0  <-- NO WRITES in 10m' "$out"
printf 'x\n' > "$T/quiet-tree/new.txt"
out=$(HERDSMAN_STATUS_SPECS="im-a:w9:p1:$T/quiet-tree" /bin/bash "$SK/agent-status.sh" 2>&1)
check "A1c a fresh write clears the flag" 'under quiet-tree: 1$' "$out"

# L1: route-log.sh appends one "- HH:MM <text>" line; L2: usage error on a missing argument or file
RL="$SK/route-log.sh"
/bin/bash -n "$RL" && echo "PASS syntax route-log" || { echo "FAIL syntax route-log"; fail=$((fail+1)); }
printf '# route\n' > "$T/00-route.md"
/bin/bash "$RL" "$T/00-route.md" "im-a started task a"
check "L1 appended line format" '^- [0-9][0-9]:[0-9][0-9] im-a started task a$' "$(tail -1 "$T/00-route.md")"
check "L1b exactly one line appended" '^2$' "$(wc -l < "$T/00-route.md" | tr -d ' ')"
out=$(/bin/bash "$RL" "$T/00-route.md" 2>&1); rc=$?
check "L2 usage on a missing text" '^usage: route-log.sh' "$out"; if [ "$rc" -ne 0 ]; then pass=$((pass+1)); echo "PASS L2 exit non-zero"; else fail=$((fail+1)); echo "FAIL L2 exit 0"; fi
out=$(/bin/bash "$RL" "$T/no-such-route.md" "x" 2>&1); check "L2b usage on a missing file" '^usage: route-log.sh' "$out"

# G: with-gate.sh runs one heavy command at a time
WG="$SK/with-gate.sh"
export HERDSMAN_GATE="$T/gate" HERDSMAN_GATE_POLL=1
/bin/bash -n "$WG" && { pass=$((pass+1)); echo "PASS syntax with-gate"; } || { fail=$((fail+1)); echo "FAIL syntax with-gate"; }
/bin/bash "$WG" sh -c 'exit 3'; rc=$?
check "G1 exit status of the command" '^3$' "$rc"; check "G1b lock released" '^gone$' "$([ -d "$HERDSMAN_GATE" ] && echo held || echo gone)"
# Read the niceness from the kernel (getpriority), not from ps: the ps columns differ between macOS and Linux.
# nice adds to the caller's niceness (a CI runner can start jobs at -10), up to the maximum of 19.
prio='print getpriority(0, 0)'
want=$(( $(perl -e "$prio") + 10 )); [ "$want" -gt 19 ] && want=19
check "G1c lowers the priority by 10" "^$want\$" "$(/bin/bash "$WG" perl -e "$prio" 2>&1)"
/bin/bash "$WG" 2>/dev/null; check "G1d usage error without a command" '^2$' "$?"
: > "$T/gate.log"
/bin/bash "$WG" sh -c "/bin/sleep 2; echo first >> '$T/gate.log'" & holder=$!
/bin/sleep 0.5
/bin/bash "$WG" sh -c "echo second >> '$T/gate.log'" 2>/dev/null; wait "$holder"
check "G2 the second command waits for the first" '^first second$' "$(tr '\n' ' ' < "$T/gate.log" | sed 's/ $//')"
sh -c 'exit 0' & dead=$!; wait "$dead"
mkdir "$HERDSMAN_GATE"; echo "$dead" > "$HERDSMAN_GATE/pid"
check "G3 a lock whose process is gone is taken over" '^ran$' "$(HERDSMAN_GATE_WAIT=5 /bin/bash "$WG" echo ran 2>/dev/null)"
# G3b: six waiters find the same stale lock at once: only one takes it over, and the commands never overlap
sh -c 'exit 0' & dead=$!; wait "$dead"
mkdir "$HERDSMAN_GATE"; echo "$dead" > "$HERDSMAN_GATE/pid"; : > "$T/race.log"
for i in 1 2 3 4 5 6; do
  /bin/bash "$WG" sh -c "echo s >> '$T/race.log'; /bin/sleep 0.2; echo e >> '$T/race.log'" 2>/dev/null &
done
wait
check "G3b a stale lock is taken over by one waiter only" '^(se){6}$' "$(tr -d '\n' < "$T/race.log")"
check "G3c no lock is left behind" '^none$' "$(ls -d "$HERDSMAN_GATE" "$HERDSMAN_GATE.takeover" 2>/dev/null || echo none)"
mkdir "$HERDSMAN_GATE"; touch -t 202601010000 "$HERDSMAN_GATE"
check "G4 an old lock without a pid is taken over" '^ran$' "$(HERDSMAN_GATE_WAIT=5 /bin/bash "$WG" echo ran 2>/dev/null)"
/bin/bash "$WG" /bin/sleep 3 & holder=$!
/bin/sleep 0.5
check "G5 an inherited mark without the lock does not skip the gate" 'running without the gate' "$(HERDSMAN_GATE_HELD=12345 HERDSMAN_GATE_WAIT=1 /bin/bash "$WG" echo inherited 2>&1)"
out=$(HERDSMAN_GATE_WAIT=1 /bin/bash "$WG" echo ran 2>&1)
check "G6 after the wait limit it runs anyway" 'running without the gate' "$out"; check "G6b and the command ran" 'ran$' "$out"
wait "$holder"
check "G5b a nested gate inside a gated command does not wait for its own lock" '^nested$' \
  "$(HERDSMAN_GATE_WAIT=60 /bin/bash "$WG" /bin/bash "$WG" echo nested 2>/dev/null)"
# G7: the gate shell is killed alone while its command runs: the next command waits for that command
: > "$T/orphan.log"
/bin/bash "$WG" sh -c "/bin/sleep 2; echo first >> '$T/orphan.log'" & gate=$!
/bin/sleep 0.5; kill -9 "$gate" 2>/dev/null; wait "$gate" 2>/dev/null
/bin/bash "$WG" sh -c "echo second >> '$T/orphan.log'" 2>/dev/null
check "G7 a running command keeps the lock after its gate shell dies" '^first second$' "$(tr '\n' ' ' < "$T/orphan.log" | sed 's/ $//')"
# G8: a stale lock that cannot be cleared: no busy loop, the wait limit still applies
mkdir "$HERDSMAN_GATE"; echo "$dead" > "$HERDSMAN_GATE/pid"; : > "$HERDSMAN_GATE/kept"
start=$(date +%s)
out=$(HERDSMAN_GATE_WAIT=2 perl -e 'alarm 15; exec @ARGV' /bin/bash "$WG" echo ran 2>&1)
check "G8 an uncleared stale lock reaches the wait limit" 'running without the gate' "$out"
check "G8b within the limit, not after a timeout" '^ok$' "$([ $(( $(date +%s) - start )) -lt 10 ] && echo ok || echo slow)"
rm -f "$HERDSMAN_GATE/kept" "$HERDSMAN_GATE/pid"; rmdir "$HERDSMAN_GATE"
unset HERDSMAN_GATE HERDSMAN_GATE_POLL

# Z: the scripts started from zsh in a clean environment, with the command forms the skill documents
if command -v zsh >/dev/null 2>&1; then
  zrun() { env -i PATH="$PATH" HOME="$HOME" FAKE_PANE_DIR="$FAKE_PANE_DIR" zsh -f -c "$@"; }
  mkdir -p "$T/z/reports"; printf 'STATUS: complete\n' > "$T/z/reports/a.md"
  out=$(zrun "HERDSMAN_REPORTS='$T/z/reports' HERDSMAN_STATE='$T/z/state' HERDSMAN_IMPLEMENTERS='im-a im-b' HERDSMAN_PANE_im_a=w9:p1 HERDSMAN_PANE_im_b=w9:p2 HERDSMAN_DUE_im_b=\$(( \$(date +%s) - 60 )) HERDSMAN_REVIEWERS='rv-1:w9:p3' DEADLINE=0 POLL=1 /bin/bash '$SK/wait-event.sh'" 2>&1)
  check "Z1 waiter from zsh: report" 'REPORT a ' "$out"; check "Z1b waiter from zsh: due time" 'OVERDUE im-b due [0-9][0-9]:[0-9][0-9]' "$out"
  zrun '/bin/bash "$1" "$2" "im-a: round 2 (due 14:05) * done"' zsh "$RL" "$T/00-route.md"
  check "Z2 route-log from zsh keeps the text" '^- [0-9][0-9]:[0-9][0-9] im-a: round 2 \(due 14:05\) \* done$' "$(tail -1 "$T/00-route.md")"
  zrun "HERDSMAN_GATE='$T/zgate' /bin/bash '$SK/with-gate.sh' sh -c 'exit 4'"; check "Z3 gate from zsh keeps the exit status" '^4$' "$?"
  check "Z4 a test runner started with zsh" 'RESULT pass=[0-9]+ fail=0' "$(zsh "$SK/test/checkpointer-tests.sh" 2>&1 | tail -1)"
else
  echo "SKIP Z tests: zsh is not installed"
fi

echo "RESULT pass=$pass fail=$fail (work dir $T)"
[ "$fail" -eq 0 ]
