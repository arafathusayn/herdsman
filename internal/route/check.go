package route

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/shglob"
)

// Stages of Check.
const (
	StagePlan   = "plan"
	StageLaunch = "launch"
	StageClose  = "close"
)

var (
	// A line that looks like a checkbox in any form; only the strict forms
	// below pass, so no line hides from the open count.
	checkboxLike = regexp.MustCompile(`^[ \t]*([-*+]|[0-9]+[.)])[ \t]+\[`)
	openLine     = regexp.MustCompile(`^- \[ \] `)
	closedLine   = regexp.MustCompile(`^- \[[x-]\] `)
	nopaneLine   = regexp.MustCompile(`^- \[[ x]\] C3\.2 nopane: `)
	// The adjutant rule's patterns, as route-check.sh greps them.
	adjutantLine  = regexp.MustCompile(`(?i)^[^[:alnum:]]*adjutant:[[:space:]]*[^[:space:]]`)
	noAdjutant    = regexp.MustCompile(`(?i)adjutant:[[:space:]]*(none|no|-)([[:space:]]|$)`)
	questionsLine = regexp.MustCompile(`^[^[:alnum:]]*Questions: through the adjutant`)
	adjLine       = regexp.MustCompile(`^- \[[ x]\] C2.13 adj: `)
	separatorRow  = regexp.MustCompile(`^\| *:?-+:? *\|`)
	cellTrim      = regexp.MustCompile(`^ +| +$`)
)

// section returns the lines under the first heading that starts with
// "## <name>" up to the next "## " heading, as the strict check reads them.
func section(lines []string, name string) []string {
	var out []string
	in := false
	for _, l := range lines {
		if strings.HasPrefix(l, "## "+name) {
			in = true
			continue
		}
		if strings.HasPrefix(l, "## ") {
			in = false
		}
		if in {
			out = append(out, l)
		}
	}
	return out
}

// awkFields splits a table line on "|" as awk -F'|' does: the fields
// between the first and the last.
func awkFields(line string) []string { return strings.Split(line, "|") }

func trimCell(s string) string { return cellTrim.ReplaceAllString(s, "") }

// field returns awk's $i (1-based) of the split line, "" past the end.
func field(f []string, i int) string {
	if i < 1 || i > len(f) {
		return ""
	}
	return f[i-1]
}

// columns finds the 1-based index of each wanted header (the last one
// when a header repeats), or 0.
func columns(header string, want ...string) []int {
	f := awkFields(header)
	idx := make([]int, len(want))
	for i := 2; i < len(f); i++ {
		h := trimCell(f[i-1])
		for j, w := range want {
			if h == w {
				idx[j] = i
			}
		}
	}
	return idx
}

