#!/bin/bash
# Sanity tests for the herdsman scripts under macOS /bin/bash 3.2 with BSD tools, and under GNU tools.
# Run from bash or zsh: zsh <skill>/scripts/test/run-tests.sh [work dir] (or /bin/bash, sh, ./run-tests.sh)
# A `sleep` stub exits the watcher after one pass, so every test is one poll iteration.
# A fake `herdr` on PATH serves pane text and the session id from files and variables, and a fake `gh` serves pull
# request fixtures (see the comments in test/fakebin). The Z tests start the scripts from zsh, as a macOS shell does.
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
chmod +x "$SK/test/fakebin/herdr" "$SK/test/fakebin/gh"
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
# W9: as for the waiter, HERDSMAN_IMPLEMENTERS unset keeps the default list; set but empty means no implementers
# (a review-only route). Own pane, report and state folders. Each run is one pass, started with a fresh state folder.
imp_w() { # state folder name, then NAME=value settings
  local s=$1; shift
  env -u HERDSMAN_IMPLEMENTERS FAKE_PANE_DIR="$T/panes-w9" HERDSMAN_REPORTS="$T/reports-w9" HERDSMAN_STATE="$T/$s" "$@" /bin/bash "$SK/watch-implementers.sh" 2>&1
}
mkdir -p "$T/panes-w9" "$T/reports-w9"
printf '%s\n' "Password for 'https://x@github.com': Device not configured" > "$T/panes-w9/w9:p1.txt"
printf '%s\n' "Password for 'https://x@github.com': Device not configured" > "$T/panes-w9/w9:p2.txt"
printf 'STATUS: complete\n' > "$T/reports-w9/a.md"
out=$(imp_w state-w9a HERDSMAN_IMPLEMENTERS=); nocheck "W9a an empty implementer list watches no implementer" 'BLOCKED|REPORT|STALL|IDLE|rror' "$out"
out=$(imp_w state-w9b HERDSMAN_IMPLEMENTERS= HERDSMAN_INTEGRATOR=im-b); check "W9b an empty list still watches the integrator" 'BLOCKED im-b Password for' "$out"
nocheck "W9c and only the integrator" 'im-a' "$out"
out=$(imp_w state-w9c); check "W9d an unset implementer list keeps the default list" 'BLOCKED im-a Password for' "$out"

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
# R7: the poll interval is 60 s or less, because a permission prompt closes itself after about a minute.
# A `sleep` stub that records its argument shows the interval.
export SLEEP_LOG="$T/sleep-arg"; rm -f "$SLEEP_LOG"
sleep() { printf '%s' "$1" > "$SLEEP_LOG"; exit 0; }; export -f sleep
out=$(run_r)
sleep() { exit 0; }; export -f sleep
check "R7 the reviewer watcher sleeps 60 s or less between polls" '^([1-9]|[1-5][0-9]|60)$' "$(cat "$SLEEP_LOG" 2>/dev/null)"

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
export STUB_OUT="$T/status-out" HERDSMAN_STATUS_SPECS="im-a:w9:p1:$T" HERDSMAN_STATUS_SCRIPT="$T/status-stub.sh" HERDSMAN_HEALTH_EVERY=0
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
# E17: the health interval is the setting HERDSMAN_HEALTH_EVERY (seconds), default 300; the old name HEALTH_EVERY is
# still read. The clock file of a fresh state folder says that the last check ran N seconds ago, so each run shows
# whether a check is due.
health_run() { # N (seconds since the last check), then optional NAME=value settings
  rm -rf "$T/state-h"; mkdir -p "$T/state-h"; printf '%s' $(( $(date +%s) - $1 )) > "$T/state-h/health-at"; shift
  env -u HERDSMAN_HEALTH_EVERY -u HEALTH_EVERY HERDSMAN_STATE="$T/state-h" ${1+"$@"} DEADLINE=0 POLL=1 /bin/bash "$SK/wait-event.sh" 2>&1
}
out=$(health_run 200); nocheck "E17a default interval: a check 200 s ago is not due" 'HEALTH' "$out"
out=$(health_run 400); check "E17b default interval is 300 s: a check 400 s ago is due" 'HEALTH machine LOAD HIGH' "$out"
out=$(health_run 200 HERDSMAN_HEALTH_EVERY=100); check "E17c HERDSMAN_HEALTH_EVERY is read: a shorter interval is due" 'HEALTH machine LOAD HIGH' "$out"
out=$(health_run 700 HERDSMAN_HEALTH_EVERY=1000); nocheck "E17d HERDSMAN_HEALTH_EVERY is read: a longer interval is not due (a 600 s default or an ignored setting would be due)" 'HEALTH' "$out"
out=$(health_run 200 HEALTH_EVERY=100); check "E17e the old name HEALTH_EVERY is still read: a shorter interval is due" 'HEALTH machine LOAD HIGH' "$out"
out=$(health_run 700 HEALTH_EVERY=1000); nocheck "E17f the old name HEALTH_EVERY is still read: a longer interval is not due" 'HEALTH' "$out"
out=$(health_run 200 HERDSMAN_HEALTH_EVERY=1000 HEALTH_EVERY=100); nocheck "E17g HERDSMAN_HEALTH_EVERY wins over the old name" 'HEALTH' "$out"
unset STUB_OUT HERDSMAN_STATUS_SPECS HERDSMAN_STATUS_SCRIPT HERDSMAN_HEALTH_EVERY

