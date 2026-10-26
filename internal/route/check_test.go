package route

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The K cases of the skill's run-tests.sh: route-check.sh on a route folder.

func krepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	write(t, filepath.Join(repo, ".gitignore"), ".herdsman/\n")
	return repo
}

func write(t *testing.T, p string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func check(t *testing.T, dir, stage string) (string, int) {
	t.Helper()
	var b bytes.Buffer
	rc := Check(dir, stage, &b)
	return b.String(), rc
}

func want(t *testing.T, name, re, out string) {
	t.Helper()
	if !regexp.MustCompile("(?m)" + re).MatchString(out) {
		t.Errorf("%s: want /%s/ in:\n%s", name, re, out)
	}
}

func notWant(t *testing.T, name, re, out string) {
	t.Helper()
	if regexp.MustCompile("(?m)" + re).MatchString(out) {
		t.Errorf("%s: unexpected /%s/ in:\n%s", name, re, out)
	}
}

func wantRC(t *testing.T, name string, got, wantRC int) {
	t.Helper()
	if got != wantRC {
		t.Errorf("%s: exit %d, want %d", name, got, wantRC)
	}
}

func TestCheckFirstRoute(t *testing.T) {
	repo := krepo(t)
	kf := filepath.Join(repo, ".herdsman", "r1")
	route := []string{"# Route r1", "## Decisions", "- x", "## Must prove", "- y", "## Task table", "| Task | Agent | Pane |", "| --- | --- | --- |",
		"| a | im-a | w1:p2 |", "## Checklist", "- [x] C0.1 all: read guards -- done", "- [ ] C2.8 all: run-check.sh tested",
		"- [-] C2.9 sbx: load publisher -- no sandboxed harness", "## Run state", "- [ ] not a checklist line"}
	write(t, filepath.Join(kf, "00-route.md"), route...)
	write(t, filepath.Join(kf, "10-shared-rules.md"), "# Shared rules", "## Must prove", "Every check runs through run-check.sh")
	write(t, filepath.Join(kf, "11-task-a.md"), "# Task a", "Due: 10:40")
	write(t, filepath.Join(kf, "run-check.sh"), "#!/bin/bash")
	_ = os.Chmod(filepath.Join(kf, "run-check.sh"), 0o755)

	out, rc := check(t, kf, StagePlan)
	wantRC(t, "K1 a complete folder passes the plan stage", rc, 0)
	want(t, "K1b and prints the open line", `^OPEN C2.8 all: run-check.sh tested$`, out)
	notWant(t, "K1c ticked, dropped and run-state lines are not open", `OPEN (C0.1|C2.9|not a checklist)`, out)
	want(t, "K1d git ignore seen", `^ok route folder ignored by git$`, out)
	_, rc = check(t, kf, StageClose)
	wantRC(t, "K2 an open line fails the close stage", rc, 1)

	route[11] = "- [x] C2.8 all: run-check.sh tested -- real check and probes off passed"
	write(t, filepath.Join(kf, "00-route.md"), route...)
	out, rc = check(t, kf, StageClose)
	wantRC(t, "K2b with every line closed the close stage passes", rc, 0)
	want(t, "K2c and says so", `^ok no open checklist line$`, out)

	_ = os.Chmod(filepath.Join(kf, "run-check.sh"), 0o644)
	out, _ = check(t, kf, StagePlan)
	want(t, "K3 a contract names run-check.sh that is not executable", `^MISSING executable run-check.sh`, out)
	_ = os.Chmod(filepath.Join(kf, "run-check.sh"), 0o755)

	write(t, filepath.Join(kf, "12-task-b.md"), "# Task b")
	out, _ = check(t, kf, StagePlan)
	want(t, "K4 a task file without a Due line", `^MISSING Due line in 12-task-b.md$`, out)
	_ = os.Mkdir(filepath.Join(kf, "13-task-dir.md"), 0o755)
	out, _ = check(t, kf, StagePlan)
	notWant(t, "(Go) a folder named as a task file is no task file", `13-task-dir`, out)
	_ = os.Remove(filepath.Join(kf, "13-task-dir.md"))
	write(t, filepath.Join(kf, "12-task-b.md"), "# Task b", "Due: 11:00")

	withB := append(append(append([]string{}, route[:9]...), "| b | im-b | tbd |"), route[9:]...)
	write(t, filepath.Join(kf, "00-route.md"), withB...)
	_, rc = check(t, kf, StagePlan)
	wantRC(t, "K5 the plan stage ignores a placeholder pane", rc, 0)
	out, _ = check(t, kf, StageLaunch)
	want(t, "K5b the launch stage reports it", `^MISSING pane in the task table: \| b \| im-b \| tbd \|$`, out)

	var noChecklist []string
	for _, l := range withB {
		if !strings.HasPrefix(l, "## Checklist") {
			noChecklist = append(noChecklist, l)
		}
	}
	write(t, filepath.Join(kf, "00-route.md"), noChecklist...)
	out, rc = check(t, kf, StagePlan)
	want(t, "K6 a missing section is reported", `^MISSING section Checklist in 00-route.md$`, out)
	wantRC(t, "K6b and fails", rc, 1)

	write(t, filepath.Join(repo, ".gitignore"), "")
	out, _ = check(t, kf, StagePlan)
	want(t, "K7 a route folder that git does not ignore", `^MISSING git ignore`, out)
}

func kroute(t *testing.T, dir, header, row string, checklist ...string) {
	t.Helper()
	lines := []string{"# Route r2", "## Decisions", "- x", "## Must prove", "- y", "## Task table", header, "| --- | --- | --- |", row, "## Checklist"}
	lines = append(lines, checklist...)
	write(t, filepath.Join(dir, "00-route.md"), append(lines, "## Run state")...)
}

func TestCheckSecondRoute(t *testing.T) {
	repo := krepo(t)
	k2 := filepath.Join(repo, ".herdsman", "r2")
	const hd, row = "| Task | Agent | Pane |", "| a | im-a | w1:p2 |"

	kroute(t, k2, hd, row, "- [x] C0.1 all: read guards")
	out, rc := check(t, k2, StagePlan)
	want(t, "K9 a done line without evidence is BAD", `^BAD - \[x\] C0.1 all: read guards`, out)
	wantRC(t, "K9b and fails", rc, 1)

	kroute(t, k2, hd, row, "- [-] C0.1 all: read guards -- ", "- [?] C0.2 all: unsure -- x")
	out, _ = check(t, k2, StagePlan)
	want(t, "K10 a dropped line with an empty reason is BAD", `^BAD - \[-\] C0.1`, out)
	want(t, "K10b an unknown state is BAD", `^BAD - \[\?\] C0.2`, out)

	kroute(t, k2, hd, "| a | im-a |   |", "- [x] C0.1 all: read guards -- done")
	out, _ = check(t, k2, StageLaunch)
	want(t, "K11 an empty pane cell fails the launch stage", `^MISSING pane in the task table: \| a \| im-a \|`, out)

	kroute(t, k2, "| Task | Pane | Agent |", "| a | tbd | im-a |", "- [x] C0.1 all: read guards -- done")
	out, _ = check(t, k2, StageLaunch)
	want(t, "K12 a placeholder in a reordered Pane column", `^MISSING pane in the task table: \| a \| tbd \| im-a \|`, out)

	kroute(t, k2, "| Task | Agent | Where |", row, "- [x] C0.1 all: read guards -- done")
	out, _ = check(t, k2, StageLaunch)
	want(t, "K13 a table with no Pane column", `^MISSING a Pane column`, out)

	kroute(t, k2, hd, row, "- [x] C2.5 pr int gate2 ship: contracts -- 10-shared-rules.md", "- [x] C2.7 pr int gate2 ship: reviewer -- 20-reviewer.md")
	if err := os.Mkdir(filepath.Join(k2, "10-shared-rules.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(k2, "20-notes.md"), "# notes")
	out, rc = check(t, k2, StagePlan)
	want(t, "K14 a kept contracts line needs the shared rules, and a directory is not a file", `^MISSING 10-shared-rules.md \(C2.5\)$`, out)
	want(t, "K14b and a Contract column that names the task contracts", `^MISSING a Contract column in the task table \(C2.5\)$`, out)
	want(t, "K14c and the reviewer contract, which another 20- file does not replace", `^MISSING 20-reviewer.md \(C2.7\)$`, out)
	wantRC(t, "K14d and fails", rc, 1)
	_ = os.Remove(filepath.Join(k2, "10-shared-rules.md"))
	_ = os.Remove(filepath.Join(k2, "20-notes.md"))

	kroute(t, k2, hd, row, "- [x] C2.6 int: integrator -- 15-task-i.md", "- [x] C2.7 ro: brief -- 20-review-brief.md")
	write(t, filepath.Join(k2, "15-task-i.md"), "# Task I", "Due: 12:00")
	out, _ = check(t, k2, StagePlan)
	want(t, "K16 a numbered integrator contract satisfies C2.6", `^ok the integrator contract 1n-task-i.md \(C2.6\)$`, out)
	want(t, "K16b a review-only route needs its brief", `^MISSING 20-review-brief.md \(C2.7\)$`, out)
	_ = os.Remove(filepath.Join(k2, "15-task-i.md"))
	kroute(t, k2, hd, row, "- [x] C2.6 int: integrator -- 15-task-i.md")
	out, _ = check(t, k2, StagePlan)
	want(t, "K16c (Go) a kept integrator line with no contract is MISSING", `^MISSING the integrator contract 1n-task-i.md \(C2.6\)$`, out)

	kroute(t, k2, hd, row, "- [x] C0.1 all: read guards -- done", "  - [ ] C2.8 all: indented and open")
	out, rc = check(t, k2, StageClose)
	want(t, "K17 an indented checkbox is BAD, not hidden", `^BAD   - \[ \] C2.8`, out)
	wantRC(t, "K17b and the close stage fails", rc, 1)
	kroute(t, k2, hd, row, "- [x] C0.1 all: read guards -- done", "-  [ ] C2.8 all: two spaces", "-\t[ ] C2.9 all: a tab", "1. [ ] C2.10 all: numbered")
	out, rc = check(t, k2, StageClose)
	want(t, "K17c two spaces after the bullet are BAD", `^BAD -  \[ \] C2.8`, out)
	want(t, "K17d a tab after the bullet is BAD", "^BAD -\t\\[ \\] C2.9", out)
	want(t, "K17e a numbered checkbox is BAD", `^BAD 1\. \[ \] C2.10`, out)
	wantRC(t, "K17f and the close stage fails", rc, 1)

	write(t, filepath.Join(k2, "00-route.md"), "# Route r2", "## Decisions", "## Must prove", "## Task table", "## Checklist", "- [x] C0.1 all: read guards -- done", "## Run state")
	out, _ = check(t, k2, StageLaunch)
	want(t, "K18 an empty task table fails the launch stage", `^MISSING a Pane column`, out)
	notWant(t, "K18b with no ok for its panes", `^ok task table panes filled`, out)

	kroute(t, k2, hd, row)
	out, rc = check(t, k2, StageClose)
	want(t, "K15 an empty checklist is MISSING, not a pass", `^MISSING checklist lines`, out)
	notWant(t, "K15b with no ok for open lines", `^ok no open checklist line`, out)
	wantRC(t, "K15c and fails the close stage", rc, 1)
}

func TestCheckContracts(t *testing.T) {
	repo := krepo(t)
	k2 := filepath.Join(repo, ".herdsman", "r2")
	head := []string{"# Route r2", "## Decisions", "- x", "## Must prove", "- y", "## Task table", "| Task | Agent | Pane | Contract |", "| --- | --- | --- | --- |"}
	rows := []string{"| a | im-a | w1:p2 | 11-task-a.md |", "| b | rt-b | w1:p3 | 12-task-b.md |", "| i | im-a | w1:p2 | 15-task-i.md |",
		"| review | rv-1 | w2:p1 | - |", "| checkpointer | checkpointer.sh | w1:p5 | - |"}
	tail := []string{"## Checklist", "- [x] C2.5 pr int gate2 ship: contracts -- 10-shared-rules.md", "## Run state"}
	route := func(rows ...string) {
		write(t, filepath.Join(k2, "00-route.md"), append(append(append([]string{}, head...), rows...), tail...)...)
	}
	route(rows...)
	write(t, filepath.Join(k2, "10-shared-rules.md"), "# Shared rules", "## Must prove")
	write(t, filepath.Join(k2, "11-task-a.md"), "# Task a", "Due: 10:40")
	write(t, filepath.Join(k2, "15-task-i.md"), "# Task I", "Due: 12:00")
	out, rc := check(t, k2, StagePlan)
	want(t, "K19 a named contract that does not exist is MISSING, for a route-specific agent name too", `^MISSING the contract 12-task-b.md of task b \(C2.5\)$`, out)
	want(t, "K19b the contracts that exist pass", `^ok the contract 11-task-a.md of task a \(C2.5\)$`, out)
	wantRC(t, "K19c and the plan stage fails", rc, 1)
	want(t, "K19g an implementer kept on as the integrator is checked against the contract its row names", `^ok the contract 15-task-i.md of task i \(C2.5\)$`, out)
	notWant(t, "K19e no contract asked for rows marked -", `task (review|checkpointer)`, out)
	write(t, filepath.Join(k2, "12-task-b.md"), "# Task b", "Due: 11:00")
	_, rc = check(t, k2, StagePlan)
	wantRC(t, "K19d with every named contract written the plan stage passes", rc, 0)
	rows[1] = "| b | rt-b | w1:p3 | tbd |"
	route(rows...)
	out, _ = check(t, k2, StagePlan)
	want(t, "K19f a placeholder Contract cell is MISSING, not a pass", `^MISSING contract in the task table: \| b \| rt-b \| w1:p3 \| tbd \|$`, out)
	rows[1] = "| b | rt-b | w1:p3 |  |"
	route(rows...)
	out, _ = check(t, k2, StagePlan)
	want(t, "K19i an empty Contract cell is MISSING, not a pass", `^MISSING contract in the task table: \| b \| rt-b \| w1:p3 \|  \|$`, out)
	route("| review | rv-1 | w2:p1 | - |")
	out, _ = check(t, k2, StagePlan)
	want(t, "K19h a kept contracts line with no contract named is MISSING", `^MISSING a task contract named in the Contract column \(C2.5\)$`, out)
	route()
	out, _ = check(t, k2, StagePlan)
	want(t, "K19j (Go) a header with no task row has no contract column to read", `^MISSING a Contract column in the task table \(C2.5\)$`, out)

	head[7] = "| :--- | :---: | ---: | --- |"
	route("| a | im-a | w1:p2 | 11-task-a.md |")
	_ = os.Remove(filepath.Join(k2, "12-task-b.md"))
	_ = os.Remove(filepath.Join(k2, "15-task-i.md"))
	out, rc = check(t, k2, StageLaunch)
	wantRC(t, "K22 an aligned separator row passes the launch stage", rc, 0)
	notWant(t, "K22b and is not read as a row", `:---`, out)

	head[6] = "| Task | Agent | Pane |"
	head[7] = "| --- | --- | --- |"
	route()
	out, _ = check(t, k2, StageLaunch)
	want(t, "K22c (Go) a header with no task row is MISSING task rows", `^MISSING task rows in the task table$`, out)
}

func TestCheckNopane(t *testing.T) {
	repo := krepo(t)
	k2 := filepath.Join(repo, ".herdsman", "r2")
	const hd = "| Task | Agent | Pane |"
	kroute(t, k2, hd, "| a | stage-a | none |", "- [x] C3.2 nopane: agents as subagents -- user picked Without agent panes")
	out, rc := check(t, k2, StageLaunch)
	wantRC(t, "K20 a nopane route passes the launch stage", rc, 0)
	want(t, "K20b and says why", `^ok no agent panes \(C3.2 nopane\)$`, out)
	_, rc = check(t, k2, StageClose)
	wantRC(t, "K20c and the close stage", rc, 0)
	kroute(t, k2, hd, "| a | stage-a | none |", "- [x] C0.1 all: read guards -- done")
	out, _ = check(t, k2, StageLaunch)
	want(t, "K20d without the nopane line the pane check stays", `^MISSING pane in the task table`, out)
	kroute(t, k2, hd, "| a | im-a | none |", "- [-] C3.2 nopane: agents as subagents -- user switched to Herdr panes")
	out, _ = check(t, k2, StageLaunch)
	want(t, "K20e a dropped nopane line restores the pane check", `^MISSING pane in the task table`, out)
}

func TestCheckWithoutRouteFile(t *testing.T) {
	dir := t.TempDir()
	out, rc := check(t, dir, StagePlan)
	if rc != 1 || out != "MISSING 00-route.md\n" {
		t.Fatalf("rc %d out %q", rc, out)
	}
	if err := os.Mkdir(filepath.Join(dir, RouteFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, rc := check(t, dir, StagePlan); rc != 1 {
		t.Fatal("a folder named 00-route.md passed")
	}
	f := filepath.Join(t.TempDir(), RouteFile)
	write(t, f, "x")
	_ = os.Chmod(f, 0)
	if os.Geteuid() != 0 {
		if _, rc := check(t, filepath.Dir(f), StagePlan); rc != 1 {
			t.Fatal("an unreadable route file passed")
		}
	}
}

func TestCheckMustProveAndUnreadableContract(t *testing.T) {
	repo := krepo(t)
	k := filepath.Join(repo, ".herdsman", "r3")
	kroute(t, k, "| Task | Agent | Pane |", "| a | im-a | w1:p2 |", "- [x] C2.7 pr int: reviewer -- 20-reviewer.md")
	write(t, filepath.Join(k, "20-reviewer.md"), "# Reviewer")
	write(t, filepath.Join(k, "10-shared-rules.md"), "# Rules", "Must prove")
	out, _ := check(t, k, StagePlan)
	want(t, "K23 (Go) a reviewer contract without Must prove is MISSING", `^MISSING Must prove list in 20-reviewer.md$`, out)
	want(t, "K23b (Go) the shared rules with Must prove pass", `^ok Must prove in 10-shared-rules.md$`, out)
	if os.Geteuid() != 0 {
		_ = os.Chmod(filepath.Join(k, "20-reviewer.md"), 0)
		out, _ = check(t, k, StagePlan)
		want(t, "K23c (Go) an unreadable contract is MISSING", `^MISSING 20-reviewer.md \(C2.7\)$`, out)
	}
}

func TestAppendLogLine(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, RouteFile)
	write(t, f, "# route")
	now := time.Date(2026, 10, 25, 9, 7, 0, 0, time.Local)
	if err := AppendLogLine(f, "im-a: round 2 (due 14:05) * done", now); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f)
	if string(b) != "# route\n- 09:07 im-a: round 2 (due 14:05) * done\n" {
		t.Fatalf("file = %q", b)
	}
	if err := AppendLogLine(filepath.Join(dir, "nope.md"), "x", now); err == nil {
		t.Fatal("no error for a missing file")
	}
	if os.Geteuid() != 0 {
		_ = os.Chmod(f, 0o444)
		if err := AppendLogLine(f, "x", now); err == nil {
			t.Fatal("no error for a read-only file")
		}
	}
}

func TestSkillChecklistWaitsOnNoCloseStage(t *testing.T) {
	b, err := os.ReadFile("../../skills/herdsman/references/checklist.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(route-check\.sh|route check)[^;]* close`)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "- [ ]") && re.MatchString(l) {
			t.Errorf("K21 no checklist line waits on the close stage: %s", l)
		}
	}
}

func TestField(t *testing.T) {
	if field([]string{"a"}, 0) != "" || field([]string{"a"}, 2) != "" || field([]string{"a"}, 1) != "a" {
		t.Error("awk fields")
	}
}

// R3 sweep (a): a route folder whose name holds [ lists its own contracts.
func TestCheckRouteFolderWithBrackets(t *testing.T) {
	repo := krepo(t)
	kf := filepath.Join(repo, ".herdsman", "r[1]")
	write(t, filepath.Join(kf, "00-route.md"), "# Route r1", "## Decisions", "- x", "## Must prove", "- y", "## Task table",
		"| Task | Agent | Pane |", "| --- | --- | --- |", "| a | im-a | w1:p2 |", "## Checklist", "- [x] C0.1 all: read guards -- done",
		"## Run state")
	write(t, filepath.Join(kf, "11-task-a.md"), "# Task a")
	out, _ := check(t, kf, StagePlan)
	want(t, "a task file without a Due line in a bracketed folder", `^MISSING Due line in 11-task-a.md$`, out)
	write(t, filepath.Join(kf, "11-task-a.md"), "# Task a", "Due: 10:40")
	out, _ = check(t, kf, StagePlan)
	want(t, "and with its Due line", `^ok Due line in 11-task-a.md$`, out)
}

// Review head-2 P1 (A26): a dropped [-] line stopped applying, so it asks
// for no file; the script still demanded the dropped lines' files. Its
// reason is still checked.
func TestCheckDroppedRequirements(t *testing.T) {
	repo := krepo(t)
	k2 := filepath.Join(repo, ".herdsman", "r2")
	const hd, row = "| Task | Agent | Pane |", "| review | rv-1 | w2:p1 |"
	dropped := []string{
		"- [-] C2.5 pr int gate2 ship: contracts -- review only",
		"- [-] C2.6 int: the integrator -- review only",
		"- [-] C2.7 pr gate: the reviewer contract -- review only",
		"- [-] C2.8 all: run-check wrapper -- no heavy checks",
		"- [x] C2.7 ro: the review brief -- 20-review-brief.md",
	}
	kroute(t, k2, hd, row, dropped...)
	write(t, filepath.Join(k2, "20-review-brief.md"), "# Review brief")
	out, rc := check(t, k2, StageClose)
	wantRC(t, "a closed review-only route with dropped lines passes the close stage", rc, 0)
	notWant(t, "no dropped requirement is MISSING", `^MISSING`, out)
	want(t, "the open requirement is still checked", `^ok 20-review-brief.md \(C2.7\)$`, out)
	kroute(t, k2, hd, row, append([]string{"- [-] C2.8 all: run-check wrapper"}, dropped[4])...)
	out, rc = check(t, k2, StageClose)
	want(t, "a dropped line without a reason is BAD", `^BAD - \[-\] C2.8 all: run-check wrapper `, out)
	wantRC(t, "and fails", rc, 1)
	kroute(t, k2, hd, row, "- [ ] C2.8 all: run-check wrapper", dropped[4])
	out, _ = check(t, k2, StagePlan)
	want(t, "an open line is a requirement", `^MISSING executable run-check.sh`, out)
}

// The K23 cases of PR 28's run-tests.sh (feat/adjutant-owns-questions): a
// route with an adjutant records that every question, gos included, goes
// through the adjutant, and keeps C2.13 open or done.
func TestCheckAdjutantOwnsQuestions(t *testing.T) {
	repo := krepo(t)
	k2 := filepath.Join(repo, ".herdsman", "r2")
	kadj := func(a, q string, checklist ...string) {
		lines := []string{"# Route r2", "## Decisions", a}
		if q != "" {
			lines = append(lines, q)
		}
		lines = append(lines, "## Must prove", "## Task table", "| Task | Agent | Pane |", "| --- | --- | --- |", "| a | im-a | w1:p2 |", "## Checklist")
		write(t, filepath.Join(k2, "00-route.md"), append(append(lines, checklist...), "## Run state")...)
	}
	const kadjLine = "- [ ] C2.13 adj: questions through the adjutant"
	kadj("Adjutant: `front`, pane w1:p6", "Questions: through the adjutant (every question, gos included)", kadjLine)
	out, rc := check(t, k2, StagePlan)
	wantRC(t, "K23 (adj) an adjutant route that records its questions passes", rc, 0)
	want(t, "K23b (adj) and says so", `^ok questions through the adjutant \(C2.13\)$`, out)
	kadj("- Adjutant: `front`, pane w1:p6", "", kadjLine)
	out, rc = check(t, k2, StagePlan)
	want(t, "K23c (adj) an adjutant route without the Questions line is MISSING", `^MISSING a "Questions: through the adjutant" line`, out)
	wantRC(t, "K23d (adj) and fails", rc, 1)
	kadj("Adjutant: `front`, pane w1:p6", "Questions: through the adjutant", "- [-] C2.13 adj: questions through the adjutant -- not needed")
	out, _ = check(t, k2, StagePlan)
	want(t, "K23e (adj) an adjutant route cannot drop C2.13", `^MISSING the checklist line C2.13 adj`, out)
	kadj("Adjutant: `front`, pane w1:p6", "Questions: through the adjutant", "- [x] C0.1 all: read guards -- done")
	out, _ = check(t, k2, StagePlan)
	want(t, "K23f (adj) nor leave it out", `^MISSING the checklist line C2.13 adj`, out)
	kadj("Adjutant: none", "", "- [x] C0.1 all: read guards -- done")
	out, rc = check(t, k2, StagePlan)
	wantRC(t, "K23g (adj) a route without an adjutant needs neither", rc, 0)
	notWant(t, "K23h (adj) and checks no adjutant line", `C2.13`, out)
	for _, v := range []string{"no", "-", "NONE (solo route)"} {
		kadj("Adjutant: "+v, "", "- [x] C0.1 all: read guards -- done")
		out, rc = check(t, k2, StagePlan)
		if rc != 0 || strings.Contains(out, "C2.13") {
			t.Errorf("K23g (Go) Adjutant: %s: %d %q", v, rc, out)
		}
	}
	// K23i: the skill's checklist carries the line route check requires.
	// The checklist is PR 28's to change; until it merges into this branch
	// the line is not there, and the case says so instead of failing.
	b, err := os.ReadFile("../../skills/herdsman/references/checklist.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "C2.13") {
		t.Skip("K23i: the skill's checklist has no C2.13 line before PR 28 merges")
	}
	want(t, "K23i (adj) the skill's checklist has the C2.13 adj line that route check requires", `^- \[ \] C2\.13 adj: .*Questions: through the adjutant`, string(b))
}
