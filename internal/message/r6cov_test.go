package message

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Round 6: the relay's exposure record fails only at its publication (a
// waiting record whose boundary-tainted rename fails): the watcher fails.
func TestR6RelayPublicationFailure(t *testing.T) {
	c := newCase(t, true)
	c.ic.WriteJSON(c.in("watch-targets.json"), []any{map[string]any{"pane": "receiver-pane", "session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "receiver-session"}}})
	c.fx.SetScreen("receiver-pane", "output line\n✦ Heads up · first finding\n")
	c.relayBoundary()
	base := c.fx.Handler("agent.list")
	calls := 0
	c.srv.Handle("agent.list", func(p map[string]any) (any, string) {
		calls++
		if calls == 1 {
			os.MkdirAll(c.in("state", "exposure-pending"), 0o700)
			c.write(c.in("state", "exposure-pending", "1.json"), `{"agents":[],"time":1}`+"\n")
		}
		return base(p)
	})
	defer inbox.SetFileOps(inbox.FileOps{Rename: func(o, n string) error {
		if filepath.Base(n) == "boundary-tainted" {
			return os.ErrPermission
		}
		return os.Rename(o, n)
	}})()
	out, code := c.watch()
	if code == 0 {
		t.Errorf("a relay exposure that cannot be published did not fail the watcher: %q", out)
	}
}
