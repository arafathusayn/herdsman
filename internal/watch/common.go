// Package watch holds the waiter and the watchers of the herdsman loop, as
// the skill's wait-event.sh, watch-implementers.sh, watch-reviewer.sh and
// watch-prs.sh run them: the same environment, state files, event lines
// and poll intervals. Pane text comes through the herdr socket.
package watch

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"

	"github.com/arafathusayn/herdsman/internal/settings"
	"github.com/arafathusayn/herdsman/internal/shglob"
	"github.com/arafathusayn/herdsman/internal/shnum"
)

// Env looks up one environment variable, as os.LookupEnv.
type Env func(string) (string, bool)

func (e Env) get(k string) string {
	v, _ := e(k)
	return v
}

func (e Env) or(k, d string) string {
	if v := e.get(k); v != "" {
		return v
	}
	return d
}

// Deps are what a watcher needs besides its environment.
type Deps struct {
	Client *herdr.Client
	Now    func() time.Time
	// Sleep waits between polls; false ends the loop (tests stop there).
	Sleep  func(time.Duration) bool
	Stdout io.Writer
	Stderr io.Writer
	// Status writes a `herdsman status` snapshot for the specs (the
	// waiter's health check when HERDSMAN_STATUS_SCRIPT is not set).
	Status func(ctx context.Context, w io.Writer, specs string)
	// Gh runs the gh program with args and returns its stdout; its stderr
	// goes to Stderr.
	Gh func(ctx context.Context, env []string, args ...string) ([]byte, error)
}

// defaults fills the clock and the sleep; the default sleep ends early, with
// false, when ctx ends, so an interrupt stops a watcher at once.
func (d *Deps) defaults(ctx context.Context) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = func(t time.Duration) bool {
			timer := time.NewTimer(t)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-timer.C:
				return true
			}
		}
	}
}

// defaultImplementers is the list an unset HERDSMAN_IMPLEMENTERS takes.
const defaultImplementers = "im-a im-b im-c im-d"

// watched is the implementers and the integrator: an unset
// HERDSMAN_IMPLEMENTERS takes the default list, an empty one none.
func watched(env Env) []string {
	list, ok := env("HERDSMAN_IMPLEMENTERS")
	if !ok {
		list = defaultImplementers
	}
	return strings.Fields(list + " " + env.get("HERDSMAN_INTEGRATOR"))
}

func underscore(name string) string { return strings.ReplaceAll(name, "-", "_") }

// paneOf is HERDSMAN_PANE_<name>, else the default pane of im-a to im-d.
func paneOf(env Env, name string) string {
	if v := env.get("HERDSMAN_PANE_" + underscore(name)); v != "" {
		return v
	}
	switch name {
	case "im-a":
		return "w2:pA"
	case "im-b":
		return "w2:pB"
	case "im-c":
		return "w2:pC"
	case "im-d":
		return "w2:pD"
	}
	return ""
}

// readPane reads the last lines of a pane, "" when it cannot, without the
// trailing newlines (as the scripts' command substitution drops them).
func readPane(ctx context.Context, c *herdr.Client, pane string, lines int) string {
	if c == nil || pane == "" {
		return ""
	}
	t, err := c.ReadText(ctx, pane, herdr.SourceRecentUnwrapped, lines)
	if err != nil {
		return ""
	}
	return strings.TrimRight(t, "\n")
}

// hash is the screen's POSIX cksum CRC, as the scripts store it.
func hash(text string) string {
	d := inbox.Digest([]byte(text))
	crc, _, _ := strings.Cut(d, "-")
	return crc
}

// state is a watcher's state folder.
type state string

func (s state) path(name string) string { return filepath.Join(string(s), name) }

func (s state) read(name string) string {
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}

func (s state) write(name, v string) { _ = os.WriteFile(s.path(name), []byte(v), 0o644) }

func (s state) touch(name string) {
	p := s.path(name)
	if f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
		_ = f.Close()
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
}

func (s state) has(name string) bool {
	st, err := os.Stat(s.path(name))
	return err == nil && st.Mode().IsRegular()
}

func (s state) remove(name string) { _ = os.Remove(s.path(name)) }

