package message

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// snapshot lists every path and file content of a tree.
func snapshot(t *testing.T, root string) string {
	var b []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		s := p + " " + info.Mode().String()
		if info.Mode().IsRegular() {
			d, _ := os.ReadFile(p)
			s += " " + string(d) + " " + info.ModTime().String()
		}
		b = append(b, s)
		return nil
	})
	sort.Strings(b)
	return strings.Join(b, "\n")
}

// message-tests.sh part 04: H1-H8.
func TestPart04AlertsSingletonsStatus(t *testing.T) {
	c := newCase(t, true)
	c.fx.Shown(false)
	c.send()
	c.courier()
	old(t, c.ob("ready", ID+".md"))
	c.courier("ALERT_AFTER=60")
	check(t, "H1 a disabled toast stays pending, not sent", "^1 0$", strconv.Itoa(nfiles(c.in("alerts"), "unread-*.pending"))+" "+strconv.Itoa(nfiles(c.in("alerts"), "unread-*.sent")))
	c.fx.Shown(true)
	ms, _ := filepath.Glob(c.in("alerts", "unread-*.attempt"))
	for _, m := range ms {
		old(t, m)
	}
	c.courier("ALERT_AFTER=60")
	check(t, "H1b a later retry records the shown toast", "^0 1$", strconv.Itoa(nfiles(c.in("alerts"), "unread-*.pending"))+" "+strconv.Itoa(nfiles(c.in("alerts"), "unread-*.sent")))
	c.fx.Fail("pane.report_metadata", "unavailable")
	check(t, "H2 a failed ring warns and the message stays queued", "WARNING ring failed.*queued", strings.ReplaceAll(c.send(), "\n", " "))
	c.fx.Fail("pane.report_metadata", "")
	c.write(c.in("route-ghost", "new", "m000000000050.md"), "full brief\n")
	if err := os.Symlink(c.base("body.txt"), c.ob("new", "m000000000051.md")); err != nil {
		t.Fatal(err)
	}
	c.courier()
	check(t, "H3 an orphan mailbox alerts and keeps its work", "notification show Orphaned message mailbox", c.calls())
	check(t, "H3b orphan work intact", "^yes$", yn(c.in("route-ghost", "new", "m000000000050.md")))
	check(t, "H4 a queue symlink is held with a safe alert", "notification show Suspicious queue entry", c.calls())
	check(t, "H4b the link stays in new and its target is untouched", "^yes full brief$", yn(c.ob("new", "m000000000051.md"))+" "+strings.TrimSpace(c.read(c.base("body.txt"))))
	out, _ := c.courier("SWEEP=abc")
	check(t, "H5 a malformed control fails before start", `SWEEP: not a duration`, out)
	check(t, "H5b without taking the courier lock", "^no$", yn(c.in("courier.lock")))
	os.Mkdir(c.in("courier.lock"), 0o700)
	out, _ = c.courier()
	check(t, "H6 a second courier is refused", "courier lock exists", out)
	os.Remove(c.in("courier.lock"))
	before := snapshot(t, c.ic.Dir)
	calls := len(c.srv.Requests())
	out, _ = c.run("receiver-pane", map[string]string{"HERDSMAN_NAME": O}, "message")
	check(t, "H7 message with no action is status", "courier pid=", out)
	check(t, "H7b status changes no inbox byte", "^same$", map[bool]string{true: "same"}[before == snapshot(t, c.ic.Dir)])
	check(t, "H7c and makes no Herdr call", "^"+strconv.Itoa(calls)+"$", strconv.Itoa(len(c.srv.Requests())))

	c = newCase(t, true)
	for i := range 1000 {
		c.write(c.ob("ready", "x"+strconv.Itoa(i)+".md"), "")
	}
	out = c.send()
	check(t, "H8 a full backlog refuses send before allocation", "pending backlog full", out)
	check(t, "H8b no ID was allocated", "^no$", yn(c.in("ids", ID)))
}