# E15: HERDSMAN_IMPLEMENTERS unset keeps the default list; set but empty means no implementers (a review-only route)
mkdir -p "$T/reports3"; printf 'STATUS: complete\n' > "$T/reports3/a.md"
imp_run() { # state folder name, then NAME=value settings
  local s=$1; shift
  env HERDSMAN_REPORTS="$T/reports3" HERDSMAN_STATE="$T/$s" "$@" DEADLINE=0 POLL=1 /bin/bash "$SK/wait-event.sh" 2>&1
}
out=$(imp_run state-i1 HERDSMAN_IMPLEMENTERS=); nocheck "E18a an empty implementer list watches no implementer" 'REPORT' "$out"
check "E18b and the waiter still ticks" '^TICK no event' "$out"
printf 'VERDICT: pass\n' > "$T/reports3/review-3-1.md"
out=$(imp_run state-i1 HERDSMAN_IMPLEMENTERS=); check "E18c an empty implementer list still reports a review" 'REVIEW .*review-3-1.md' "$out"
out=$(env -u HERDSMAN_IMPLEMENTERS HERDSMAN_REPORTS="$T/reports3" HERDSMAN_STATE="$T/state-i2" DEADLINE=0 POLL=1 /bin/bash "$SK/wait-event.sh" 2>&1)
check "E18d an unset implementer list keeps the default list" 'REPORT a ' "$out"
# E16: a status tree that does not exist reaches the orchestrator as its own HEALTH flag
rm -rf "$T/state-t"; mkdir -p "$T/state-t"; printf '%s' $(( $(date +%s) - 400 )) > "$T/state-t/health-at"
out=$(imp_run state-t HERDSMAN_IMPLEMENTERS= HERDSMAN_STATUS_SPECS="im-a:w9:p1:$T/no-such-tree")
check "E16 the waiter reports a missing tree as a HEALTH flag" 'HEALTH im-a TREE MISSING' "$out"

# E14: a stale "Goal stalled" footer beside a working marker is no block: the stall counter still runs.
# E15: the same footer (or "Apply for Daybreak") on a pane with no working marker is BLOCKED, once per screen change.
# Own pane, report and state dirs, so the tests above stay unchanged.
old_panes=$FAKE_PANE_DIR; old_reports=$HERDSMAN_REPORTS; old_state=$HERDSMAN_STATE
export FAKE_PANE_DIR="$T/panes-stalled" HERDSMAN_REPORTS="$T/reports-stalled" HERDSMAN_STATE="$T/state-stalled"
mkdir -p "$FAKE_PANE_DIR" "$HERDSMAN_REPORTS"
printf '%s\n' "• Working (12s • esc to interrupt)" "  Goal stalled (/goal resume)" > "$FAKE_PANE_DIR/w9:p1.txt"
printf '%s\n' "Pursuing goal (1m)" > "$FAKE_PANE_DIR/w9:p2.txt"
printf '%s\n' "• Working (5s • esc to interrupt)" "  Goal stalled (/goal resume)" > "$FAKE_PANE_DIR/w9:p3.txt"
out=$(run_e); nocheck "E14a working pane with a stale stalled footer is not BLOCKED" 'BLOCKED im-a' "$out"
nocheck "E14b working reviewer with a stale stalled footer is not BLOCKED" 'BLOCKED rv-1' "$out"
out=$(run_e); out=$(run_e); nocheck "E14c no stall after the change plus two unchanged polls" 'STALL im-a' "$out"
out=$(run_e); check "E14d stall on the third unchanged poll" 'STALL im-a' "$out"
printf '%s\n' "› Ask Codex to do anything" "  Goal stalled (/goal resume)" > "$FAKE_PANE_DIR/w9:p1.txt"
printf '%s\n' "Apply for Daybreak" > "$FAKE_PANE_DIR/w9:p2.txt"
printf '%s\n' "❯" "  Goal stalled (/goal resume)" > "$FAKE_PANE_DIR/w9:p3.txt"
out=$(run_e); check "E15a quiet pane with a stalled footer is BLOCKED" 'BLOCKED im-a Goal stalled' "$out"
check "E15b quiet pane with the Daybreak text is BLOCKED" 'BLOCKED im-b Apply for Daybreak' "$out"
check "E15c quiet reviewer with a stalled footer is BLOCKED" 'BLOCKED rv-1 Goal stalled' "$out"
out=$(run_e); nocheck "E15d unchanged blocked screens are silent" 'BLOCKED' "$out"
export FAKE_PANE_DIR=$old_panes HERDSMAN_REPORTS=$old_reports HERDSMAN_STATE=$old_state

# A1: agent-status.sh prints the machine line and flags a tree with no recent writes
mkdir -p "$T/quiet-tree"; touch -t 202601010000 "$T/quiet-tree" 2>/dev/null
out=$(HERDSMAN_STATUS_SPECS="im-a:w9:p1:$T/quiet-tree" /bin/bash "$SK/agent-status.sh" 2>&1)
check "A1 machine line" '^machine: load [0-9.]+ \(5 min\) on [0-9]+ cores, memory free [0-9?]+%' "$(printf '%s\n' "$out" | grep '^machine')"
check "A1b quiet tree flagged" 'under quiet-tree: 0  <-- NO WRITES in 10m' "$out"
printf 'x\n' > "$T/quiet-tree/new.txt"
out=$(HERDSMAN_STATUS_SPECS="im-a:w9:p1:$T/quiet-tree" /bin/bash "$SK/agent-status.sh" 2>&1)
check "A1c a fresh write clears the flag" 'under quiet-tree: 1$' "$out"
out=$(HERDSMAN_STATUS_SPECS="im-a:w9:p1:$T/no-such-tree" /bin/bash "$SK/agent-status.sh" 2>&1)
check "A2 a tree path that does not exist is flagged" 'tree not found: .*no-such-tree  <-- TREE MISSING' "$out"
nocheck "A2b and is not read as no writes" 'NO WRITES' "$out"

