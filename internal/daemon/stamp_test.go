package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rewriteSameLength rewrites p in place with b (the same length as the
// old bytes), then sets its modification time back: only the change time
// shows the rewrite. The pause lets a coarse file-system clock move.
func rewriteSameLength(t *testing.T, p string, b []byte) {
	t.Helper()
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(p)
	if err != nil || after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) || inode(after) != inode(st) {
		t.Fatalf("the rewrite changed more than the bytes: %v", err)
	}
}

// TestAckCacheSeesRestoredMtime: a same-length rewrite of an acknowledged
// message with its modification time set back is checked again, so the
// changed message counts as unacknowledged.
func TestAckCacheSeesRestoredMtime(t *testing.T) {
	x := newDirect(t)
	a, _ := x.send("one", key())
	cl, e := x.r.claim("orch", ClaimRequest{ReadKey: key()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.ack("orch", cl.Claim, AckRequest{ID: a.ID, Record: "n"}); e != nil {
		t.Fatal(e)
	}
	box := x.path("orch")
	if u := x.r.unacked(box); len(u) != 0 || len(x.r.acks.m) != 1 {
		t.Fatalf("unacked %v, cache %v", u, x.r.acks.m)
	}
	f := x.path("orch", "cur", a.ID+".md")
	b, err := os.ReadFile(f)
	if err != nil || len(b) == 0 {
		t.Fatal(err)
	}
	c := append([]byte{}, b...)
	c[len(c)-1] ^= 1
	rewriteSameLength(t, f, c)
	if u := x.r.unacked(box); len(u) != 1 {
		t.Fatalf("a same-length rewrite with the old mtime kept the cached ack: %v", u)
	}
}

// TestHashCacheSeesRestoredMtime: the setup hash cache rereads a policy
// rewritten in place to the same length with its modification time set
// back.
func TestHashCacheSeesRestoredMtime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "policy")
	if err := os.WriteFile(p, []byte("allow a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var c hashCache
	first, err := c.sum(p)
	if err != nil || first != sha256Hex([]byte("allow a\n")) {
		t.Fatalf("%q %v", first, err)
	}
	rewriteSameLength(t, p, []byte("allow b\n"))
	if got, err := c.sum(p); err != nil || got != sha256Hex([]byte("allow b\n")) {
		t.Fatalf("a same-length rewrite with the old mtime kept the old hash: %q %v", got, err)
	}
}
