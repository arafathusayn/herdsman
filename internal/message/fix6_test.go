package message

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// D12 crash cut: an allocation whose send stopped between the allocation
// copy and the queue move is retired as abandoned by the courier once it
// is an hour old; a young one is left alone.
func TestD12CrashCutRetired(t *testing.T) {
	c := newCase(t, true)
	c.send()
	os.Remove(c.ob("new", ID+".md")) // the cut: allocated, never queued
	c.courier()
	check(t, "D12 a young remnant is left alone", "^no$", yn(c.in("ids", ID, "terminal.json")))
	past := c.now.Add(-2 * time.Hour)
	os.Chtimes(c.in("ids", ID, "envelope"), past, past)
	c.courier()
	m, _ := decodeObject(c.read(c.in("ids", ID, "terminal.json")))
	check(t, "D12 an old remnant is retired as abandoned", "^abandoned$", raw(m["state"]))
}

// D20 on changed coverage: when the targets change during a run, every
// later held pass keeps the one record instead of rewriting it twice.
func TestD20ChangedCoverageWrittenOnce(t *testing.T) {
	c := watchedCase(t)
	base := c.fx.Handler("agent.list")
	changed := false
	c.srv.Handle("agent.list", func(p map[string]any) (any, string) {
		if !changed {
			changed = true
			c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "sender-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "sender-session"}}})
		}
		return base(p)
	})
	writes := 0
	restore := inbox.SetFileOps(inbox.FileOps{Rename: func(o, n string) error {
		if n == c.in("state", "automation-held") {
			writes++
		}
		return os.Rename(o, n)
	}})
	defer restore()
	c.watch("RUN_FOR=5")
	check(t, "D20 changed coverage writes the hold once over six passes", "^1$", strconv.Itoa(writes))
	check(t, "D20 the record names the coverage change", "changed watch coverage", c.read(c.in("state", "automation-held")))
}

// The courier's equal-replacement branch: an entry replaced while it was
// verified, together with its allocation copy, is not delivered on the old
// verification; it stays in new for the next sweep, which verifies it again.
func TestCourierEqualReplacementWaits(t *testing.T) {
	c := newCase(t, true)
	c.send()
	base := c.fxAgentGet()
	seen := 0
	c.srv.Handle("agent.get", func(p map[string]any) (any, string) {
		if p["target"] == "sender-pane" {
			if seen++; seen == 2 {
				c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) { m["body"] = "replaced brief\n" })
				c.write(c.in("ids", ID, "envelope"), c.read(c.ob("new", ID+".md")))
			}
		}
		return base(p)
	})
	c.courier("RUN_FOR=1")
	check(t, "an equal replacement waits in new", "^yes no no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md"))+" "+yn(c.ob("quarantine", ID+".md")))
	c.srv.Handle("agent.get", base)
	c.courier()
	check(t, "the next sweep verifies and delivers it", "^no yes$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
}

// Settings parsers (A25.5): a well-formed value is always within range, and
// anything accepted is digits with at most one unit suffix.
func FuzzSettings(f *testing.F) {
	for _, s := range []string{"60", "060", "1m", "2h", "1d", "3601", "0", "", "abc", "-1", " 5", "1.5", "5x", "99999999999999999999"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		if n, ok := settingDuration(v, 1, 3600); ok {
			if n < 1 || n > 3600 {
				t.Fatalf("%q -> %d out of range", v, n)
			}
			body := strings.TrimRight(v, "smhd")
			if body == "" || len(v)-len(body) > 1 || strings.Trim(body, "0123456789") != "" {
				t.Fatalf("%q accepted", v)
			}
		}
		if n, ok := settingInt(v, 1, 60); ok && (n < 1 || n > 60 || strings.Trim(v, "0123456789") != "") {
			t.Fatalf("int %q -> %d", v, n)
		}
	})
}
