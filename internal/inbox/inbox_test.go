package inbox_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

func TestDigestMatchesPOSIX(t *testing.T) {
	// From `printf 'hello world\n' | cksum` and `printf '' | cksum`.
	for in, want := range map[string]string{"hello world\n": "3733384285-12", "": "4294967295-0"} {
		if got := inbox.Digest([]byte(in)); got != want {
			t.Errorf("Digest(%q) = %s, want %s", in, got, want)
		}
	}
	if _, err := inbox.DigestFile(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("a missing file has no digest")
	}
}

func TestStrictJSON(t *testing.T) {
	good := []string{`{}`, `{"a":{"b":[1,{"c":2}]},"d":"x"}`, `[1,2]`, `"s"`, ` {"a":1} `}
	bad := []string{``, `{`, `{"a":1,"a":2}`, `{"a":1,"a":2}`, `{"x":{"a":1,"a":2}}`, `[{"a":1,"a":1}]`, `{"a":1}{"a":2}`, `{"a":1} x`, `[1,]`, `{"a":[1}`}
	for _, s := range good {
		if err := inbox.StrictJSON([]byte(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range bad {
		if inbox.StrictJSON([]byte(s)) == nil {
			t.Errorf("%s passed", s)
		}
	}
}

func TestDecodeHelpers(t *testing.T) {
	v, err := inbox.Decode([]byte(`{"n":3,"f":3.0,"h":3.5,"s":"x","t":true,"big":1e30}`))
	m := inbox.Object(v)
	if err != nil || m == nil {
		t.Fatal(err)
	}
	if n, ok := inbox.Int(m, "n"); !ok || n != 3 {
		t.Error("int")
	}
	if n, ok := inbox.Int(m, "f"); !ok || n != 3 {
		t.Error("an integral float is an integer, as jq's floor==.")
	}
	for _, k := range []string{"h", "s", "missing"} {
		if _, ok := inbox.Int(m, k); ok {
			t.Errorf("%s is no integer", k)
		}
	}
	if s, ok := inbox.String(m, "s"); !ok || s != "x" || !inbox.NonEmpty(m, "s") || inbox.NonEmpty(m, "n") {
		t.Error("string")
	}
	if !inbox.True(m, "t") || inbox.True(m, "s") {
		t.Error("true")
	}
	if inbox.Object("x") != nil {
		t.Error("object")
	}
	a, _ := inbox.Decode([]byte(`{"x":[1,{"y":2.0}],"z":null,"b":false}`))
	b, _ := inbox.Decode([]byte(`{"b":false,"z":null,"x":[1.0,{"y":2}]}`))
	if !inbox.Equal(a, b) {
		t.Error("jq equality ignores key order and number spelling")
	}
	for _, other := range []string{`{"b":false,"z":null}`, `{"b":false,"z":null,"x":[1,{"y":3}]}`, `{"b":false,"z":null,"x":[1]}`, `{"b":true,"z":null,"x":[1,{"y":2}]}`, `{"b":false,"z":null,"q":[1,{"y":2}]}`} {
		c, _ := inbox.Decode([]byte(other))
		if inbox.Equal(a, c) {
			t.Errorf("equal to %s", other)
		}
	}
	if inbox.Equal([]any{1}, "x") || inbox.Equal(json.Number("1"), "1") || inbox.Equal(map[string]any{}, []any{}) {
		t.Error("types differ")
	}
}

func TestNamesAndNumbers(t *testing.T) {
	for _, n := range []string{"route-adjutant", "a", "a_b-1"} {
		if !inbox.NameOK(n) {
			t.Errorf("%s refused", n)
		}
	}
	for _, n := range []string{"", "A", "1a", "../x", "state", "peers", "card-events", "setup", strings.Repeat("a", 33)} {
		if inbox.NameOK(n) {
			t.Errorf("%s accepted", n)
		}
	}
	if !inbox.NameShape("state") || !inbox.Reserved("state") || inbox.Reserved("x") {
		t.Error("shape and reserved")
	}
	if !inbox.IDOK("m000000000001") || inbox.IDOK("m1") || !inbox.SessionValueOK("a-B_9") || inbox.SessionValueOK("a/b") {
		t.Error("ids")
	}
	for v, ok := range map[string]bool{"0": true, "6": true, "60": true, "61": false, "06": false, "-1": false, "x": false, "123456789": false, "": false} {
		_, err := inbox.Uint("LOCK_WAIT", v, 0, 60)
		if (err == nil) != ok {
			t.Errorf("%q: %v", v, err)
		}
	}
	if _, err := inbox.Uint("LOCK_WAIT", "0", 1, 60); err == nil || err.Error() != "invalid LOCK_WAIT: 0" {
		t.Errorf("message: %v", err)
	}
}

func TestGuards(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	_ = os.MkdirAll(filepath.Join(root, "real", "deep"), 0o700)
	_ = os.WriteFile(filepath.Join(root, "real", "deep", "f"), []byte("x"), 0o600)
	other := t.TempDir()
	_ = os.WriteFile(filepath.Join(other, "f"), []byte("x"), 0o600)
	_ = os.Symlink(other, filepath.Join(root, "link"))
	_ = os.Symlink(filepath.Join(root, "real", "deep", "f"), filepath.Join(root, "real", "lf"))
	if !inbox.GuardPath(root, filepath.Join(root, "real", "deep", "f")) || !inbox.GuardPath(root, "/elsewhere/x") || !inbox.GuardPath("", "/x") {
		t.Error("guard_path refused a real path")
	}
	if inbox.GuardPath(root, filepath.Join(root, "link", "f")) {
		t.Error("guard_path passed a linked folder")
	}
	if inbox.GuardPath(root, filepath.Join(root, "nope", "f")) {
		t.Error("guard_path passed a missing folder")
	}
	if !inbox.Regular(root, filepath.Join(root, "real", "deep", "f")) || inbox.Regular(root, filepath.Join(root, "real", "lf")) ||
		inbox.Regular(root, filepath.Join(root, "link", "f")) || inbox.Regular("", filepath.Join(root, "real")) || !inbox.Regular("", filepath.Join(root, "link", "f")) {
		t.Error("regular")
	}
	big := filepath.Join(root, "real", "big")
	_ = os.WriteFile(big, make([]byte, inbox.MaxSize+1), 0o600)
	if inbox.Bounded(root, big) || !inbox.Bounded(root, filepath.Join(root, "real", "deep", "f")) || inbox.Bounded(root, filepath.Join(root, "none")) {
		t.Error("bounded: the 128 KiB bound")
	}
	if _, ok := inbox.ReadStrict(root, big); ok {
		t.Error("ReadStrict read a file over the bound")
	}
	if err := inbox.SafeLeaf(root, filepath.Join(root, "real", "absent")); err != nil {
		t.Error(err)
	}
	if err := inbox.SafeLeaf(root, filepath.Join(root, "real", "lf")); err == nil || !strings.HasPrefix(err.Error(), "nonregular state leaf: ") {
		t.Errorf("a linked leaf: %v", err)
	}
	if err := inbox.SafeLeaf(root, filepath.Join(root, "link", "x")); err == nil {
		t.Error("a leaf under a linked folder")
	}
	if os.Geteuid() != 0 {
		_ = os.Chmod(filepath.Join(root, "real", "deep", "f"), 0)
		if inbox.Regular(root, filepath.Join(root, "real", "deep", "f")) {
			t.Error("an unreadable file is not regular")
		}
	}
	if !inbox.NoLinks(filepath.Join(root, "real"), filepath.Join(root, "none")) || inbox.NoLinks(filepath.Join(root, "link")) {
		t.Error("no links")
	}
	if !inbox.RealDirs(root, "real", "deep") || inbox.RealDirs(root, "link") || inbox.RealDirs(root, "none") {
		t.Error("real dirs")
	}
	if !inbox.RegularFile(filepath.Join(root, "real", "big")) || inbox.RegularFile(filepath.Join(root, "real", "lf")) || inbox.Exists(filepath.Join(root, "none")) {
		t.Error("regular file")
	}
	if _, err := inbox.ReadBounded(filepath.Join(root, "none"), 10); err == nil {
		t.Error("read bounded")
	}
	if _, err := inbox.ReadBounded(big, 10); err == nil {
		t.Error("read bounded size")
	}
}

func TestCheckDir(t *testing.T) {
	if err := inbox.CheckDir("rel"); err == nil {
		t.Error("relative")
	}
	dir := t.TempDir()
	if err := inbox.CheckDir(filepath.Join(dir, "none")); err == nil {
		t.Error("missing")
	}
	f := filepath.Join(dir, "f")
	_ = os.WriteFile(f, nil, 0o600)
	if err := inbox.CheckDir(f); err == nil {
		t.Error("a file")
	}
	_ = os.Chmod(dir, 0o755)
	if err := inbox.CheckDir(dir); err == nil || !strings.Contains(err.Error(), "755") {
		t.Errorf("mode: %v", err)
	}
	_ = os.Chmod(dir, 0o700)
	if err := inbox.CheckDir(dir); err != nil {
		t.Error(err)
	}
}

func fatal(t *testing.T, err error, want string) {
	t.Helper()
	msg, ok := inbox.IsFatal(err)
	if !ok || !strings.Contains(msg, want) {
		t.Errorf("error %v, want a fatal %q", err, want)
	}
}

func TestOpen(t *testing.T) {
	c := inboxtest.New(t)
	r, err := inbox.Open(c.Options(false))
	if err != nil {
		t.Fatal(err)
	}
	if r.HealthAfter != 180 || r.LockWait != 6 || r.ProofStale || r.TimingStale {
		t.Fatalf("root %+v", r)
	}
	for _, d := range []string{"state", "ids", "enrolled", "alerts"} {
		if st, err := os.Stat(r.Path(d)); err != nil || !st.IsDir() {
			t.Errorf("%s not created", d)
		}
	}
	if !r.Now().Before(time.Now().Add(time.Second)) {
		t.Error("clock")
	}

	o := c.Options(false)
	o.HealthAfter = "0"
	_, err = inbox.Open(o)
	fatal(t, err, "invalid HEALTH_AFTER: 0")
	o = c.Options(false)
	o.LockWait = "61"
	_, err = inbox.Open(o)
	fatal(t, err, "invalid LOCK_WAIT: 61")
	_, err = inbox.Open(inbox.Options{})
	fatal(t, err, "set HERDSMAN_INBOX")
	_, err = inbox.Open(inbox.Options{Dir: "rel"})
	fatal(t, err, "inbox must be absolute")
	_, err = inbox.Open(inbox.Options{Dir: filepath.Join(c.Base, "none")})
	fatal(t, err, "pre-create a private inbox directory")
	_ = os.Symlink(c.Dir, filepath.Join(c.Base, "alias"))
	_, err = inbox.Open(inbox.Options{Dir: filepath.Join(c.Base, "alias")})
	fatal(t, err, "pre-create a private inbox directory")
	_, err = inbox.Open(inbox.Options{Dir: c.Dir + "/../inbox"})
	fatal(t, err, "inbox must be canonical")
	_ = os.Chmod(c.Dir, 0o750)
	_, err = inbox.Open(c.Options(false))
	fatal(t, err, "inbox mode must be 700")
	_ = os.Chmod(c.Dir, 0o500)
	if os.Geteuid() != 0 {
		_, err = inbox.Open(c.Options(false))
		fatal(t, err, "inbox must be owned and writable")
	}
	_ = os.Chmod(c.Dir, 0o700)

	_ = os.Remove(r.Path("alerts"))
	_ = os.WriteFile(r.Path("alerts"), nil, 0o600)
	_, err = inbox.Open(c.Options(false))
	fatal(t, err, "symlink or invalid administrative state")
	_ = os.Remove(r.Path("alerts"))
	_ = os.Mkdir(r.Path("peers"), 0o700)
	_, err = inbox.Open(c.Options(false))
	fatal(t, err, "nonregular state leaf: "+r.Path("peers"))
	_ = os.Remove(r.Path("peers"))
}

func TestOpenTimingAndProof(t *testing.T) {
	c := inboxtest.New(t)
	timing := filepath.Join(c.Dir, "state", "timing.json")
	c.Write(timing, `{"health":60}`)
	r, err := inbox.Open(c.Options(false))
	if err != nil || r.HealthAfter != 60 {
		t.Fatalf("stored timing: %v %+v", err, r)
	}
	o := c.Options(false)
	o.HealthAfter = "60"
	if _, err := inbox.Open(o); err != nil {
		t.Error("an equal supplied timing")
	}
	o.HealthAfter = "90"
	_, err = inbox.Open(o)
	fatal(t, err, "invalid or conflicting shared timing configuration")
	o.ReadOnly = true
	if r, err := inbox.Open(o); err != nil || !r.TimingStale || r.HealthAfter != 90 {
		t.Errorf("read-only conflicting timing: %v %+v", err, r)
	}
	for _, bad := range []string{`{"health":0}`, `{"health":"60"}`, `{"health":1.5}`, `{"health":60,"health":60}`, `x`} {
		c.Write(timing, bad)
		if _, err := inbox.Open(c.Options(false)); err == nil {
			t.Errorf("timing %s passed", bad)
		}
	}
	_ = os.Remove(timing)

	// A stale proof: fatal for a writer, recorded for a reader.
	c.Edit(c.Proof, func(m map[string]any) { m["policy_verified"] = false })
	_, err = inbox.Open(c.Options(false))
	fatal(t, err, "sandbox policy or protected installation proof stale; repeat human setup")
	if r, err := inbox.Open(c.Options(true)); err != nil || !r.ProofStale {
		t.Errorf("read-only stale proof: %v %+v", err, r)
	}
	c.Prove()
	_ = os.RemoveAll(filepath.Join(c.Dir, "ids"))
	_ = os.Symlink(c.Base, filepath.Join(c.Dir, "ids"))
	_, err = inbox.Open(c.Options(false))
	fatal(t, err, "symlink or invalid administrative state")
}

// The setup proof (preflight_valid): message-tests.sh V3c and Z1.
func TestPreflight(t *testing.T) {
	c := inboxtest.New(t)
	open := func() *inbox.Root {
		r, err := inbox.Open(c.Options(true))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if !open().PreflightValid() {
		t.Fatal("the fresh proof is stale")
	}
	cases := []struct {
		name string
		edit func(m map[string]any)
	}{
		{"another inbox", func(m map[string]any) { m["inbox"] = "/x" }},
		{"no verified policy", func(m map[string]any) { delete(m, "policy_verified") }},
		{"no rules", func(m map[string]any) { m["rules"] = []any{} }},
		{"scripts not a list", func(m map[string]any) { m["scripts"] = "x" }},
		{"an extra key in an entry", func(m map[string]any) { m["rules"].([]any)[0].(map[string]any)["x"] = 1 }},
		{"a relative path", func(m map[string]any) { m["rules"].([]any)[0].(map[string]any)["path"] = "rel" }},
		{"a path with a tab", func(m map[string]any) { m["rules"].([]any)[0].(map[string]any)["path"] = "/a\tb" }},
		{"a checksum of the wrong shape", func(m map[string]any) { m["rules"].([]any)[0].(map[string]any)["checksum"] = "12" }},
		{"a checksum that differs", func(m map[string]any) { m["rules"].([]any)[0].(map[string]any)["checksum"] = "1-2" }},
		{"a listed file that is gone", func(m map[string]any) {
			m["rules"].([]any)[0].(map[string]any)["path"] = filepath.Join(c.Base, "gone")
		}},
		{"a script listed twice", func(m map[string]any) { m["scripts"] = append(m["scripts"].([]any), m["scripts"].([]any)[0]) }},
		{"Z1b a script of the folder missing from the proof", func(m map[string]any) {
			var keep []any
			for _, s := range m["scripts"].([]any) {
				if !strings.HasSuffix(s.(map[string]any)["path"].(string), "/message.sh") {
					keep = append(keep, s)
				}
			}
			m["scripts"] = keep
		}},
		{"Z1c an adapter beside the folder missing from the proof", func(m map[string]any) {
			var keep []any
			for _, s := range m["scripts"].([]any) {
				if !strings.HasSuffix(s.(map[string]any)["path"].(string), "/approval-gate.sh") {
					keep = append(keep, s)
				}
			}
			m["scripts"] = keep
		}},
		{"no message-lib.sh listed", func(m map[string]any) {
			var keep []any
			for _, s := range m["scripts"].([]any) {
				if !strings.HasSuffix(s.(map[string]any)["path"].(string), "/message-lib.sh") {
					keep = append(keep, s)
				}
			}
			m["scripts"] = keep
		}},
	}
	for _, tc := range cases {
		c.Prove()
		c.Edit(c.Proof, tc.edit)
		if open().PreflightValid() {
			t.Errorf("%s: the proof passed", tc.name)
		}
	}
	c.Prove()
	c.Write(filepath.Join(c.Base, "skill", "approval-gate.sh"), "#!/bin/bash\n# changed\n")
	if open().PreflightValid() {
		t.Error("Z1d a changed executor adapter makes the proof stale")
	}
	c.Prove()
	if !open().PreflightValid() {
		t.Error("Z1g the proof of the changed bytes is the positive control")
	}
	c.Write(filepath.Join(c.Installed, "existing-route-check.sh"), "#!/bin/bash\nexit 0\n")
	c.Prove()
	if !open().PreflightValid() {
		t.Error("Z1 an installed folder with one more reviewed script still verifies")
	}
	// V3c: an escaped duplicate key in the proof is no strict JSON.
	b, _ := os.ReadFile(c.Proof)
	c.Write(c.Proof, strings.Replace(string(b), `{"inbox":`, `{"policy_verified":true,"inbox":`, 1))
	if open().PreflightValid() {
		t.Error("V3c an escaped duplicate key in the setup proof is refused")
	}
	c.Prove()
	r := open()
	r.Preflight = filepath.Join(c.Base, "setup.json")
	if r.PreflightValid() {
		t.Error("a proof outside the inbox")
	}
	r.Preflight = c.Proof
	r.Installed = filepath.Join(c.Base, "other")
	_ = os.Mkdir(r.Installed, 0o700)
	c.Write(filepath.Join(r.Installed, "x.sh"), "x")
	if r.PreflightValid() {
		t.Error("an installed folder named by the caller must be listed")
	}
	c.Write(c.Proof, "[]")
	if open().PreflightValid() {
		t.Error("a proof that is no object")
	}
}

func TestLocks(t *testing.T) {
	c := inboxtest.New(t)
	slept := 0
	o := c.Options(false)
	o.LockWait = "3"
	o.Sleep = func(time.Duration) { slept++ }
	r, err := inbox.Open(o)
	if err != nil {
		t.Fatal(err)
	}
	l := r.Lock(inbox.ChannelLock)
	if l == nil || l.Dir() != r.Path("channel.lock") {
		t.Fatal("no lock")
	}
	pid, _ := os.ReadFile(filepath.Join(l.Dir(), "pid"))
	if strings.TrimSpace(string(pid)) == "" {
		t.Error("no pid recorded")
	}
	// A held lock (taken by a script or another process) is busy, and
	// waiting never removes it.
	if r.Lock(inbox.ChannelLock) != nil || slept != 2 || !inbox.Exists(l.Dir()) {
		t.Errorf("a held lock: slept %d", slept)
	}
	if err := l.Release(); err != nil || inbox.Exists(l.Dir()) {
		t.Errorf("release: %v", err)
	}
	if (*inbox.Lock)(nil).Release() != nil {
		t.Error("a nil lock")
	}
	// A lock folder the script made, with its pid.
	_ = os.Mkdir(r.Path(inbox.MutationLock), 0o700)
	_ = os.WriteFile(filepath.Join(r.Path(inbox.MutationLock), "pid"), []byte("99999\n"), 0o600)
	if r.Lock(inbox.MutationLock) != nil {
		t.Error("the script's lock was taken")
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(r.Path(inbox.MutationLock), old, old)
	if !inbox.Stale(r.Path(inbox.MutationLock), time.Now(), 180) || inbox.Stale(r.Path(inbox.MutationLock), time.Now(), 7200) || inbox.Stale(r.Path("none"), time.Now(), 1) {
		t.Error("stale")
	}
	// A lock whose pid cannot be written is given back.
	ro := filepath.Join(c.Base, "ro")
	_ = os.Mkdir(ro, 0o500)
	if os.Geteuid() != 0 && inbox.TryDir(filepath.Join(ro, "x.lock"), 1, func(time.Duration) {}) != nil {
		t.Error("a lock in a read-only folder")
	}
}

func TestRegistry(t *testing.T) {
	c := inboxtest.New(t)
	r, _ := inbox.Open(c.Options(false))
	if _, ok := r.Registry(); ok {
		t.Error("no registry passed")
	}
	c.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	peers, ok := r.Registry()
	if !ok || len(peers) != 2 || peers[0].Name != inboxtest.Adjutant || peers[1].Role != "orchestrator" || peers[1].Pane != "receiver-pane" {
		t.Fatalf("peers %+v", peers)
	}
	p, ok := r.Peer(inboxtest.Orchestrator)
	if !ok || p.Identity.Session.Value != "receiver-session" {
		t.Fatalf("peer %+v", p)
	}
	if _, ok := r.Peer("nobody"); ok {
		t.Error("an unknown peer")
	}
	// A change awaiting approval.
	c.Edit(filepath.Join(c.Dir, "peers"), func(m map[string]any) { delete(m, inboxtest.Adjutant) })
	if _, ok := r.Registry(); ok {
		t.Error("a pending change passed")
	}
	_ = os.Remove(filepath.Join(c.Dir, "peers.approved"))
	if _, ok := r.Peer(inboxtest.Orchestrator); ok {
		t.Error("a peer without an approved registry")
	}

	id := func(pane, s string) map[string]any { return inboxtest.Identity(pane, s) }
	entry := func(role string, i map[string]any) map[string]any { return map[string]any{"identity": i, "role": role} }
	valid := map[string]any{"a": entry("adjutant", id("p1", "s1")), "o": entry("orchestrator", id("p2", "s2"))}
	b, _ := json.Marshal(valid)
	if _, ok := inbox.ValidRegistry(b); !ok {
		t.Fatal("valid registry refused")
	}
	if peers, ok := inbox.ValidRegistry([]byte(`{}`)); !ok || len(peers) != 0 {
		t.Error("an empty registry is valid")
	}
	bads := []any{
		map[string]any{"a": entry("adjutant", id("p1", "s1")), "o": entry("orchestrator", id("p2", "s2")), "x": entry("orchestrator", id("p3", "s3"))},
		map[string]any{"state": entry("adjutant", id("p1", "s1"))},
		map[string]any{"a": entry("boss", id("p1", "s1"))},
		map[string]any{"a": entry("adjutant", id("p1", "s1")), "o": entry("adjutant", id("p2", "s2"))},
		map[string]any{"a": entry("adjutant", id("p1", "s1")), "o": entry("orchestrator", id("p1", "s2"))},
		map[string]any{"a": entry("adjutant", id("p1", "s1")), "o": entry("orchestrator", id("p2", "s1"))},
		map[string]any{"a": map[string]any{"identity": id("p1", "s1"), "role": "adjutant", "x": 1}},
		map[string]any{"a": entry("adjutant", map[string]any{"pane": "p1"})},
		map[string]any{"a": entry("adjutant", id("", "s1"))},
		map[string]any{"a": entry("adjutant", id("p1", "a/b"))},
		map[string]any{"a": entry("adjutant", map[string]any{"pane": "p1", "session": map[string]any{"agent": "c", "kind": "path", "source": "h", "value": "v"}})},
		map[string]any{"a": entry("adjutant", map[string]any{"pane": "p1", "session": map[string]any{"agent": 1, "kind": "id", "source": "h", "value": "v"}})},
		[]any{},
	}
	for _, bad := range bads {
		b, _ := json.Marshal(bad)
		if _, ok := inbox.ValidRegistry(b); ok {
			t.Errorf("registry %s passed", b)
		}
	}
	if _, ok := inbox.ValidRegistry([]byte(`{"a":1,"a":2}`)); ok {
		t.Error("duplicate keys")
	}
	if inbox.IdentityOK(json.RawMessage(`{"pane":"p","session":{"agent":"c","kind":"id","source":"h","value":1}}`)) {
		t.Error("a numeric session value")
	}
	if !(inbox.Identity{Pane: "p"}).Same(inbox.Identity{Pane: "p"}) {
		t.Error("same")
	}
}

func TestLiveIdentity(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	c := herdr.New(srv.Socket)
	ctx := context.Background()
	fx.SetAgent("p1", herdrtest.Agent("p1", "s1"))
	id, code, _ := inbox.LiveIdentity(ctx, c, "p1")
	if code != inbox.LiveOK || id.Pane != "p1" || id.Session != (inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s1"}) {
		t.Fatalf("identity %+v %d", id, code)
	}
	if _, code, _ := inbox.LiveIdentity(ctx, c, "gone"); code != inbox.LiveGone {
		t.Errorf("a pane that is gone: %d", code)
	}
	// A15 (02a122d): herdr 0.9.4 sends no launch_pending; a missing or null
	// field counts as not pending, as (.launch_pending // false)==false does.
	for _, v := range []any{nil, "missing"} {
		a := herdrtest.Agent("p1", "s1")
		if v == nil {
			a["launch_pending"] = nil
		} else {
			delete(a, "launch_pending")
		}
		fx.SetAgent("p1", a)
		if _, code, _ := inbox.LiveIdentity(ctx, c, "p1"); code != inbox.LiveOK {
			t.Errorf("launch_pending %v: %d", v, code)
		}
	}
	for _, e := range []struct {
		key string
		v   any
	}{{"interactive_ready", false}, {"launch_pending", true}, {"launch_pending", "false"}, {"agent_status", "unknown"}, {"agent", ""},
		{"terminal_id", 1}, {"workspace_id", nil}, {"tab_id", nil}, {"focused", "no"}, {"revision", -1}, {"revision", 1.5},
		{"pane_id", "p2"}, {"agent_session", nil},
		{"agent_session", map[string]any{"source": "hook", "agent": "claude", "kind": "path", "value": "s1"}},
		{"agent_session", map[string]any{"source": "", "agent": "claude", "kind": "id", "value": "s1"}},
		{"agent_session", map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": "../x"}}} {
		fx.SetAgent("p1", herdrtest.Agent("p1", "s1"))
		fx.Edit("p1", e.key, e.v)
		if _, code, _ := inbox.LiveIdentity(ctx, c, "p1"); code != inbox.LiveUnknown {
			t.Errorf("%s=%v: %d", e.key, e.v, code)
		}
	}
	fx.Fail("agent.get", "temporary_failure")
	if _, code, note := inbox.LiveIdentity(ctx, c, "p1"); code != inbox.LiveUnknown || !strings.HasPrefix(note, "agent get unavailable: ") {
		t.Errorf("an unavailable call: %d %q", code, note)
	}
	srv.Handle("agent.get", func(map[string]any) (any, string) {
		return nil, strings.Repeat("x", 400)
	})
	if _, _, note := inbox.LiveIdentity(ctx, c, "p1"); len(note) > len("agent get unavailable: ")+300 {
		t.Errorf("the note is not cut: %d", len(note))
	}
	srv.Handle("agent.get", func(map[string]any) (any, string) { return map[string]any{"agent": []any{}}, "" })
	if _, code, _ := inbox.LiveIdentity(ctx, c, "p1"); code != inbox.LiveUnknown {
		t.Error("a record that is no object")
	}
}

// Authority (message-authority.sh): message-tests.sh Z2, Z2c, Z2e, Z3, Z3b,
// and the gate's AG12 boundary.
func TestAuthority(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	hc := herdr.New(srv.Socket)
	ctx := context.Background()
	c := inboxtest.New(t)
	c.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	agents := []map[string]any{herdrtest.Agent("sender-pane", "sender-session"), herdrtest.Agent("receiver-pane", "receiver-session"), herdrtest.Agent("task-pane", "task-session")}
	for _, a := range agents {
		fx.SetAgent(a["pane_id"].(string), a)
	}
	authority := func() (string, int) {
		r, err := inbox.Open(c.Options(true))
		if err != nil {
			t.Fatal(err)
		}
		return r.Authority(ctx, hc)
	}
	if line, code := authority(); code != 3 || line != inbox.HumanLine {
		t.Errorf("no boundary recorded: HUMAN; got %q %d", line, code)
	}
	c.RelayBoundary(agents...)
	if line, code := authority(); code != 0 || line != inbox.RelayLine {
		t.Fatalf("Z2 the protected boundary is the positive control: %q %d", line, code)
	}
	// An unknown live agent.
	rogue := herdrtest.Agent("rogue-pane", "rogue-session")
	fx.SetAgent("rogue-pane", rogue)
	r, _ := inbox.Open(c.Options(true))
	ok, fault, unknown := r.BoundaryOK(ctx, hc)
	if ok || fault != inbox.FaultInventory || len(unknown) != 1 || unknown[0].Pane != "rogue-pane" || unknown[0].Agent != "claude" {
		t.Errorf("Z2b an unknown live agent: %v %s %+v", ok, fault, unknown)
	}
	fx.SetAgent("rogue-pane", nil)
	// An exposure record keeps it HUMAN after the agent exits.
	c.Write(filepath.Join(c.Dir, "state", "boundary-tainted"), `{"reason":"x"}`)
	if _, code := authority(); code != 3 {
		t.Error("Z2c after that agent exits the authority stays HUMAN")
	}
	_ = os.Remove(filepath.Join(c.Dir, "state", "boundary-tainted"))
	// A closed lower-trust pane keeps RELAY.
	fx.SetAgent("task-pane", nil)
	if _, code := authority(); code != 0 {
		t.Error("Z2e a closed lower-trust pane keeps RELAY")
	}
	// A lower-trust agent of another harness is unknown.
	fx.SetAgent("task-pane", agents[2])
	fx.Edit("task-pane", "agent", "codex")
	if _, code := authority(); code != 3 {
		t.Error("a lower-trust agent of another harness")
	}
	fx.SetAgent("task-pane", agents[2])
	// An agent with no session.
	noSession := herdrtest.Agent("bare-pane", "x")
	delete(noSession, "agent_session")
	delete(noSession, "agent")
	fx.SetAgent("bare-pane", noSession)
	if ok, _, unknown := func() (bool, string, []inbox.Unknown) {
		r, _ := inbox.Open(c.Options(true))
		return r.BoundaryOK(ctx, hc)
	}(); ok || string(unknown[0].Session) != "null" || unknown[0].Agent != nil {
		t.Errorf("an agent with no session: %v %+v", ok, unknown)
	}
	fx.SetAgent("bare-pane", nil)
	// A courier pane is known.
	fx.SetAgent("courier-pane", herdrtest.Agent("courier-pane", "courier-session"))
	c.Edit(c.Proof, func(m map[string]any) {
		m["boundary"].(map[string]any)["courier_panes"] = []any{inboxtest.Identity("courier-pane", "courier-session")}
	})
	if _, code := authority(); code != 0 {
		t.Error("a courier pane is part of the setup")
	}
	// The setup fields.
	for _, e := range []struct {
		key string
		v   any
	}{{"launch_admission", nil}, {"launch_admission", false}, {"human_checked", false}, {"version", 2}, {"pair", map[string]any{}},
		{"courier_panes", nil}, {"lower_trust", nil},
		{"lower_trust", []any{map[string]any{"sandbox": true, "inbox_denied": true, "socket_denied": true, "code_denied": true, "proof_denied": true, "launch_hash": "", "identity": map[string]any{}}}},
		{"lower_trust", []any{map[string]any{"sandbox": false, "inbox_denied": true, "socket_denied": true, "code_denied": true, "proof_denied": true, "launch_hash": "h", "identity": map[string]any{}}}},
		{"lower_trust", []any{map[string]any{"sandbox": true, "inbox_denied": true, "socket_denied": true, "code_denied": true, "proof_denied": true, "launch_hash": "h"}}}} {
		var saved any
		c.Edit(c.Proof, func(m map[string]any) {
			b := m["boundary"].(map[string]any)
			saved = b[e.key]
			if e.v == nil {
				delete(b, e.key)
			} else {
				b[e.key] = e.v
			}
		})
		if _, code := authority(); code != 3 {
			t.Errorf("Z3 setup field %s=%v: RELAY", e.key, e.v)
		}
		c.Edit(c.Proof, func(m map[string]any) { m["boundary"].(map[string]any)[e.key] = saved })
	}
	c.Edit(c.Proof, func(m map[string]any) { delete(m, "boundary") })
	if _, code := authority(); code != 3 {
		t.Error("no boundary")
	}
	c.RelayBoundary(agents...)
	// No usable agent list.
	fx.Fail("agent.list", "temporary_failure")
	r, _ = inbox.Open(c.Options(true))
	if ok, fault, _ := r.BoundaryOK(ctx, hc); ok || fault != inbox.FaultList {
		t.Errorf("a failed agent list: %s", fault)
	}
	fx.Fail("agent.list", "")
	for _, p := range []string{"sender-pane", "receiver-pane", "task-pane", "courier-pane"} {
		fx.SetAgent(p, nil)
	}
	if ok, fault, _ := r.BoundaryOK(ctx, hc); ok || fault != inbox.FaultList {
		t.Errorf("an empty agent list: %s", fault)
	}
	// A pending registry change, and a stale proof.
	c.Edit(filepath.Join(c.Dir, "peers"), func(m map[string]any) { delete(m, inboxtest.Adjutant) })
	if ok, fault, _ := r.BoundaryOK(ctx, hc); ok || fault != inbox.FaultStatic {
		t.Error("a pending registry")
	}
	r.ProofStale = true
	if ok, fault, _ := r.BoundaryOK(ctx, hc); ok || fault != inbox.FaultStatic {
		t.Error("a stale proof")
	}
}
