package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// runWithin runs the import and fails when it does not return in time: a
// source read that blocks (a FIFO opened for reading) is a failure, not a
// hang of the test binary.
func (x *importInbox) runWithin(t *testing.T, d time.Duration) *APIError {
	t.Helper()
	done := make(chan *APIError, 1)
	go func() { _, e := x.run(t); done <- e }()
	select {
	case e := <-done:
		return e
	case <-time.After(d):
		// Release the blocked open so the goroutine can end with the test.
		_ = filepath.Walk(x.dir, func(p string, fi os.FileInfo, _ error) error {
			if fi != nil && fi.Mode()&os.ModeNamedPipe != 0 {
				if f, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = f.Close()
				}
			}
			return nil
		})
		<-done
		t.Fatal("the import blocked on a source file")
		return nil
	}
}

// editEnvelope rewrites m1 (ready for orch-1) and its allocation record.
func (x *importInbox) editEnvelope(t *testing.T, edit func(map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(x.dir, "orch-1", "ready", "m000000000001.md"))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := inbox.Decode(b)
	m := inbox.Object(v)
	edit(m)
	out, _ := json.Marshal(m)
	x.write(t, "orch-1/ready/m000000000001.md", out)
	x.write(t, "ids/m000000000001/envelope", out)
}

