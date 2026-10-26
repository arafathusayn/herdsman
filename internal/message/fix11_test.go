package message

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// editAllocation rewrites the allocation copy of ID through fn and ages it.
func editAllocation(t *testing.T, c *tc, fn func(m map[string]any)) {
	t.Helper()
	alloc := c.in("ids", ID, "envelope")
	var m map[string]any
	if err := json.Unmarshal([]byte(c.read(alloc)), &m); err != nil {
		t.Fatal(err)
	}
	if fn != nil {
		fn(m)
	}
	b, _ := json.Marshal(m)
	c.write(alloc, string(b))
	past := c.now.Add(-2 * time.Hour)
	os.Chtimes(alloc, past, past)
}

func retire(t *testing.T, c *tc) {
	t.Helper()
	if err := (&courier{Env: openEnv(t, c, nil)}).retireAbandoned(); err != nil {
		t.Fatal(err)
	}
}

// Review 5, finding 1: a valid copy naming the other approved recipient is
// not retired while the id has a live entry in the real recipient's queue.
func TestR5LiveEntryInAnyMailboxKept(t *testing.T) {
	c := newCase(t, true)
	c.send()
	editAllocation(t, c, func(m map[string]any) { m["to"] = A })
	retire(t, c)
	if inbox.Exists(c.in("ids", ID, "terminal.json")) {
		t.Error("a live entry in another approved mailbox was retired as abandoned")
	}
}

// Review 5, finding 1: an allocation folder replaced by a dangling link is
// unclear: its aged mark stays and nothing is published.
func TestR5DanglingAllocationParentKeepsMark(t *testing.T) {
	c := newCase(t, true)
	c.send()
	os.Remove(c.ob("new", ID+".md"))
	retire(t, c) // the seeding sweep: the copy is young
	if err := os.Rename(c.in("ids", ID), c.base("moved-allocation")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(c.base("nowhere"), c.in("ids", ID)); err != nil {
		t.Fatal(err)
	}
	past := c.now.Add(-2 * time.Hour)
	os.Chtimes(c.in("state", "allocations", ID), past, past)
	retire(t, c)
	if !inbox.Exists(c.in("state", "allocations", ID)) {
		t.Error("a dangling allocation folder lost its aged mark")
	}
}

// Review 5, finding 2: every rule of the allocation check guards
// retirement on its own. A cut allocation (no queue entry anywhere) whose
// copy breaks one rule stays with its mark; the valid control is retired.
func TestR5AllocationRules(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"valid":         nil,
		"version":       func(m map[string]any) { m["version"] = 2 },
		"extra-key":     func(m map[string]any) { m["extra"] = "x" },
		"negative-time": func(m map[string]any) { m["time"] = -1 },
		"long-body":     func(m map[string]any) { m["body"] = strings.Repeat("b", inbox.MaxBody+1) },
		"from-identity": func(m map[string]any) { m["from_identity"] = map[string]any{"pane": "", "session": map[string]any{}} },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			c := newCase(t, true)
			c.send()
			os.Remove(c.ob("new", ID+".md"))
			editAllocation(t, c, fn)
			retire(t, c)
			retired := inbox.Exists(c.in("ids", ID, "terminal.json"))
			marked := inbox.Exists(c.in("state", "allocations", ID))
			if name == "valid" {
				if !retired || marked {
					t.Errorf("the valid cut allocation was not retired (terminal %v, mark %v)", retired, marked)
				}
				return
			}
			if retired || !marked {
				t.Errorf("%s: an invalid copy was retired (terminal %v, mark %v)", name, retired, marked)
			}
		})
	}
}

// Review 5: a registry change awaiting approval makes the mailboxes
// unclear, so a cut allocation stays; an ids/ that is a link keeps an aged
// mark whose allocation it does not show.
func TestR5UnclearChannelKeepsAllocation(t *testing.T) {
	t.Run("registry", func(t *testing.T) {
		c := newCase(t, true)
		c.send()
		os.Remove(c.ob("new", ID+".md"))
		editAllocation(t, c, nil)
		c.write(c.in("peers"), c.read(c.in("peers"))+" ")
		retire(t, c)
		if inbox.Exists(c.in("ids", ID, "terminal.json")) || !inbox.Exists(c.in("state", "allocations", ID)) {
			t.Error("an allocation was retired while the registry awaits approval")
		}
	})
	t.Run("linked-ids", func(t *testing.T) {
		c := newCase(t, true)
		retire(t, c) // seeded
		e := openEnv(t, c, nil) // opened before ids/ turns into a link
		c.write(c.in("state", "allocations", "m000000000042"), "")
		past := c.now.Add(-2 * time.Hour)
		os.Chtimes(c.in("state", "allocations", "m000000000042"), past, past)
		if err := os.Rename(c.in("ids"), c.base("ids-real")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(c.base("ids-real"), c.in("ids")); err != nil {
			t.Fatal(err)
		}
		if err := (&courier{Env: e}).retireAbandoned(); err != nil {
			t.Fatal(err)
		}
		if !inbox.Exists(c.in("state", "allocations", "m000000000042")) {
			t.Error("a linked ids/ dropped an aged mark")
		}
	})
}
