package inbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDigestFileCached(t *testing.T) {
	p := filepath.Join(t.TempDir(), "exe")
	write := func(s string, mtime time.Time) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	fixed := time.Unix(1_700_000_000, 0)
	write("aaaa\n", fixed)
	first, err := DigestFileCached(p)
	if err != nil || first != Digest([]byte("aaaa\n")) {
		t.Fatalf("first digest %q %v", first, err)
	}
	if again, _ := DigestFileCached(p); again != first {
		t.Fatalf("cached digest %q, want %q", again, first)
	}
	// Same size and the modification time set back: the change time moves,
	// so the new bytes are read.
	time.Sleep(20 * time.Millisecond)
	write("bbbb\n", fixed)
	if got, _ := DigestFileCached(p); got != Digest([]byte("bbbb\n")) {
		t.Fatalf("a rewrite kept the old digest: %q", got)
	}
	// A replaced file (a new inode) is read again.
	tmp := p + ".new"
	if err := os.WriteFile(tmp, []byte("cccc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := DigestFileCached(p); got != Digest([]byte("cccc\n")) {
		t.Fatalf("a replaced file kept the old digest: %q", got)
	}
	os.Remove(p)
	if _, err := DigestFileCached(p); err == nil {
		t.Fatal("a removed file has a digest")
	}
	// The cache stays bounded.
	dir := t.TempDir()
	for i := range digestCacheMax * 2 {
		q := filepath.Join(dir, "f"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		os.WriteFile(q, []byte("x"), 0o600)
		DigestFileCached(q)
	}
	digestCache.Lock()
	n := len(digestCache.m)
	digestCache.Unlock()
	if n > digestCacheMax {
		t.Fatalf("cache holds %d entries", n)
	}
}