# G: watch-prs.sh with the fake gh (fixtures in $FAKE_GH_DIR). A deadline of 0 makes each run one pass:
# the first pass records a baseline and prints TICK.
export FAKE_GH_DIR="$T/gh"; mkdir -p "$FAKE_GH_DIR"
run_p() { env HERDSMAN_PRS="$1" HERDSMAN_REPO=org/repo HERDSMAN_PR_STATE="$T/pr-state" HERDSMAN_PR_DEADLINE=0 HERDSMAN_PR_INTERVAL=1 /bin/bash "$SK/watch-prs.sh" 2>&1; }
/bin/bash -n "$SK/watch-prs.sh" && { pass=$((pass+1)); echo "PASS syntax watch-prs"; } || { fail=$((fail+1)); echo "FAIL syntax watch-prs"; }
printf '%s\n' 'state OPEN' 'head abcd1234' 'check test=PENDING' > "$FAKE_GH_DIR/7-view.txt"
out=$(run_p 7); check "G1 the first pass records a baseline and does not wake" '^TICK no PR change' "$out"
printf '%s\n' 'state OPEN' 'head abcd1234' 'check test=PENDING' 'check lint=SUCCESS' > "$FAKE_GH_DIR/7-view.txt"
out=$(run_p 7); check "G2 a passing check that no setting names stays silent" '^TICK no PR change' "$out"
printf '%s\n' 'state MERGED' 'head abcd1234' 'check test=PENDING' 'check lint=SUCCESS' > "$FAKE_GH_DIR/7-view.txt"
out=$(run_p 7); check "G3 the state change to MERGED wakes at once" '^PR [7] NEW state MERGED' "$out"; nocheck "G3b with no TICK" 'TICK' "$out"
out=$(run_p 7); nocheck "G3c the merge is not reported again" 'MERGED' "$out"
printf '%s\n' 'state MERGED' 'head 9999aaaa' > "$FAKE_GH_DIR/8-view.txt"
out=$(run_p 8); check "G4 a pull request that is merged at the first pass is reported once" '^PR [8] NEW state MERGED' "$out"
out=$(run_p 8); nocheck "G4b and not again" 'MERGED' "$out"

# P: checkpointer.sh --probe names the transcript and why. The fake `herdr agent get` reports the session id in
# $FAKE_SESSION. The named file of that id always wins; a newer file of the same folder, or of the working folder when
# the id has no file, is only a hint. The folders of other projects hold a newer file, which must never be used.
# TRANSCRIPT=<path> names the file to read.
CP="$T/cp"; CPP="$CP/home/.claude/projects"; mkdir -p "$CPP/projA" "$CPP/projB" "$CP/work" "$CP/empty"
usage() { printf '{"type":"assistant","message":{"usage":{"input_tokens":%s,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}\n' "$2" > "$1"; }
usage "$CPP/projA/old.jsonl" 111; touch -t 202601010000 "$CPP/projA/old.jsonl"
usage "$CPP/projA/cur.jsonl" 222
usage "$CPP/projB/other.jsonl" 999
cp_probe() { # session id, working folder of the checkpointer pane, optional TRANSCRIPT path
  (cd "$2" && env HOME="$CP/home" FAKE_SESSION="$1" TRANSCRIPT="${3:-}" TARGET=w9:p9 /bin/bash "$SK/checkpointer.sh" --probe 2>&1)
}
out=$(cp_probe cur "$CP/work")
check "P1 the file of the reported id is used when it is the newest of its folder" 'projA/cur.jsonl context=222' "$out"
check "P1b and the probe says why" 'transcript: the file of the session id that Herdr reports' "$out"
nocheck "P1c and gives no hint, because no newer file exists" 'hint:' "$out"
out=$(cp_probe old "$CP/work")
check "P2 the file of the reported id wins over a newer file of its folder" 'projA/old.jsonl context=111' "$out"
nocheck "P2b and the newer file is not read" 'cur.jsonl context=' "$out"
check "P2c and the probe says why" "transcript: the file of the session id that Herdr reports \('old'\)" "$out"
check "P2d and shows the newer file as a hint" 'hint: a newer transcript exists: .*projA/cur.jsonl; if the id is stale, set TRANSCRIPT=<path>' "$out"
derived=$(printf '%s' "$(cd "$CP/work" && pwd)" | sed 's/[^A-Za-z0-9]/-/g')
mkdir -p "$CPP/$derived"; usage "$CPP/$derived/first.jsonl" 333; touch -t 202601010000 "$CPP/$derived/first.jsonl"; usage "$CPP/$derived/second.jsonl" 444
out=$(cp_probe gone "$CP/work")
check "P3 no file for the id: no transcript is chosen" "transcript: no file for session id 'gone'" "$out"
nocheck "P3b and no file of the working folder's project is read" 'context=' "$out"
check "P3c and the newest file of that folder is shown as a hint" "hint: .*projects/$derived/second.jsonl; if the id is stale, set TRANSCRIPT=<path>" "$out"
out=$(cp_probe gone "$CP/empty")
check "P4 no file for the id and no project folder: the probe says so" "transcript: no file for session id 'gone' and no .jsonl in" "$out"
nocheck "P4b and names no file" 'cur.jsonl|second.jsonl|other.jsonl' "$out"
nocheck "P4c and prints no context line and no error" 'context=|No such file|tail:' "$out"
(cd "$CP/empty" && env HOME="$CP/home" FAKE_SESSION=gone TARGET=w9:p9 LOG="$CP/log" /bin/bash "$SK/checkpointer.sh" --once >/dev/null 2>&1); check "P5 --once with no transcript exits 1" '^1$' "$?"
out=$(cd "$CP/work" && env HOME="$CP/home" FAKE_SESSION=gone TARGET=w9:p9 LOG="$CP/log" /bin/bash "$SK/checkpointer.sh" --once 2>&1)
check "P5b --once with no file for the id stops, although the working folder holds a file" 'no transcript found' "$out"
nocheck "P5c and sends no checkpoint" 'sending' "$out"
out=$(cp_probe cur "$CP/work" "$CPP/projB/other.jsonl")
check "P6 TRANSCRIPT names the file to read, whatever the session id says" 'projB/other.jsonl context=999' "$out"
check "P6b and the probe says so" 'transcript: TRANSCRIPT is set' "$out"
out=$(cp_probe cur "$CP/work" "$CP/no-such.jsonl")
check "P6c TRANSCRIPT names a missing file: the probe says so" 'transcript: TRANSCRIPT is set but the file does not exist' "$out"
nocheck "P6d and no other file is read" 'context=' "$out"