// mtime is a file's modification time in seconds, "" when it has none.
func mtime(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(st.ModTime().Unix(), 10)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// reviews lists reports/review-*.md in name order.
func reviews(reports string) []string {
	files, _ := shglob.Glob(reports, "review-*.md")
	var out []string
	for _, f := range files {
		if isFile(f) {
			out = append(out, f)
		}
	}
	return out
}

// posix compiles a pattern with grep -E's leftmost-longest matching.
func posix(re string) *regexp.Regexp { return regexp.MustCompilePOSIX(re) }

// first is the first match of re in text, as `grep -o | head -1`.
func first(re *regexp.Regexp, text string) string {
	for _, l := range strings.Split(text, "\n") {
		if m := re.FindString(l); m != "" {
			return m
		}
	}
	return ""
}

func clock(t time.Time) string { return t.Format("15:04:05") }

// maxDuration caps the watchers' duration settings: the scripts had no
// range, so one day (A25).
const maxDuration = 24 * time.Hour

// readSettings reads a watcher's duration settings strictly (A25): name,
// default. A poll interval under a second would spin (A24), so the names
// in atLeastOne must be at least a second.
func readSettings(env Env, specs []durSpec) ([]time.Duration, error) {
	r := settings.Reader{Getenv: env.get}
	out := make([]time.Duration, len(specs))
	for i, s := range specs {
		out[i] = r.Duration(s.name, s.def, maxDuration)
		if r.Err == nil && s.atLeastOne && out[i] < time.Second {
			r.Err = &settings.Error{Var: s.name, Reason: "must be at least 1 second", Got: env.get(s.name)}
		}
	}
	return out, r.Err
}

type durSpec struct {
	name       string
	def        time.Duration
	atLeastOne bool
}

// count reads a counter the watcher keeps in its state file. The scripts
// read it in $(( )), where 08 or junk ended the whole pass (A26): 08 is
// 8 here, and junk is reported and counted from 0.
func count(st state, file string, stderr io.Writer, who string) int {
	s := strings.TrimSpace(st.read(file))
	if s == "" {
		return 0
	}
	n, ok := shnum.Test(s)
	if !ok || n < 0 || n > 1<<30 {
		fmt.Fprintf(stderr, "herdsman %s: state file %s is not a number (got %q); counting from 0\n", who, file, s)
		return 0
	}
	return int(n)
}

// agentStatus is Herdr's status of the pane's agent, "" when unknown.
func agentStatus(ctx context.Context, c *herdr.Client, pane string) herdr.Status {
	if c == nil || pane == "" {
		return ""
	}
	a, err := c.AgentGet(ctx, pane)
	if err != nil {
		return ""
	}
	return a.Status
}

// blockReason decides BLOCKED from Herdr's agent status first (A25):
// "blocked" is a block (with the screen's reason when one shows); the
// screen patterns cover what the status cannot carry (a password, a model
// switch, a usage limit), and while Herdr reports "working" a match counts
// only on the screen's last 5 lines, so printed output is no block.
func blockReason(st herdr.Status, text string, re *regexp.Regexp) (string, bool) {
	if st == "blocked" {
		if r := first(re, text); r != "" {
			return r, true
		}
		return "agent blocked", true
	}
	if !re.MatchString(text) {
		return "", false
	}
	if st == "working" {
		lines := strings.Split(text, "\n")
		tail := strings.Join(lines[max(0, len(lines)-5):], "\n")
		if !re.MatchString(tail) {
			return "", false
		}
		return first(re, tail), true
	}
	return first(re, text), true
}

// screenBlock is blockReason for the block patterns, then, where no work
// text shows, the stalled-goal patterns, both through the same status-aware
// filter (review 5, finding 3).
func screenBlock(st herdr.Status, text string, block, work, stalled *regexp.Regexp) (string, bool) {
	if r, ok := blockReason(st, text, block); ok {
		return r, true
	}
	if stalled != nil && !work.MatchString(text) {
		return blockReason(st, text, stalled)
	}
	return "", false
}

// newBlock records whether name is blocked and says whether to report it:
// on each new blocked state, and again when its screen changes, so a
// working -> blocked change from Herdr is reported even when the pane text
// stays the same (review 5, finding 2).
func newBlock(st state, name string, blocked bool, h, prev string) bool {
	was := st.has("blocked-" + name)
	if !blocked {
		if was {
			st.remove("blocked-" + name)
		}
		return false
	}
	if !was {
		st.touch("blocked-" + name)
	}
	return h != prev || !was
}
