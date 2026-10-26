// Package status is the one-shot health snapshot of every agent, as the
// skill's agent-status.sh prints it: the machine's load and memory, then
// per agent its state, model, background jobs with their ages, the
// foreground spinner, and the recent file writes in the tree it writes to.
// A flag is a line suffix "  <-- <FLAG>", which the waiter reads.
package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/machine"
)

// Spec is one agent to check: "name:pane:tree" (the pane holds a colon;
// an empty tree skips the write check).
type Spec struct{ Name, Pane, Tree string }

// ParseSpecs splits HERDSMAN_STATUS_SPECS as the script does: words, each
// name up to the first colon, the tree after the last, the pane between.
func ParseSpecs(s string) []Spec {
	var out []Spec
	for _, w := range strings.Fields(s) {
		name, rest, found := strings.Cut(w, ":")
		if !found {
			rest = w
		}
		pane, tree := rest, ""
		if i := strings.LastIndex(rest, ":"); i >= 0 {
			pane, tree = rest[:i], rest[i+1:]
		} else {
			tree = rest
		}
		out = append(out, Spec{Name: name, Pane: pane, Tree: tree})
	}
	return out
}

// Machine is the machine part of the snapshot; an unknown value is "".
type Machine struct {
	Load5 string // the 5-minute load average
	CPUs  string
	Free  string // free memory, whole percent
}

// ReadMachine reads the machine through the kernel.
func ReadMachine() Machine {
	var m Machine
	if l, err := machine.ReadLoad(); err == nil {
		m.Load5 = machine.FormatLoad(l[1])
	}
	m.CPUs = strconv.Itoa(machine.CPUs())
	if f, err := machine.FreePercent(); err == nil {
		m.Free = strconv.Itoa(f)
	}
	return m
}

// Options are the inputs of a snapshot.
type Options struct {
	Specs    []Spec
	LimitMin int // LIMIT_MIN as given (the command defaults it to 30)
	// LimitLabel is LIMIT_MIN as written, for the OVER flag (the script
	// prints ${LIMIT_MIN}m); "" prints LimitMin.
	LimitLabel string
	Client     *herdr.Client
	Now        time.Time
	Machine    Machine
}

var (
	bgRe = regexp.MustCompile(`(running|workflow).*[0-9]+(m|h) ?[0-9]*s?`)
	// durationRe is one elapsed time: "1h 02m", "23m 45s", "2h".
	durationRe = regexp.MustCompile(`(?:\b([0-9]+)h ?|\b)([0-9]+)m(?: ?[0-9]+s)?|\b([0-9]+)h\b`)
	fgRe       = regexp.MustCompile(`Thinking|Calling tools|esc to interrupt`)
	leadRe     = regexp.MustCompile(`^[^A-Za-z]*`)
)

// pruned are the dependency and build caches the write count skips.
var pruned = map[string]bool{"node_modules": true, ".git": true, ".next": true, ".turbo": true, ".venv": true, "target": true}