# W10-W16: watch-prs.sh with the fake gh (fixtures in $FAKE_GH_DIR, see test/fakebin/gh). A deadline of 0 makes a run
# one pass. A longer deadline polls every second. Each group has its own state folder and its own pull request numbers.
export FAKE_GH_DIR="$T/gh"; mkdir -p "$FAKE_GH_DIR"
run_pw() { # pull requests, state folder name, deadline in seconds
  env HERDSMAN_PRS="$1" HERDSMAN_REPO=org/repo HERDSMAN_PR_STATE="$T/$2" HERDSMAN_PR_DEADLINE="$3" HERDSMAN_PR_INTERVAL=1 /bin/bash "$SK/watch-prs.sh" 2>&1
}
/bin/bash -n "$SK/watch-prs.sh" && { pass=$((pass+1)); echo "PASS syntax watch-prs"; } || { fail=$((fail+1)); echo "FAIL syntax watch-prs"; }
# pr_json <state> <head> <check run>...: the JSON of `gh pr view --json state,headRefOid,statusCheckRollup`
pr_json() { local s=$1 h=$2 IFS=,; shift 2; printf '{"state":"%s","headRefOid":"%s","statusCheckRollup":[%s]}\n' "$s" "$h" "$*"; }
# check_run <name> <status> <conclusion> <started> <completed> [<workflow>]. The times are short strings: the watcher only sorts them.
check_run() { printf '{"__typename":"CheckRun","name":"%s","status":"%s","conclusion":"%s","startedAt":"%s","completedAt":"%s","workflowName":"%s"}' "$@"; }
if command -v jq >/dev/null 2>&1; then
  # W10: a push or a rerun cancels the old run of a check while the new run is in progress: no failure event.
  # The old run is last in the list, so a pick of the last item (instead of the newest start time) fails.
  pr_json OPEN aaaa1111bbbb "$(check_run test COMPLETED SUCCESS T10:00:00Z T10:01:00Z)" > "$FAKE_GH_DIR/21-view.json"
  out=$(run_pw 21 w10 0); check "W10 the first pass records a baseline" '^TICK no PR change' "$out"
  pr_json OPEN aaaa1111bbbb "$(check_run test IN_PROGRESS '' T10:05:00Z '')" "$(check_run test COMPLETED CANCELLED T10:00:00Z T10:03:00Z)" > "$FAKE_GH_DIR/21-view.json"
  out=$(run_pw 21 w10 0); check "W10a an old cancelled run beside a newer run of the same name does not wake" '^TICK no PR change' "$out"
  nocheck "W10b and no event reports the cancelled result" 'CANCELLED' "$out"
  # W11: the newest run of a name decides, so a newer failure still wakes. The newer run is last in the list here.
  pr_json OPEN aaaa1111bbbb "$(check_run test COMPLETED SUCCESS T10:00:00Z T10:01:00Z)" > "$FAKE_GH_DIR/22-view.json"
  out=$(run_pw 22 w11 0)
  pr_json OPEN aaaa1111bbbb "$(check_run test COMPLETED SUCCESS T10:00:00Z T10:01:00Z)" "$(check_run test COMPLETED FAILURE T10:05:00Z T10:06:00Z)" > "$FAKE_GH_DIR/22-view.json"
  out=$(run_pw 22 w11 0); check "W11 a newer failed run of a name wakes" '^PR .* NEW check test=FAILURE$' "$out"
  nocheck "W11b with no TICK" 'TICK' "$out"
  # W16: two workflows that share a job name are two checks: a failure in one is not hidden by a newer success in the other.
  pr_json OPEN aaaa1111bbbb "$(check_run build COMPLETED SUCCESS T09:00:00Z T09:01:00Z CI)" > "$FAKE_GH_DIR/28-view.json"
  out=$(run_pw 28 w16 0)
  pr_json OPEN aaaa1111bbbb "$(check_run build COMPLETED FAILURE T10:00:00Z T10:01:00Z CI)" "$(check_run build COMPLETED SUCCESS T10:00:05Z T10:01:00Z 'Deploy preview')" > "$FAKE_GH_DIR/28-view.json"
  out=$(run_pw 28 w16 0); check "W16 a failed run of a job name wakes although another workflow's job of that name started later" '^PR .* NEW check build=FAILURE$' "$out"
  nocheck "W16b with no TICK" 'TICK' "$out"
