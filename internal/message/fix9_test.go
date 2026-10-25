package message

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Review 4, finding 1: the courier's verification and quarantine repair
// take their locks in the run's own owner, so the parent's signal cleanup
// frees them at the commit.
func TestR4CourierChildLocksFreedBySignal(t *testing.T) {
	for _, mode := range []string{"verify", "quarantine"} {
		t.Run(mode, func(t *testing.T) {
			c := newCase(t, true)
			c.send()
			f := c.ob("new", ID+".md")
			if mode == "quarantine" {
				q := c.ob("quarantine", ID+".md")
				if err := os.Rename(f, q); err != nil {
					t.Fatal(err)
				}
				f = q
			}
			e := openEnv(t, c, nil)
			signalled, stranded := false, false
			e.Now = func() time.Time {
				if !signalled && inbox.Exists(c.in(inbox.ChannelLock)) {
					signalled = true
					e.Release()
					stranded = inbox.Exists(c.in(inbox.ChannelLock))
				}
				return c.now
			}
			e.ch.Root = reopenedRoot(t, e)
			cr := &courier{Env: e}
			if mode == "verify" {
				cr.processEntry(f, O)
			} else {
				_ = cr.recoverQuarantine(f, O)
			}
			if !signalled {
				t.Fatal("the child never held the queue lock while reading the clock")
			}
			if stranded {
				t.Error("the parent's signal cleanup left the child's queue lock")
			}
		})
	}
}

// reopenedRoot reopens the channel so its clock is the Env's current Now.
func reopenedRoot(t *testing.T, e *Env) *inbox.Root {
	t.Helper()
	ch, err := inbox.OpenChannel(e.options(false), e.Executable)
	if err != nil {
		t.Fatal(err)
	}
	return ch.Root
}

// Review 4, finding 2: an old allocation whose copy is a JSON object that
// is not a valid envelope for its id is never retired, even when it names
// a plausible recipient without an entry; an allocation folder that cannot
// be searched keeps its aged mark.
func TestR4UnclearAllocationKept(t *testing.T) {
	for _, mode := range []string{"schema", "renamed", "identity", "id"} {
		t.Run(mode, func(t *testing.T) {
			c := newCase(t, true)
			c.send()
			alloc := c.in("ids", ID, "envelope")
			var m map[string]any
			json.Unmarshal([]byte(c.read(alloc)), &m)
			m["to"] = A // a plausible recipient with no entry: the live entry is in O's queue
			switch mode {
			case "schema":
				delete(m, "version")
			case "identity":
				m["to_identity"] = map[string]any{"pane": "", "session": map[string]any{}}
			case "renamed":
				delete(m, "version")
				m["versions"] = 1
			default:
				m["id"] = "m000000000777"
			}
			b, _ := json.Marshal(m)
			c.write(alloc, string(b))
			past := c.now.Add(-2 * time.Hour)
			os.Chtimes(alloc, past, past)
			e := openEnv(t, c, nil)
			if err := (&courier{Env: e}).retireAbandoned(); err != nil {
				t.Fatal(err)
			}
			if inbox.Exists(c.in("ids", ID, "terminal.json")) {
				t.Errorf("%s: an invalid allocation copy marked a live message abandoned", mode)
			}
		})
	}
	t.Run("unsearchable", func(t *testing.T) {
		c := newCase(t, true)
		c.send()
		os.Remove(c.ob("new", ID+".md"))
		e := openEnv(t, c, nil)
		if err := (&courier{Env: e}).retireAbandoned(); err != nil { // the seeding sweep
			t.Fatal(err)
		}
		past := c.now.Add(-2 * time.Hour)
		os.Chtimes(c.in("state", "allocations", ID), past, past)
		chmodT(t, c.in("ids", ID), 0)
		if err := (&courier{Env: e}).retireAbandoned(); err != nil {
			t.Fatal(err)
		}
		if !inbox.Exists(c.in("state", "allocations", ID)) {
			t.Error("an unsearchable allocation lost its aged mark")
		}
	})
}

// PR #24 P0s in Go: a pending entry whose name is only a newline is a
// pending exposure (authority holds), and a busy writer keeps a large
// observed exposure as a bounded record that is published later.
func TestR4PendingNewlineName(t *testing.T) {
	c := watchedCase(t)
	os.MkdirAll(c.in("state", "exposure-pending"), 0o700)
	c.write(c.in("state", "exposure-pending", "\n"), `{"agents":[{"pane":"rogue-pane","agent":"claude","session":null}],"time":1}`+"\n")
	out, code := c.run("receiver-pane", nil, "authority")
	if code != 3 {
		t.Errorf("a newline-named pending entry did not hold authority: %d %q", code, out)
	}
}

func TestR4LargeExposureKeptBounded(t *testing.T) {
	c := newCase(t, true)
	e := openEnv(t, c, nil)
	var unknown []inbox.Unknown
	for i := range 700 {
		s, _ := json.Marshal(map[string]string{"agent": "claude", "kind": "id", "source": "hook", "value": strings.Repeat("s", 160) + fmt.Sprint(i)})
		unknown = append(unknown, inbox.Unknown{Pane: fmt.Sprintf("rogue-pane-%d", i), Agent: "claude", Session: s})
	}
	os.Mkdir(c.in(inbox.MutationLock), 0o700) // a busy writer
	if err := e.recordExposure(inbox.FaultInventory, unknown); err != nil {
		t.Fatalf("a large exposure was not kept: %v", err)
	}
	ents, _ := os.ReadDir(c.in("state", "exposure-pending"))
	if len(ents) != 1 {
		t.Fatalf("pending records %d", len(ents))
	}
	p := c.in("state", "exposure-pending", ents[0].Name())
	if b, ok := inbox.ReadStrict(c.ic.Dir, p); !ok || len(b) > inbox.MaxSize {
		t.Errorf("the pending record is not bounded strict JSON (%d bytes)", len(b))
	}
	os.Remove(c.in(inbox.MutationLock))
	if l, err := e.takeMutation(1); err != nil || l == nil {
		t.Fatalf("take: %v", err)
	}
	if b, ok := inbox.ReadStrict(c.ic.Dir, c.in("state", "boundary-tainted")); !ok || !strings.Contains(string(b), "rogue-pane-0") {
		t.Errorf("the large exposure was not published as a bounded record: %d bytes", len(b))
	}
}
