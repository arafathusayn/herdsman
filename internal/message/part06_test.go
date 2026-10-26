package message

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// fault replaces the inbox's rename for the paths that contain one of the
// parts: fail returns an error, pretend reports success without moving.
func fault(t *testing.T, mode string, parts ...string) {
	restore := inbox.SetFileOps(inbox.FileOps{Rename: func(o, n string) error {
		for _, p := range parts {
			if strings.Contains(o+" "+n, p) {
				if mode == "pretend" {
					return nil
				}
				return errors.New("injected rename failure")
			}
		}
		return os.Rename(o, n)
	}})
	t.Cleanup(restore)
}

// message-tests.sh part 06: F, injected faults through the inbox's file calls.
func TestPart06Faults(t *testing.T) {
	c := newCase(t, true)
	c.send()
	func() {
		restore := inbox.SetFileOps(inbox.FileOps{})
		defer restore()
		fault(t, "fail", "/ready/", "/quarantine/", "/cur/")
		c.courier()
	}()
	inbox.SetFileOps(inbox.FileOps{Rename: os.Rename})
	check(t, "F1 a failed delivery move keeps the envelope in new", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	nocheck(t, "F1b and logs no verified delivery", "VERIFIED", verifiedLog(c))

	c = newCase(t, true)
	c.send()
	fault(t, "pretend", "/ready/", "/quarantine/", "/cur/")
	c.courier()
	inbox.SetFileOps(inbox.FileOps{Rename: os.Rename})
	nocheck(t, "F2 a move that claims success without moving reports no count to the recipient", "report-metadata receiver-pane .*messages=1", c.calls())
	nocheck(t, "F2b and logs no verified delivery", "VERIFIED", verifiedLog(c))

	c = newCase(t, true)
	c.send()
	c.courier()
	fault(t, "fail", "/ready/", "/quarantine/", "/cur/")
	_, st := c.run("receiver-pane", nil, "read", O)
	inbox.SetFileOps(inbox.FileOps{Rename: os.Rename})
	check(t, "F3 a failed read transition fails the read", "^[1-9]", strconv.Itoa(st))
	check(t, "F3b the message stays in ready with no ack", "^yes 0$", yn(c.ob("ready", ID+".md"))+" "+strconv.Itoa(nfiles(c.ob("ack"), "*")))

	c = newCase(t, true)
	c.send()
	c.editJSON(c.ob("new", ID+".md"), func(m map[string]any) { m["from"] = "route-intruder" })
	fault(t, "fail", "/ready/", "/quarantine/", "/cur/")
	_, st = c.courier()
	inbox.SetFileOps(inbox.FileOps{Rename: os.Rename})
	check(t, "F4 a failed quarantine move keeps the entry in new and the courier exits 0", "^yes no 0$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("quarantine", ID+".md"))+" "+strconv.Itoa(st))
	check(t, "F4b and raises the suspicious entry alert", "notification show Suspicious queue entry", c.calls())

	c = newCase(t, true)
	fault(t, "fail", "/new/")
	_, st = c.run("sender-pane", nil, "send", A, O, c.base("body.txt"), "brief")
	inbox.SetFileOps(inbox.FileOps{Rename: os.Rename})
	check(t, "F6 a failed publication fails the send", "^[1-9]", strconv.Itoa(st))
	check(t, "F6b and leaves no temp file and nothing queued", "^0 0 0$", strconv.Itoa(nfiles(c.ob("tmp"), ".send.*"))+" "+strconv.Itoa(nfiles(c.ob("tmp"), "*"))+" "+strconv.Itoa(nfiles(c.ob("new"), "*")))
	check(t, "F6c the next send gets a fresh ID: the failed one is never reused", "^m000000000002 queued", c.send())

	c = newCase(t, true)
	c.send()
	c.courier()
	out, _ := c.run("receiver-pane", nil, "read", O)
	tok := token(out)
	c.write(c.base("record.txt"), "intake recorded\n")
	fault(t, "fail", "/ack/")
	_, st = c.run("receiver-pane", nil, "ack", O, tok, ID, c.base("record.txt"))
	inbox.SetFileOps(inbox.FileOps{Rename: os.Rename})
	check(t, "F7 a failed ack producer fails the ack and commits no ack", "^[1-9][0-9]* no$", strconv.Itoa(st)+" "+yn(c.ob("ack", ID+".md.json")))
	out, _ = c.run("receiver-pane", nil, "read", O, tok)
	check(t, "F7b the unhandled message is still listed for the claim", `"id":"`+ID+`"`, out)

	c = newCase(t, true)
	e := &Env{Getenv: func(k string) string {
		return map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "HERDSMAN_MESSAGES_DIR": c.ic.Installed}[k]
	}}
	if err := e.open(false); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(c.in("ids", "probe"), 0o700)
	os.MkdirAll(c.ob("ack"), 0o700)
	os.MkdirAll(c.ob("notices"), 0o700)
	for _, leaf := range []string{"peers", "peers.approved", "state/test.json", "enrolled/test.json", "alerts/test.pending", "state/sequence",
		"ids/probe/terminal.json", "ids/probe/claim", O + "/ack/probe.json", O + "/notices/probe.json", "state/count-probe", "state/timing.json"} {
		p := c.in(filepath.FromSlash(leaf))
		if err := e.ch.WriteText(p, "1"); err != nil {
			t.Fatalf("%s: %v", leaf, err)
		}
		if e.ch.Atomic(p, nil) == nil || ((strings.HasSuffix(p, ".json") || strings.HasPrefix(filepath.Base(p), "peers")) && e.ch.Atomic(p, []byte("{")) == nil) {
			t.Errorf("F8 %s: a failed producer committed", leaf)
		}
		fault(t, "fail", "/"+filepath.Base(leaf))
		if e.ch.Atomic(p, []byte("2\n")) == nil {
			t.Errorf("F8 %s: a failed rename reported success", leaf)
		}
		inbox.SetFileOps(inbox.FileOps{Rename: os.Rename})
		check(t, "F8 a failed producer leaves every class of state file unchanged: "+leaf, "^1\n$", c.read(p))
	}
	if left := nfiles(c.in("state"), ".write.*"); left != 0 {
		t.Errorf("F8 %d temporary files left", left)
	}

	// F10: the exact age boundary on a fixed clock.
	c = newCase(t, true)
	c.send()
	if err := os.Chtimes(c.ob("new", ID+".md"), time.Unix(100, 0), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, at := range []int64{101, 102, 103} {
		now := time.Unix(at, 0)
		var out bytes.Buffer
		e := &Env{Getenv: func(k string) string {
			return map[string]string{"HERDSMAN_INBOX": c.ic.Dir, "HERDSMAN_PREFLIGHT": c.ic.Proof, "HERDSMAN_MESSAGES_DIR": c.ic.Installed, "WATCH_REQUIRED": "0"}[k]
		}, Stdout: &out, Stderr: &out, Client: herdr.New(c.srv.Socket), Now: func() time.Time { return now }, Sleep: func(time.Duration) {}}
		if err := e.open(false); err != nil {
			t.Fatal(err)
		}
		cr := &courier{Env: e, sweepSecs: 1, alertAfter: 2}
		if err := cr.sweep(); err != nil {
			t.Fatal(err)
		}
		e.releaseAll()
		got = append(got, strconv.Itoa(c.count("notification show Unread messages")))
	}
	check(t, "F10 no alert one second early, one at the exact age, none repeated", "^0 1 1$", strings.Join(got, " "))
}
