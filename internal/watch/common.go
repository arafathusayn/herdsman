// Package watch holds the waiter and the watchers of the herdsman loop, as
// the skill's wait-event.sh, watch-implementers.sh, watch-reviewer.sh and
// watch-prs.sh run them: the same environment, state files, event lines
// and poll intervals. Pane text comes through the herdr socket.
package watch

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
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

func (d *Deps) defaults() {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = func(t time.Duration) bool { time.Sleep(t); return true }
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
	files, _ := filepath.Glob(filepath.Join(reports, "review-*.md"))
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