// Write prints the snapshot to w.
func Write(ctx context.Context, w io.Writer, o Options) {
	fmt.Fprintln(w, o.Now.Format("15:04:05"))
	m := o.Machine
	fmt.Fprintf(w, "machine: load %s (5 min) on %s cores, memory free %s%%\n", or(m.Load5, "?"), or(m.CPUs, "?"), or(m.Free, "?"))
	load, _ := strconv.ParseFloat(or(m.Load5, "0"), 64)
	cpus, _ := strconv.ParseFloat(or(m.CPUs, "0"), 64)
	if cpus > 0 && load > cpus {
		fmt.Fprintln(w, "   <-- LOAD HIGH")
	}
	if free, err := strconv.Atoi(m.Free); err == nil && free < 10 {
		fmt.Fprintln(w, "   <-- MEMORY LOW")
	}
	for _, s := range o.Specs {
		// The model is what Herdr reports for the agent; no screen pattern
		// lists model names (A28), so without a report the column is empty.
		state, screen, model := "", "", ""
		if o.Client != nil {
			if a, err := o.Client.AgentGet(ctx, s.Name); err == nil {
				state = string(a.Status)
				var m struct {
					Model *string `json:"model"`
				}
				if json.Unmarshal(a.Raw, &m) == nil && m.Model != nil {
					model = strings.Join(strings.Fields(*m.Model), " ")
				}
			}
			screen, _ = o.Client.ReadText(ctx, s.Pane, herdr.SourceVisible, 60)
		}
		lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
		fmt.Fprintf(w, "== %s (%s) state=%s model=%s\n", s.Name, s.Pane, state, model)
		var bg []string
		for _, l := range lines {
			if bgRe.MatchString(l) {
				bg = append(bg, l)
			}
		}
		if len(bg) > 3 {
			bg = bg[len(bg)-3:]
		}
		for _, row := range bg {
			total := elapsedMinutes(row)
			flag := ""
			if total >= o.LimitMin {
				label := o.LimitLabel
				if label == "" {
					label = strconv.Itoa(o.LimitMin)
				}
				flag = fmt.Sprintf("  <-- OVER %sm", label)
			}
			text := []rune(leadRe.ReplaceAllString(row, ""))
			if len(text) > 70 {
				text = text[:70]
			}
			fmt.Fprintf(w, "   bg: %s [%dm]%s\n", string(text), total, flag)
		}
		fg := ""
		for _, l := range lines {
			if fgRe.MatchString(l) {
				fg = l
			}
		}
		if fg != "" {
			fmt.Fprintf(w, "   fg: %s\n", fg)
		}
		if s.Tree == "" {
			continue
		}
		if st, err := os.Stat(s.Tree); err != nil || !st.IsDir() {
			// A wrong path would show NO WRITES, so name it as its own flag.
			fmt.Fprintf(w, "   tree not found: %s  <-- TREE MISSING\n", s.Tree)
			continue
		}
		n := RecentWrites(s.Tree, o.Now.Add(-10*time.Minute), 50)
		count, flag := strconv.Itoa(n), ""
		if n == 0 {
			flag = "  <-- NO WRITES in 10m"
		}
		if n == 50 {
			count = "50+"
		}
		fmt.Fprintf(w, "   files changed in last 10m under %s: %s%s\n", filepath.Base(s.Tree), count, flag)
	}
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

var errEnough = errors.New("enough")

// RecentWrites counts the regular files under tree changed after since, up
// to max, skipping dependency and build caches (and never following a link).
func RecentWrites(tree string, since time.Time, max int) int {
	n := 0
	_ = filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if pruned[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(since) {
			if n++; n >= max {
				return errEnough
			}
		}
		return nil
	})
	return n
}

// Flags reads the flags of a snapshot as the waiter does: "<agent> <flag>"
// for each line with "<-- ", the agent being the last "== <name>" above it
// (or "machine"), sorted and unique.
func Flags(snapshot string) []string {
	who := "machine"
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Split(snapshot, "\n") {
		if strings.HasPrefix(l, "== ") {
			if f := strings.Fields(l); len(f) > 1 {
				who = f[1]
			} else {
				who = ""
			}
		}
		if i := strings.LastIndex(l, "<-- "); i >= 0 {
			flag := who + " " + l[i+4:]
			if !seen[flag] {
				seen[flag] = true
				out = append(out, flag)
			}
		}
	}
	sort.Strings(out)
	return out
}

// maxMinutes caps a job's elapsed minutes: a count too large to read is
// still over any limit.
const maxMinutes = 1 << 30

// elapsedMinutes is a background row's elapsed time in minutes: its last
// duration. The script took the first hour count and the last minute
// count anywhere in the row, and an 08 or two hour counts broke its
// arithmetic (A26).
func elapsedMinutes(row string) int {
	all := durationRe.FindAllStringSubmatch(row, -1)
	if len(all) == 0 {
		return 0
	}
	m := all[len(all)-1]
	num := func(s string) int {
		if s == "" {
			return 0
		}
		n, err := strconv.Atoi(s)
		if err != nil || n > maxMinutes {
			return maxMinutes
		}
		return n
	}
	h, mins := num(m[1]), num(m[2])
	if m[3] != "" {
		h = num(m[3])
	}
	return min(h*60+mins, maxMinutes)
}