// Check checks what a route folder shows on disk, the way the skill's
// route-check.sh does, and prints its lines to w: "ok <check>", "MISSING
// <check>", "BAD <line> (...)" and "OPEN <text>". It is strict where Load
// is lenient: a checklist line in any form but "- [ ] ", "- [x] ... --
// <evidence>" or "- [-] ... -- <reason>" is BAD. It returns 1 on any
// MISSING or BAD (and on OPEN at the close stage), else 0. The caller
// checks the folder and the stage.
func Check(dir, stage string, w io.Writer) int {
	miss := 0
	ok := func(s string) { fmt.Fprintf(w, "ok %s\n", s) }
	missing := func(s string) { fmt.Fprintf(w, "MISSING %s\n", s); miss = 1 }
	f := filepath.Join(dir, RouteFile)
	if st, err := os.Stat(f); err != nil || !st.Mode().IsRegular() {
		missing(RouteFile)
		return 1
	}
	b, err := os.ReadFile(f)
	if err != nil {
		missing(RouteFile)
		return 1
	}
	text := strings.TrimSuffix(string(b), "\n")
	all := strings.Split(text, "\n")
	for _, h := range []string{"Decisions", "Must prove", "Task table", "Checklist", "Run state"} {
		if slices.ContainsFunc(all, func(l string) bool { return strings.HasPrefix(l, "## "+h) }) {
			ok("section " + h)
		} else {
			missing("section " + h + " in 00-route.md")
		}
	}
	if gitIgnores(dir, f) {
		ok("route folder ignored by git")
	} else {
		missing("git ignore for the route folder (.herdsman/)")
	}

	var lines []string
	for _, l := range section(all, "Checklist") {
		if checkboxLike.MatchString(l) {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		missing("checklist lines in the Checklist section")
	} else {
		var bad []string
		for _, l := range lines {
			if openLine.MatchString(l) {
				continue
			}
			if closedLine.MatchString(l) {
				if _, after, found := strings.Cut(l, " -- "); found && strings.Trim(after, " ") != "" {
					continue
				}
			}
			bad = append(bad, l)
		}
		for _, l := range bad {
			fmt.Fprintf(w, "BAD %s (done needs \" -- <evidence>\", dropped needs \" -- <reason>\")\n", l)
			miss = 1
		}
		if len(bad) == 0 {
			ok("checklist lines well formed")
		}
	}
	// A requirement is an open or done line. The script counted a dropped
	// [-] line too (A26), though the checklist says [-] stopped applying;
	// a dropped line is still checked above for its reason.
	has := func(pattern string) bool {
		re := regexp.MustCompile(`^- \[[ x]\] ` + pattern + ` `)
		return slices.ContainsFunc(lines, re.MatchString)
	}
	need := func(name string, paths ...string) {
		for _, p := range paths {
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && readable(p) {
				ok(name)
				return
			}
		}
		missing(name)
	}

	table := section(all, "Task table")
	var rows []string
	for _, l := range table {
		if strings.HasPrefix(l, "|") {
			rows = append(rows, l)
		}
	}
	if has(`C2.5`) {
		need("10-shared-rules.md (C2.5)", filepath.Join(dir, "10-shared-rules.md"))
		cols := []int{0, 0}
		if len(rows) > 0 {
			cols = columns(rows[0], "Task", "Contract")
		}
		if len(rows) == 0 || cols[0] == 0 || cols[1] == 0 {
			missing("a Contract column in the task table (C2.5)")
		} else {
			named := false
			body := 0
			for _, row := range rows[1:] {
				if separatorRow.MatchString(row) {
					continue
				}
				body++
				fs := awkFields(row)
				t, c := trimCell(field(fs, cols[0])), trimCell(field(fs, cols[1]))
				switch c {
				case "-":
				case "", "tbd", "TBD", "?":
					missing("contract in the task table: " + row)
				default:
					named = true
					need(fmt.Sprintf("the contract %s of task %s (C2.5)", c, t), filepath.Join(dir, c))
				}
			}
			if body == 0 {
				missing("a Contract column in the task table (C2.5)")
			} else if !named {
				missing("a task contract named in the Contract column (C2.5)")
			}
		}
	}
	if has(`C2.6`) {
		integrators, _ := shglob.Glob(dir, "1[0-9]-task-i.md")
		need("the integrator contract 1n-task-i.md (C2.6)", integrators...)
	}
	if has(`C2.7 pr[a-z0-9 ]*:`) {
		need("20-reviewer.md (C2.7)", filepath.Join(dir, "20-reviewer.md"))
	}
	if has(`C2.7 ro:`) {
		need("20-review-brief.md (C2.7)", filepath.Join(dir, "20-review-brief.md"))
	}
	for _, name := range []string{"10-shared-rules.md", "20-reviewer.md"} {
		p := filepath.Join(dir, name)
		if !isFile(p) {
			continue
		}
		if b, _ := os.ReadFile(p); strings.Contains(string(b), "Must prove") {
			ok("Must prove in " + name)
		} else {
			missing("Must prove list in " + name)
		}
	}
	tasks, _ := shglob.Glob(dir, "1[0-9]-task-*.md")
	for _, p := range tasks {
		if !isFile(p) {
			continue
		}
		b, _ := os.ReadFile(p)
		if slices.ContainsFunc(strings.Split(string(b), "\n"), func(l string) bool { return strings.HasPrefix(l, "Due: ") }) {
			ok("Due line in " + filepath.Base(p))
		} else {
			missing("Due line in " + filepath.Base(p))
		}
	}
	if has(`C2.8`) || namesRunCheck(dir) {
		p := filepath.Join(dir, "run-check.sh")
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && executable(p) {
			ok("run-check.sh executable")
		} else {
			missing("executable run-check.sh (C2.8 or a contract names it)")
		}
	}

	// A route with an adjutant (an "Adjutant:" line whose value is not
	// none) sends every question, gos included, through the adjutant: the
	// route file records that in a "Questions: through the adjutant" line,
	// and the checklist keeps C2.13 open or done (a dropped line does not
	// count).
	if slices.ContainsFunc(all, func(l string) bool { return adjutantLine.MatchString(l) && !noAdjutant.MatchString(l) }) {
		if slices.ContainsFunc(all, questionsLine.MatchString) {
			ok("questions through the adjutant (C2.13)")
		} else {
			missing(`a "Questions: through the adjutant" line in 00-route.md of a route with an adjutant (C2.13)`)
		}
		if slices.ContainsFunc(lines, adjLine.MatchString) {
			ok("adjutant question line kept (C2.13)")
		} else {
			missing("the checklist line C2.13 adj, open or done, in a route with an adjutant")
		}
	}

	if stage != StagePlan {
		if slices.ContainsFunc(lines, nopaneLine.MatchString) {
			ok("no agent panes (C3.2 nopane)")
		} else {
			paneCheck(rows, ok, missing)
		}
	}

	var open []string
	for _, l := range lines {
		if openLine.MatchString(l) {
			open = append(open, strings.TrimPrefix(l, "- [ ] "))
		}
	}
	if len(open) > 0 {
		for _, l := range open {
			fmt.Fprintf(w, "OPEN %s\n", l)
		}
		if stage == StageClose {
			miss = 1
		}
	} else if len(lines) > 0 {
		ok("no open checklist line")
	}
	return miss
}

// paneCheck requires a real pane in every task row, from the Pane column
// of the header row.
func paneCheck(rows []string, ok, missing func(string)) {
	if len(rows) == 0 {
		missing("a Pane column in the task table header")
		return
	}
	col := columns(rows[0], "Pane")[0]
	if col == 0 {
		missing("a Pane column in the task table header")
		return
	}
	n := 0
	for _, row := range rows[1:] {
		if separatorRow.MatchString(row) {
			continue
		}
		n++
		switch trimCell(field(awkFields(row), col)) {
		case "", "tbd", "TBD", "?", "-", "none":
			missing("pane in the task table: " + row)
			return
		}
	}
	if n == 0 {
		missing("task rows in the task table")
		return
	}
	ok("task table panes filled")
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func readable(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// namesRunCheck reports whether a contract or brief ([12][0-9]-*.md) names
// run-check.sh.
func namesRunCheck(dir string) bool {
	files, _ := shglob.Glob(dir, "[12][0-9]-*.md")
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), "run-check.sh") {
			return true
		}
	}
	return false
}

// gitIgnores asks git whether it ignores the route file.
func gitIgnores(dir, file string) bool {
	return exec.Command("git", "-C", dir, "check-ignore", "-q", file).Run() == nil
}

// AppendLogLine appends "- HH:MM <text>" to the route file at path, as
// the skill's route-log.sh does: the text as given, the time from the
// clock. The file must exist.
func AppendLogLine(path, text string, now time.Time) error {
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("no route file %s", path)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "- %s %s\n", now.Format("15:04"), text)
	return err
}

// executable is the shell's -x: this user may run the file.
func executable(p string) bool { return syscall.Access(p, 1) == nil } // X_OK
