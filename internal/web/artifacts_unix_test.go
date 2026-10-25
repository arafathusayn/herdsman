//go:build unix

package web

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// A queued folder swapped for a FIFO before the walk opens it is skipped:
// opening a FIFO to read would wait for a writer forever while the walk
// holds the walk lock, so every later cold listing would wait too.
func TestArtifactWalkSkipsAFolderSwappedForAFIFO(t *testing.T) {
	base := runtime.NumGoroutine()
	root, other := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "page.html"), "p")
	writeFile(t, filepath.Join(root, "sub", "inner.html"), "i")
	writeFile(t, filepath.Join(other, "o.html"), "o")
	sub := filepath.Join(root, "sub")
	a := newArtifacts()
	a.beforeOpen = func(r, dir string) {
		if r == root && dir == "sub" {
			if err := os.RemoveAll(sub); err != nil {
				t.Error(err)
			}
			if err := syscall.Mkfifo(sub, 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	done := make(chan []Artifact, 1)
	go func() { done <- a.find([]string{root}) }()
	select {
	case got := <-done:
		if len(got) != 1 || got[0].Path != "page.html" {
			t.Fatalf("listing = %+v", got)
		}
	case <-time.After(3 * time.Second):
		// Free the blocked open so the test leaves nothing behind.
		if f, err := os.OpenFile(sub, os.O_RDWR, 0); err == nil {
			_ = f.Close()
		}
		<-done
		t.Fatal("the walk blocked opening a FIFO in place of a folder")
	}
	second := make(chan []Artifact, 1)
	go func() { second <- a.find([]string{other}) }()
	select {
	case got := <-second:
		if len(got) != 1 || got[0].Path != "o.html" {
			t.Fatalf("second listing = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a later cold listing waited on the walk")
	}
	for deadline := time.Now().Add(5 * time.Second); runtime.NumGoroutine() > base; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines %d, were %d", runtime.NumGoroutine(), base)
		}
	}
}