// link replaces rel by a link to a copy of it outside the inbox.
func (x *importInbox) link(t *testing.T, rel string) {
	t.Helper()
	p := filepath.Join(x.dir, rel)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(target, b, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(p)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// TestImportSourceBoundary (F1, F4): every source file is read as a
// bounded regular file of this user, not writable by others, never a
// link and never opened when it is a FIFO; a present source that fails
// refuses the import, as does metadata that is present but malformed.
func TestImportSourceBoundary(t *testing.T) {
	fifo := func(rel string) func(*testing.T, *importInbox) {
		return func(t *testing.T, x *importInbox) {
			p := filepath.Join(x.dir, rel)
			_ = os.Remove(p)
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	chmod := func(rel string, m os.FileMode) func(*testing.T, *importInbox) {
		return func(t *testing.T, x *importInbox) {
			if err := os.Chmod(filepath.Join(x.dir, rel), m); err != nil {
				t.Fatal(err)
			}
		}
	}
	claimed := func(t *testing.T, x *importInbox) {
		x.write(t, "orch-1/claim/token", []byte(x.token+"\n"))
		x.write(t, "orch-1/claim/owner", []byte("p2:4242\n"))
		x.write(t, "ids/m000000000003/claim", []byte(x.token+"\n"))
	}
	for name, edit := range map[string]func(*testing.T, *importInbox){
		"fifo binding claim": func(t *testing.T, x *importInbox) { claimed(t, x); fifo("ids/m000000000003/claim")(t, x) },
		"fifo claim token":   func(t *testing.T, x *importInbox) { claimed(t, x); fifo("orch-1/claim/token")(t, x) },
		"fifo ack claim":     fifo("ids/m000000000002/claim"),
		"fifo enrolled":      fifo("enrolled/adj-1"),
		"fifo sequence":      fifo("state/sequence"),
		"fifo envelope":      fifo("ids/m000000000001/envelope"),
		"writable message":   chmod("orch-1/ready/m000000000001.md", 0o666),
		"writable queue":     chmod("orch-1/ready", 0o777),
		"writable ids":       chmod("ids", 0o777),
		"writable mailbox":   chmod("orch-1", 0o777),
		"writable inbox":     chmod(".", 0o777),
		"linked sequence":    func(t *testing.T, x *importInbox) { x.link(t, "state/sequence") },
		"linked enrolled":    func(t *testing.T, x *importInbox) { x.link(t, "enrolled/adj-1") },
		"linked ack":         func(t *testing.T, x *importInbox) { x.link(t, "orch-1/ack/m000000000002.md.json") },
		"linked ack claim":   func(t *testing.T, x *importInbox) { x.link(t, "ids/m000000000002/claim") },
		"non-JSON allocation": func(t *testing.T, x *importInbox) {
			x.write(t, "ids/m000000000001/envelope", []byte("not JSON\n"))
		},
		"allocation differs": func(t *testing.T, x *importInbox) {
			x.write(t, "ids/m000000000001/envelope", []byte("{}"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newImportInbox(t)
			edit(t, x)
			if e := x.runWithin(t, 3*time.Second); e == nil {
				t.Fatal("imported")
			}
		})
	}
}

// TestImportEnvelopeRules (F2): a ready or cur message is checked by the
// contract 1 envelope rules (inbox.Envelope's) before it is inserted.
func TestImportEnvelopeRules(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"version":          func(m map[string]any) { m["version"] = 999 },
		"unknown kind":     func(m map[string]any) { m["kind"] = "hostile-kind" },
		"null identity":    func(m map[string]any) { m["from_identity"] = nil },
		"sender path":      func(m map[string]any) { m["from"] = "../outside" },
		"numeric reply_to": func(m map[string]any) { m["reply_to"] = 123 },
		"missing reply_to": func(m map[string]any) { delete(m, "reply_to") },
		"negative time":    func(m map[string]any) { m["time"] = -1 },
		"body too long":    func(m map[string]any) { m["body"] = strings.Repeat("x", inbox.MaxBody+1) },
		"extra key":        func(m map[string]any) { m["extra"] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			x := newImportInbox(t)
			x.editEnvelope(t, edit)
			if _, e := x.run(t); e == nil {
				t.Fatal("imported")
			}
		})
	}
	// A quarantined entry that is no envelope (the courier quarantines
	// those) is not a message row: it stays in the legacy tree, logged.
	x := newImportInbox(t)
	x.write(t, "adj-1/quarantine/m000000000004.md", []byte("not an envelope\n"))
	x.write(t, "orch-1/notices/m000000000004.md.json", prettyJSON(obj("id", "m000000000004", "to", "adj-1", "state", "quarantined", "authority", "notice-only")))
	c, e := x.run(t)
	if e != nil {
		t.Fatal(e)
	}
	if n := rowsOf(t, c, `SELECT count(*) FROM messages WHERE id = 4`); n[0] != "0" {
		t.Fatal("a non-envelope quarantined entry became a message")
	}
	for _, p := range []string{"adj-1/quarantine/m000000000004.md", "ids/m000000000004/terminal.json", "ids/m000000000004/envelope", "orch-1/notices/m000000000004.md.json"} {
		if got := rowsOf(t, c, `SELECT disposition FROM import_log WHERE path = ?`, p); len(got) != 1 || !strings.HasPrefix(got[0], "legacy evidence") {
			t.Errorf("%s: %v", p, got)
		}
	}
}

// TestImportInvalidAck (F3): an ack file that is present must be valid;
// a malformed, mismatched, linked or fractional-time ack refuses the
// import instead of leaving its message silently unacked.
func TestImportInvalidAck(t *testing.T) {
	const rel = "orch-1/ack/m000000000002.md.json"
	set := func(k string, v any) func(*testing.T, *importInbox) {
		return func(t *testing.T, x *importInbox) {
			b, _ := os.ReadFile(filepath.Join(x.dir, rel))
			d, _ := inbox.Decode(b)
			m := inbox.Object(d)
			m[k] = v
			out, _ := json.Marshal(m)
			x.write(t, rel, out)
		}
	}
	for name, edit := range map[string]func(*testing.T, *importInbox){
		"bad json":         func(t *testing.T, x *importInbox) { x.write(t, rel, []byte("not JSON\n")) },
		"bad checksum":     set("envelope_checksum", "0-0"),
		"fractional time":  set("time", 1.5),
		"negative time":    set("time", -1),
		"other claim":      set("claim", ".claim.999"),
		"other id":         set("id", "m000000000003"),
		"empty processing": set("processing_record", ""),
		"no claim record": func(t *testing.T, x *importInbox) {
			_ = os.Remove(filepath.Join(x.dir, "ids", "m000000000002", "claim"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newImportInbox(t)
			edit(t, x)
			if _, e := x.run(t); e == nil {
				t.Fatal("imported")
			}
		})
	}
}

// TestImportNeedsPair (F5): a registry of one approved party refuses the
// import even when the other mailbox holds messages.
func TestImportNeedsPair(t *testing.T) {
	x := newImportInbox(t)
	b, _ := os.ReadFile(filepath.Join(x.dir, "peers"))
	v, _ := inbox.Decode(b)
	m := inbox.Object(v)
	delete(m, "orch-1")
	out, _ := json.Marshal(m)
	x.write(t, "peers", out)
	x.write(t, "peers.approved", out)
	if _, e := x.run(t); e == nil || e.Code != "checksum_mismatch" {
		t.Fatalf("got %v", e)
	}
}

// TestImportClaimSources (F4, F6): the binding record of a claimed
// message is in import_log; a file-mode token (.claim.<digits>) is
// imported unchanged with its binding; a token of neither form refuses.
func TestImportClaimSources(t *testing.T) {
	for _, token := range []string{strings.Repeat("c", 32), ".claim.987654"} {
		x := newImportInbox(t)
		x.write(t, "orch-1/claim/token", []byte(token+"\n"))
		x.write(t, "orch-1/claim/owner", []byte("p2:4242\n"))
		x.write(t, "ids/m000000000003/claim", []byte(token+"\n"))
		c, e := x.run(t)
		if e != nil {
			t.Fatalf("%s: %v", token, e)
		}
		if got := rowsOf(t, c, `SELECT token FROM claims`); len(got) != 1 || got[0] != token {
			t.Fatalf("claims %v, want %s", got, token)
		}
		if got := rowsOf(t, c, `SELECT coalesce(claim_token, '-') FROM messages WHERE id = 3`); got[0] != token {
			t.Fatalf("m3 bound to %v, want %s", got, token)
		}
		if got := rowsOf(t, c, `SELECT disposition FROM import_log WHERE path = 'ids/m000000000003/claim'`); len(got) != 1 || got[0] != "claims binding" {
			t.Fatalf("binding source: %v", got)
		}
	}
	for _, token := range []string{"short", ".claim.12x", strings.Repeat("C", 32), ".claim." + strings.Repeat("1", 40)} {
		x := newImportInbox(t)
		x.write(t, "orch-1/claim/token", []byte(token+"\n"))
		x.write(t, "orch-1/claim/owner", []byte("p2:4242\n"))
		if _, e := x.run(t); e == nil {
			t.Fatalf("token %q imported", token)
		}
	}
}