else
  echo "SKIP W10, W11, W16: jq is not installed"
fi
# W12: a pull request that cannot be read is no "no change": the third failed poll in a row ends the run with exit 1.
touch "$FAKE_GH_DIR/23-fail"
out=$(run_pw 23 w12 5); rc=$?
check "W12 the third failed poll in a row ends the run with an error line" '^PR-WATCH-ERROR PR .* could not be read$' "$out"
nocheck "W12b and not with a TICK" 'TICK' "$out"
check "W12c exit 1" '^1$' "$rc"
check "W12d after exactly three failed polls" '^3$' "$(printf '%s\n' "$out" | grep -c 'HTTP 502')"
# W13: the deadline ends with one failed read: the TICK names the pull request that it did not read.
touch "$FAKE_GH_DIR/24-fail"
out=$(run_pw 24 w13 0); rc=$?
check "W13 a TICK after a failed read names the pull request" '^TICK no PR change \(not read: 24\)' "$out"
check "W13b exit 0" '^0$' "$rc"
# W14: a good read clears the count of failed reads, so three failed polls must follow each other.
touch "$FAKE_GH_DIR/25-fail"
out=$(run_pw 25 w14 0); out=$(run_pw 25 w14 0)
nocheck "W14 two failed polls in a row end no run" 'PR-WATCH-ERROR' "$out"
rm -f "$FAKE_GH_DIR/25-fail"; printf '%s\n' 'state OPEN' 'head aaaa1111' > "$FAKE_GH_DIR/25-view.txt"
out=$(run_pw 25 w14 0); nocheck "W14b a good read clears the failed reads from the TICK" 'not read' "$out"
touch "$FAKE_GH_DIR/25-fail"
out=$(run_pw 25 w14 0); check "W14c a failed poll after a good read counts as the first" 'not read: 25' "$out"
nocheck "W14d and ends no run" 'PR-WATCH-ERROR' "$out"
# W15: the error exit keeps the events that the same pass found on another pull request.
printf '%s\n' 'state OPEN' 'head aaaa1111' > "$FAKE_GH_DIR/26-view.txt"
touch "$FAKE_GH_DIR/27-fail"
out=$(run_pw "26 27" w15 0); out=$(run_pw "26 27" w15 0)
printf '%s\n' 'state OPEN' 'head bbbb2222' > "$FAKE_GH_DIR/26-view.txt"
out=$(run_pw "26 27" w15 0); rc=$?
check "W15 the event of the readable pull request is printed" '^PR .* NEW head bbbb2222$' "$out"
check "W15b and the unreadable one ends the run" '^PR-WATCH-ERROR PR .* could not be read$' "$out"
check "W15c exit 1" '^1$' "$rc"

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

# K: route-check.sh checks a route folder on disk and prints the open checklist lines
RK="$SK/route-check.sh"
/bin/bash -n "$RK" && { pass=$((pass+1)); echo "PASS syntax route-check"; } || { fail=$((fail+1)); echo "FAIL syntax route-check"; }
KR="$T/krepo"; KF="$KR/.herdsman/r1"
mkdir -p "$KF"; git -C "$KR" init -q; printf '.herdsman/\n' > "$KR/.gitignore"
printf '%s\n' '# Route r1' '## Decisions' '- x' '## Must prove' '- y' '## Task table' '| Task | Agent | Pane |' '| --- | --- | --- |' \
  '| a | im-a | w1:p2 |' '## Checklist' '- [x] C0.1 all: read guards -- done' '- [ ] C2.8 all: run-check.sh tested' \
  '- [-] C2.9 sbx: load publisher -- no sandboxed harness' '## Run state' '- [ ] not a checklist line' > "$KF/00-route.md"
