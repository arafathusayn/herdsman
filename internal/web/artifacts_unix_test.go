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

// A root swapped for a FIFO after it was accepted, and before the walk
// opens it, is skipped: the open requires a folder, so it cannot wait for a
// writer while the walk holds the walk lock. The other roots of the listing
// are walked, and a later cold listing does not wait.
func TestArtifactWalkSkipsARootSwappedForAFIFO(t *testing.T) {
	base := runtime.NumGoroutine()
	tmp := t.TempDir()
	fifo, other, third := filepath.Join(tmp, "fifo"), filepath.Join(tmp, "other"), filepath.Join(tmp, "third")
	writeFile(t, filepath.Join(fifo, "f.html"), "f")
	writeFile(t, filepath.Join(other, "o.html"), "o")
	writeFile(t, filepath.Join(third, "t.html"), "t")
	a := newArtifacts()
	a.beforeOpen = func(r, dir string) {
		if r == fifo && dir == "" {
			if err := os.RemoveAll(fifo); err != nil {
				t.Error(err)
			}
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	free := func() {
		if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
			_ = f.Close()
		}
	}
	listing := func(roots []string, want string) {
		t.Helper()
		done := make(chan []Artifact, 1)
		go func() { done <- a.find(roots) }()
		select {
		case got := <-done:
			if len(got) != 1 || got[0].Path != want {
				t.Fatalf("listing of %v = %+v, want %s", roots, got, want)
			}
		case <-time.After(3 * time.Second):
			free()
			<-done
			t.Fatalf("the listing of %v blocked on a root swapped for a FIFO", roots)
		}
	}
	listing([]string{fifo, other}, "o.html")
	listing([]string{third}, "t.html")
	for deadline := time.Now().Add(5 * time.Second); runtime.NumGoroutine() > base; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines %d, were %d", runtime.NumGoroutine(), base)
		}
	}
}

// A root that can be read but not searched (mode 0400) is skipped; the
// other roots are listed.
func TestArtifactWalkSkipsAnUnsearchableRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root searches every folder")
	}
	tmp := t.TempDir()
	locked, other := filepath.Join(tmp, "locked"), filepath.Join(tmp, "other")
	writeFile(t, filepath.Join(locked, "l.html"), "l")
	writeFile(t, filepath.Join(other, "o.html"), "o")
	if err := os.Chmod(locked, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	got := newArtifacts().find([]string{locked, other})
	if len(got) != 1 || got[0].Root != other || got[0].Path != "o.html" {
		t.Fatalf("listing = %+v, want only o.html under %s", got, other)
	}
}

// openRefuses checks that opening a page folder for a grant fails promptly
// when beforeOpen swaps the folder at swapDir ("" for the root) for a FIFO
// at that exact moment. A blocked open is freed through the FIFO, so a
// failure cannot hang the suite.
func openRefuses(t *testing.T, swapDir string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pages", "p.html"), "p")
	id, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	dirID, err := os.Stat(filepath.Join(root, "pages"))
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, swapDir)
	hook := func(r, dir string) {
		if r == root && dir == swapDir {
			if err := os.RemoveAll(fifo); err != nil {
				t.Error(err)
			}
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	done := make(chan error, 1)
	go func() {
		r, err := openPageDirWith(root, id, "pages", dirID, hook)
		if err == nil {
			_ = r.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a folder swapped for a FIFO was opened")
		}
	case <-time.After(3 * time.Second):
		if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
			_ = f.Close()
		}
		<-done
		t.Fatalf("opening for a grant blocked on a FIFO in place of %q", swapDir)
	}
}

// A grant's root swapped for a FIFO just before it is opened is refused at
// once instead of blocking the request.
func TestArtifactOpenRefusesARootSwappedForAFIFO(t *testing.T) {
	openRefuses(t, "")
}

// A page folder swapped for a FIFO after its checks and just before it is
// opened is refused at once instead of blocking the request.
func TestArtifactOpenRefusesAPageFolderSwappedForAFIFO(t *testing.T) {
	openRefuses(t, "pages")
}
