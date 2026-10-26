package message

import (
	"os"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Review 6, finding 2: with the valid allocation naming the other
// recipient and the real queue moved aside, a linked queue ancestor (new/
// a dangling link, new/ a link to an empty folder, the mailbox a link) is
// unclear: no terminal is written and the mark stays.
func TestR6LinkedQueueAncestorKeepsAllocation(t *testing.T) {
	for _, mode := range []string{"dangling-new", "empty-new", "linked-mailbox"} {
		t.Run(mode, func(t *testing.T) {
			c := newCase(t, true)
			c.send()
			editAllocation(t, c, func(m map[string]any) { m["to"] = A })
			e := openEnv(t, c, nil) // opened before the links appear
			switch mode {
			case "dangling-new", "empty-new":
				if err := os.Rename(c.ob("new"), c.base("new-aside")); err != nil {
					t.Fatal(err)
				}
				target := c.base("nowhere")
				if mode == "empty-new" {
					target = c.base("empty")
					os.Mkdir(target, 0o700)
				}
				if err := os.Symlink(target, c.ob("new")); err != nil {
					t.Fatal(err)
				}
			case "linked-mailbox":
				if err := os.Rename(c.in(O), c.base("box-aside")); err != nil {
					t.Fatal(err)
				}
				for _, s := range []string{"new", "ready", "cur", "quarantine"} {
					os.MkdirAll(c.base("box-empty", s), 0o700)
				}
				if err := os.Symlink(c.base("box-empty"), c.in(O)); err != nil {
					t.Fatal(err)
				}
			}
			past := c.now.Add(-2 * time.Hour)
			os.Chtimes(c.in("ids", ID, "envelope"), past, past)
			if err := (&courier{Env: e}).retireAbandoned(); err != nil {
				t.Fatal(err)
			}
			if inbox.Exists(c.in("ids", ID, "terminal.json")) || !inbox.Exists(c.in("state", "allocations", ID)) {
				t.Errorf("%s: a linked queue ancestor let a live allocation retire", mode)
			}
		})
	}
}