printf '%s\n' '# Shared rules' '## Must prove' 'Every check runs through run-check.sh' > "$KF/10-shared-rules.md"
printf '%s\n' '# Task a' 'Due: 10:40' > "$KF/11-task-a.md"
printf '#!/bin/bash\n' > "$KF/run-check.sh"; chmod +x "$KF/run-check.sh"
out=$(/bin/bash "$RK" "$KF" 2>&1); rc=$?
check "K1 a complete folder passes the plan stage" '^0$' "$rc"; check "K1b and prints the open line" '^OPEN C2.8 all: run-check.sh tested$' "$out"
nocheck "K1c ticked, dropped and run-state lines are not open" 'OPEN (C0.1|C2.9|not a checklist)' "$out"
check "K1d git ignore seen" '^ok route folder ignored by git$' "$out"
out=$(/bin/bash "$RK" "$KF" close 2>&1); check "K2 an open line fails the close stage" '^1$' "$?"
sed 's/^- \[ \] C2.8 all: run-check.sh tested$/- [x] C2.8 all: run-check.sh tested -- real check and probes off passed/' "$KF/00-route.md" > "$T/k.tmp" && cp "$T/k.tmp" "$KF/00-route.md"
out=$(/bin/bash "$RK" "$KF" close 2>&1); rc=$?
check "K2b with every line closed the close stage passes" '^0$' "$rc"; check "K2c and says so" '^ok no open checklist line$' "$out"
chmod -x "$KF/run-check.sh"
out=$(/bin/bash "$RK" "$KF" 2>&1); check "K3 a contract names run-check.sh that is not executable" '^MISSING executable run-check.sh' "$out"
chmod +x "$KF/run-check.sh"; printf '# Task b\n' > "$KF/12-task-b.md"
out=$(/bin/bash "$RK" "$KF" 2>&1); check "K4 a task file without a Due line" '^MISSING Due line in 12-task-b.md$' "$out"
printf 'Due: 11:00\n' >> "$KF/12-task-b.md"
printf '%s\n' '| b | im-b | tbd |' >> "$T/k.row"
awk -v row="$(cat "$T/k.row")" '{print} /^\| a \| im-a/ {print row}' "$KF/00-route.md" > "$T/k.tmp" && cp "$T/k.tmp" "$KF/00-route.md"
check "K5 the plan stage ignores a placeholder pane" '^0$' "$(/bin/bash "$RK" "$KF" >/dev/null 2>&1; echo $?)"
out=$(/bin/bash "$RK" "$KF" launch 2>&1); check "K5b the launch stage reports it" '^MISSING pane in the task table: \| b \| im-b \| tbd \|$' "$out"
grep -v '^## Checklist' "$KF/00-route.md" > "$T/k.tmp" && cp "$T/k.tmp" "$KF/00-route.md"
out=$(/bin/bash "$RK" "$KF" 2>&1); rc=$?
check "K6 a missing section is reported" '^MISSING section Checklist in 00-route.md$' "$out"; check "K6b and fails" '^1$' "$rc"
printf '' > "$KR/.gitignore"
out=$(/bin/bash "$RK" "$KF" 2>&1); check "K7 a route folder that git does not ignore" '^MISSING git ignore' "$out"
/bin/bash "$RK" 2>/dev/null; check "K8 usage without a folder" '^2$' "$?"
/bin/bash "$RK" "$KF" later 2>/dev/null; check "K8b usage on an unknown stage" '^2$' "$?"
# K9-K15: a second route folder for the cases a pass must not hide
K2F="$KR/.herdsman/r2"; mkdir -p "$K2F"
kroute() { # header row, task row, checklist lines...
  local hd="$1" row="$2"; shift 2
  { printf '%s\n' '# Route r2' '## Decisions' '- x' '## Must prove' '- y' '## Task table' "$hd" '| --- | --- | --- |' "$row" '## Checklist'
    [ $# -gt 0 ] && printf '%s\n' "$@"; printf '%s\n' '## Run state'; } > "$K2F/00-route.md"
}
kroute '| Task | Agent | Pane |' '| a | im-a | w1:p2 |' '- [x] C0.1 all: read guards'
out=$(/bin/bash "$RK" "$K2F" 2>&1); rc=$?
check "K9 a done line without evidence is BAD" '^BAD - \[x\] C0.1 all: read guards' "$out"; check "K9b and fails" '^1$' "$rc"
kroute '| Task | Agent | Pane |' '| a | im-a | w1:p2 |' '- [-] C0.1 all: read guards -- ' '- [?] C0.2 all: unsure -- x'
out=$(/bin/bash "$RK" "$K2F" 2>&1)
check "K10 a dropped line with an empty reason is BAD" '^BAD - \[-\] C0.1' "$out"; check "K10b an unknown state is BAD" '^BAD - \[\?\] C0.2' "$out"
kroute '| Task | Agent | Pane |' '| a | im-a |   |' '- [x] C0.1 all: read guards -- done'
out=$(/bin/bash "$RK" "$K2F" launch 2>&1); check "K11 an empty pane cell fails the launch stage" '^MISSING pane in the task table: \| a \| im-a \|' "$out"
kroute '| Task | Pane | Agent |' '| a | tbd | im-a |' '- [x] C0.1 all: read guards -- done'
out=$(/bin/bash "$RK" "$K2F" launch 2>&1); check "K12 a placeholder in a reordered Pane column" '^MISSING pane in the task table: \| a \| tbd \| im-a \|' "$out"
kroute '| Task | Agent | Where |' '| a | im-a | w1:p2 |' '- [x] C0.1 all: read guards -- done'
out=$(/bin/bash "$RK" "$K2F" launch 2>&1); check "K13 a table with no Pane column" '^MISSING a Pane column' "$out"
kroute '| Task | Agent | Pane |' '| a | im-a | w1:p2 |' '- [x] C2.5 pr int gate2 ship: contracts -- 10-shared-rules.md' '- [x] C2.7 pr int gate2 ship: reviewer -- 20-reviewer.md'
mkdir -p "$K2F/10-shared-rules.md"; printf '# notes\n' > "$K2F/20-notes.md"
out=$(/bin/bash "$RK" "$K2F" 2>&1); rc=$?
check "K14 a kept contracts line needs the shared rules, and a directory is not a file" '^MISSING 10-shared-rules.md \(C2.5\)$' "$out"
check "K14b and a task contract" '^MISSING a 1n-task-<x>.md contract \(C2.5\)$' "$out"
check "K14c and the reviewer contract, which another 20- file does not replace" '^MISSING 20-reviewer.md \(C2.7\)$' "$out"
check "K14d and fails" '^1$' "$rc"
rmdir "$K2F/10-shared-rules.md"; rm -f "$K2F/20-notes.md"
kroute '| Task | Agent | Pane |' '| a | im-a | w1:p2 |' '- [x] C2.6 int: integrator -- 15-task-i.md' '- [x] C2.7 ro: brief -- 20-review-brief.md'
printf '# Task I\nDue: 12:00\n' > "$K2F/15-task-i.md"
out=$(/bin/bash "$RK" "$K2F" 2>&1)
check "K16 a numbered integrator contract satisfies C2.6" '^ok the integrator contract 1n-task-i.md \(C2.6\)$' "$out"
check "K16b a review-only route needs its brief" '^MISSING 20-review-brief.md \(C2.7\)$' "$out"
rm -f "$K2F/15-task-i.md"
kroute '| Task | Agent | Pane |' '| a | im-a | w1:p2 |' '- [x] C0.1 all: read guards -- done' '  - [ ] C2.8 all: indented and open'
out=$(/bin/bash "$RK" "$K2F" close 2>&1); rc=$?
check "K17 an indented checkbox is BAD, not hidden" '^BAD   - \[ \] C2.8' "$out"; check "K17b and the close stage fails" '^1$' "$rc"
tab=$(printf '\t')
kroute '| Task | Agent | Pane |' '| a | im-a | w1:p2 |' '- [x] C0.1 all: read guards -- done' '-  [ ] C2.8 all: two spaces' "-${tab}[ ] C2.9 all: a tab" '1. [ ] C2.10 all: numbered'
out=$(/bin/bash "$RK" "$K2F" close 2>&1); rc=$?
check "K17c two spaces after the bullet are BAD" '^BAD -  \[ \] C2.8' "$out"; check "K17d a tab after the bullet is BAD" "^BAD -${tab}\\[ \\] C2.9" "$out"
check "K17e a numbered checkbox is BAD" '^BAD 1\. \[ \] C2.10' "$out"; check "K17f and the close stage fails" '^1$' "$rc"
printf '%s\n' '# Route r2' '## Decisions' '## Must prove' '## Task table' '## Checklist' '- [x] C0.1 all: read guards -- done' '## Run state' > "$K2F/00-route.md"
out=$(/bin/bash "$RK" "$K2F" launch 2>&1); check "K18 an empty task table fails the launch stage" '^MISSING a Pane column' "$out"
nocheck "K18b with no ok for its panes" '^ok task table panes filled' "$out"
kroute '| Task | Agent | Pane |' '| a | im-a | w1:p2 |'
out=$(/bin/bash "$RK" "$K2F" close 2>&1); rc=$?
check "K15 an empty checklist is MISSING, not a pass" '^MISSING checklist lines' "$out"; nocheck "K15b with no ok for open lines" '^ok no open checklist line' "$out"
check "K15c and fails the close stage" '^1$' "$rc"

# C: run-check.sh runs one heavy check at a time, behind a load gate. A fixed reading keeps the gate deterministic.
RC="$SK/run-check.sh"
export HERDSMAN_LOCK="$T/checks.lock" HERDSMAN_READING="$T/load" HERDSMAN_PROBES=off HERDSMAN_GATE_POLL=1 HERDSMAN_LOAD_LIMIT=4
/bin/bash -n "$RC" && { pass=$((pass+1)); echo "PASS syntax run-check"; } || { fail=$((fail+1)); echo "FAIL syntax run-check"; }
echo "0.5 50" > "$HERDSMAN_READING"
/bin/bash "$RC" sh -c 'exit 3' 2>/dev/null; check "C1 exit status of the check" '^3$' "$?"
# Read the niceness from the kernel (getpriority), not from ps: the ps columns differ between macOS and Linux.
# nice adds to the caller's niceness (a CI runner can start jobs at -10), up to the maximum of 19.
prio='print getpriority(0, 0)'
want=$(( $(perl -e "$prio") + 10 )); [ "$want" -gt 19 ] && want=19
check "C1b lowers the priority by 10" "^$want\$" "$(/bin/bash "$RC" perl -e "$prio" 2>&1)"
/bin/bash "$RC" 2>/dev/null; check "C1c usage error without a command" '^64$' "$?"
check "C2 a nested check runs at once" '^nested$' "$(HERDSMAN_LOCK_WAIT=5 /bin/bash "$RC" /bin/bash "$RC" echo nested 2>&1)"
: > "$T/order.log"; echo "0.5 50" > "$HERDSMAN_READING"
/bin/bash "$RC" sh -c "/bin/sleep 2; echo first >> '$T/order.log'" 2>/dev/null & holder=$!
/bin/sleep 0.5
out=$(/bin/bash "$RC" sh -c "echo second >> '$T/order.log'" 2>&1); wait "$holder"
check "C3 the second check waits for the first" '^first second$' "$(tr '\n' ' ' < "$T/order.log" | sed 's/ $//')"
check "C3b and says whose lock it waits for" 'waiting for the machine lock, held by pid' "$out"
/bin/bash "$RC" /bin/sleep 3 2>/dev/null & holder=$!
/bin/sleep 0.5
out=$(HERDSMAN_LOCK_WAIT=1 /bin/bash "$RC" echo ran 2>&1); rc=$?; wait "$holder"
check "C4 no lock within the wait: NOT RUN" 'run-check: NOT RUN \(no lock within 1s\)' "$out"; nocheck "C4b the check did not run" '^ran$' "$out"
check "C4c exit 75" '^75$' "$rc"
echo "9.0 50" > "$HERDSMAN_READING"
out=$(HERDSMAN_GATE_WAIT=1 /bin/bash "$RC" echo ran 2>&1); rc=$?
check "C5 load over the limit: the gate gives up" 'NOT RUN \(load gate gave up after 1s: load 9.0, limit 4' "$out"
nocheck "C5b the check did not run" '^ran$' "$out"; check "C5c exit 75" '^75$' "$rc"
echo "0.5 5" > "$HERDSMAN_READING"
check "C6 low free memory: the gate gives up" 'NOT RUN \(load gate gave up' "$(HERDSMAN_GATE_WAIT=1 /bin/bash "$RC" echo ran 2>&1)"
echo "0.5 50" > "$HERDSMAN_READING"
out=$(/bin/bash "$RC" echo ran 2>&1)
check "C7 under the limits the check runs" '^ran$' "$out"; nocheck "C7b without a gate warning" 'run-check:' "$out"
rm -f "$HERDSMAN_READING"
out=$(/bin/bash "$RC" echo ran 2>&1)
check "C8 no reading: runs under the lock only and says so" 'no load reading, running under the lock only' "$out"; check "C8b and ran" 'ran$' "$out"
check "C9 no start markers left behind" '^none$' "$(ls "$T"/herdsman-check.* 2>/dev/null || echo none)"
: > "$T/orphan.log"
/bin/bash "$RC" sh -c "/bin/sleep 2; echo first >> '$T/orphan.log'" 2>/dev/null & outer=$!
/bin/sleep 0.5; kill -9 "$outer" 2>/dev/null; wait "$outer" 2>/dev/null
/bin/bash "$RC" sh -c "echo second >> '$T/orphan.log'" 2>/dev/null
check "C10 a running check keeps the lock after its run-check shell dies" '^first second$' "$(tr '\n' ' ' < "$T/orphan.log" | sed 's/ $//')"
unset HERDSMAN_PROBES
# C10b: the real probes feed the gate: no reading file, loose limits (a busy CI runner must not fail the test)
rm -f "$HERDSMAN_READING"
out=$(HERDSMAN_LOAD_LIMIT=10000 HERDSMAN_MIN_FREE=1 HERDSMAN_GATE_WAIT=5 /bin/bash "$RC" echo ran 2>&1)
check "C10b the machine's own reading lets a check run" '^ran$' "$out"; nocheck "C10c with a reading, not under the lock only" 'run-check:' "$out"
HERDSMAN_PUBLISH_SECONDS=2 HERDSMAN_PUBLISH_EVERY=1 /bin/bash "$RC" --publish 1 2> "$T/publisher.err"
check "C11 the publisher writes a reading" '^[0-9.]+ [0-9.]+$' "$(cat "$HERDSMAN_READING" 2>/dev/null)"
check "C11b and stops by itself" 'publisher stopped' "$(cat "$T/publisher.err")"
unset HERDSMAN_LOCK HERDSMAN_READING HERDSMAN_GATE_POLL HERDSMAN_LOAD_LIMIT

# Z: the scripts started from zsh in a clean environment, with the command forms the skill documents
if command -v zsh >/dev/null 2>&1; then
  zrun() { env -i PATH="$PATH" HOME="$HOME" FAKE_PANE_DIR="$FAKE_PANE_DIR" zsh -f -c "$@"; }
  mkdir -p "$T/z/reports"; printf 'STATUS: complete\n' > "$T/z/reports/a.md"
  out=$(zrun "HERDSMAN_REPORTS='$T/z/reports' HERDSMAN_STATE='$T/z/state' HERDSMAN_IMPLEMENTERS='im-a im-b' HERDSMAN_PANE_im_a=w9:p1 HERDSMAN_PANE_im_b=w9:p2 HERDSMAN_DUE_im_b=\$(( \$(date +%s) - 60 )) HERDSMAN_REVIEWERS='rv-1:w9:p3' DEADLINE=0 POLL=1 /bin/bash '$SK/wait-event.sh'" 2>&1)
  check "Z1 waiter from zsh: report" 'REPORT a ' "$out"; check "Z1b waiter from zsh: due time" 'OVERDUE im-b due [0-9][0-9]:[0-9][0-9]' "$out"
  zrun '/bin/bash "$1" "$2" "im-a: round 2 (due 14:05) * done"' zsh "$RL" "$T/00-route.md"
  check "Z2 route-log from zsh keeps the text" '^- [0-9][0-9]:[0-9][0-9] im-a: round 2 \(due 14:05\) \* done$' "$(tail -1 "$T/00-route.md")"
  zrun "HERDSMAN_LOCK='$T/z.lock' HERDSMAN_PROBES=off /bin/bash '$SK/run-check.sh' sh -c 'exit 4'" 2>/dev/null; check "Z3 run-check from zsh keeps the exit status" '^4$' "$?"
  check "Z4 a test runner started with zsh" 'RESULT pass=[0-9]+ fail=0' "$(zsh "$SK/test/checkpointer-tests.sh" 2>&1 | tail -1)"
else
  echo "SKIP Z tests: zsh is not installed"
fi

echo "RESULT pass=$pass fail=$fail (work dir $T)"
[ "$fail" -eq 0 ]
