package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Small units the command-level cases of inbox_test.go do not reach.
func TestUnits(t *testing.T) {
	for c, want := range map[string]bool{"12-34": true, "-5": false, "a-5": false, "5": false, "5-": false} {
		if checksumShape(c) != want {
			t.Errorf("checksumShape(%q) = %v", c, !want)
		}
	}
	if KeysExactly([]byte(`{"a":1,"c":2}`), "a", "b") {
		t.Error("KeysExactly: a missing key with the same count")
	}
	if IdentityOK([]byte(`{"pane":"p1","session":{"agent":"claude","kind":"id","source":"hook"}}`)) {
		t.Error("IdentityOK: a session without a value")
	}
	if (&Fatal{Msg: "m"}).Error() != "m" {
		t.Error("Fatal text")
	}
	if _, ok := IsFatal(errors.New("x")); ok {
		t.Error("IsFatal of a plain error")
	}
	p := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(p, []byte("abc"), 0o600)
	if b, err := ReadBounded(p, 3); err != nil || string(b) != "abc" {
		t.Errorf("ReadBounded: %q %v", b, err)
	}
	if _, err := ReadBounded(p, 2); err == nil {
		t.Error("ReadBounded: over the bound")
	}
	if _, err := ReadBounded(p+".none", 2); err == nil {
		t.Error("ReadBounded: a missing file")
	}
}

// Review head-2 P1: the size and type checks held for the path, and the
// read went through a second lookup, so a file replaced or grown in
// between was read whole. Each case changes the file between the checks
// and the read; ReadStrict and ReadBounded must refuse what they read.
func TestBoundDuringReplacement(t *testing.T) {
	big := []byte(`"` + strings.Repeat("a", 2*MaxSize) + `"`)
	for _, tc := range []struct {
		name   string
		change func(p string)
	}{
		{"replaced by rename", func(p string) {
			tmp := p + ".new"
			_ = os.WriteFile(tmp, big, 0o600)
			_ = os.Rename(tmp, p)
		}},
		{"grown in place", func(p string) {
			f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
			_, _ = f.Write([]byte(strings.Repeat(" ", 2*MaxSize)))
			_ = f.Close()
		}},
		{"replaced by a link", func(p string) {
			target := p + ".target"
			_ = os.WriteFile(target, []byte(`{}`), 0o600)
			_ = os.Remove(p)
			_ = os.Symlink(target, p)
		}},
		{"replaced by a fifo", func(p string) {
			_ = os.Remove(p)
			_ = syscall.Mkfifo(p, 0o600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			p := filepath.Join(root, "f.json")
			if err := os.WriteFile(p, []byte(`{"a":1}`), 0o600); err != nil {
				t.Fatal(err)
			}
			beforeOpen = func(string) { tc.change(p) }
			defer func() { beforeOpen = nil }()
			if b, ok := ReadStrict(root, p); ok {
				t.Errorf("ReadStrict accepted %d bytes", len(b))
			}
			if err := os.WriteFile(p+".2", []byte(`{"a":1}`), 0o600); err != nil {
				t.Fatal(err)
			}
			p = p + ".2"
			if b, err := ReadBounded(p, MaxSize); err == nil {
				t.Errorf("ReadBounded accepted %d bytes", len(b))
			}
		})
	}
}

// Review port-core-11 P1: ReadStrict's path check (Regular) opened the
// leaf with a blocking open after a separate Lstat, so a leaf replaced by
// a fifo in between hung the reader. Leaves flip between a regular file
// and a fifo while ReadStrict reads; the reader must keep going.
func TestReviewStrictFIFOReplacementDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	small, fifo, p := filepath.Join(dir, "small"), filepath.Join(dir, "fifo"), filepath.Join(dir, "current.json")
	if err := os.WriteFile(small, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(small, p); err != nil {
		t.Fatal(err)
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, src := range []string{fifo, small} {
				_ = os.Link(src, p+".next")
				_ = os.Rename(p+".next", p)
			}
		}
	}()
	var progress atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 100000; i++ {
			ReadStrict(dir, p)
			progress.Store(int64(i))
		}
	}()
	prev, same, blocked := int64(-1), 0, false
	for i := 0; i < 100 && !blocked; i++ {
		select {
		case <-done:
			i = 100
			continue
		case <-time.After(20 * time.Millisecond):
		}
		n := progress.Load()
		if n == prev {
			same++
		} else {
			same = 0
		}
		prev = n
		blocked = same >= 10
	}
	close(stop)
	<-stopped
	_ = os.Remove(p)
	_ = os.Link(small, p)
	// Open a writer on the fifo to release a blocked open, then join the
	// reader before cleaning up.
	f, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the reader did not stop after the fifo was released")
	}
	if blocked {
		t.Errorf("ReadStrict blocked on a fifo after %d reads", prev)
	}
}

// Review core-followups-1 P1: Regular's open must refuse the leaf it
// actually opens. A leaf swapped after the Lstat for a link to a regular
// file (refused by O_NOFOLLOW), a fifo or a folder (refused by the
// descriptor's type) is not regular.
func TestRegularChecksTheOpenedLeaf(t *testing.T) {
	for _, kind := range []string{"link", "fifo", "folder"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			p, target := filepath.Join(dir, "leaf"), filepath.Join(dir, "target")
			for _, f := range []string{p, target} {
				if err := os.WriteFile(f, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			beforeRegularOpen = func(string) {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "link":
					err = os.Symlink(target, p)
				case "fifo":
					err = syscall.Mkfifo(p, 0o600)
				case "folder":
					err = os.Mkdir(p, 0o700)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			defer func() { beforeRegularOpen = nil }()
			if Regular(dir, p) {
				t.Errorf("Regular accepted a %s swapped in after its Lstat", kind)
			}
		})
	}
	// Unswapped, the same regular leaf passes.
	dir := t.TempDir()
	p := filepath.Join(dir, "leaf")
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Regular(dir, p) {
		t.Error("Regular refused a regular leaf")
	}
}
