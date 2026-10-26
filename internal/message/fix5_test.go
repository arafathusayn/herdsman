package message

import (
	"strings"
	"testing"
)

// e93bcc1 FL1-FL3: a steady flood of other panes' events never starves a
// timer: the courier's sweep, the waiter's recheck and its deadline each
// run on time while the stream keeps sending.
func TestE93FloodKeepsTimers(t *testing.T) {
	c, _ := evcase(t, "@sleep 1", "@stage", "@flood-for 30")
	start := c.now
	c.courier("RUN_FOR=4", "SWEEP=1")
	check(t, "FL1 under a flood the courier's timer sweep delivers a send that has no ring", "^yes$", yn(c.ob("ready", ID+".md")))
	if took := c.now.Sub(start).Seconds(); took > 6 {
		t.Errorf("FL1b the courier exits by its run limit: %.1fs", took)
	}
	c, _ = evcase(t, "@flood-for 30")
	start = c.now
	out, _ := c.run("receiver-pane", map[string]string{"DEADLINE": "2", "RECHECK": "60"}, "wait", O)
	check(t, "FL2 under a flood the waiter reaches its deadline", "^TICK", out)
	if took := c.now.Sub(start).Seconds(); took > 4 {
		t.Errorf("FL2b on time: %.1fs", took)
	}
	c, _ = evcase(t, "@sleep 1", "@ready", "@flood-for 30")
	out, _ = c.run("receiver-pane", map[string]string{"DEADLINE": "12", "RECHECK": "1"}, "wait", O)
	check(t, "FL3 under a flood the waiter's recheck finds a delivery with no event for its pane", "^MESSAGE "+O, strings.TrimSpace(out))
}
