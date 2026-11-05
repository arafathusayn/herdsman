package daemon

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestStartupLogRefusedHomeWritesNothing: a private home that startup
// refuses gets no log write, so a daemon.log link there never reaches
// its target.
func TestStartupLogRefusedHomeWritesNothing(t *testing.T) {
	ph, home := shortDir(t), shortDir(t)
	if err := os.Mkdir(filepath.Join(ph, "daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "unrelated-file")
	const before = "original-content\n"
	if err := os.WriteFile(target, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(ph, "daemon", "daemon.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	a := &AdminEnv{Stdout: io.Discard, Stderr: &stderr, Version: testVersion}
	if exit := a.run(ph, home); exit == 0 {
		t.Fatal("unsafe home accepted")
	}
	if after, _ := os.ReadFile(target); string(after) != before {
		t.Fatalf("refused startup wrote through the daemon.log link: %q", after)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("mode 700")) {
		t.Errorf("the refusal is not on stderr: %q", stderr.String())
	}
}

// TestStartupLogFIFODoesNotBlock: a FIFO daemon.log never blocks the
// run, refused home or not.
func TestStartupLogFIFODoesNotBlock(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o700} {
		ph := shortDir(t)
		if err := os.Mkdir(filepath.Join(ph, "daemon"), 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(ph, "daemon", "daemon.log")
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(ph, mode); err != nil {
			t.Fatal(err)
		}
		l := newBoundedLog(ph, io.Discard)
		done := make(chan struct{})
		go func() { l.Line("x"); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			f, _ := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			<-done
			_ = f.Close()
			t.Fatalf("home mode %o: the log blocked opening a FIFO daemon.log", mode)
		}
		l.Close()
	}
}

// TestDaemonLogRefusesUnsafeFiles: in an accepted private home, a linked
// daemon.log, a linked or open daemon folder, or a log of another mode
// is not written; the line still goes to stderr.
func TestDaemonLogRefusesUnsafeFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, ph, outside string)
	}{
		{"linked log", func(t *testing.T, ph, outside string) {
			mustDo(t, os.Mkdir(filepath.Join(ph, "daemon"), 0o700))
			mustDo(t, os.Symlink(filepath.Join(outside, "f"), filepath.Join(ph, "daemon", "daemon.log")))
		}},
		{"linked daemon folder", func(t *testing.T, ph, outside string) {
			mustDo(t, os.Symlink(outside, filepath.Join(ph, "daemon")))
		}},
		{"open daemon folder", func(t *testing.T, ph, outside string) {
			mustDo(t, os.Mkdir(filepath.Join(ph, "daemon"), 0o700))
			mustDo(t, os.Chmod(filepath.Join(ph, "daemon"), 0o777))
		}},
		{"log of mode 644", func(t *testing.T, ph, outside string) {
			mustDo(t, os.Mkdir(filepath.Join(ph, "daemon"), 0o700))
			mustDo(t, os.WriteFile(filepath.Join(ph, "daemon", "daemon.log"), nil, 0o600))
			mustDo(t, os.Chmod(filepath.Join(ph, "daemon", "daemon.log"), 0o644))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ph, outside := shortDir(t), shortDir(t)
			mustDo(t, os.WriteFile(filepath.Join(outside, "f"), []byte("keep\n"), 0o600))
			tc.setup(t, ph, outside)
			var echo bytes.Buffer
			l := newBoundedLog(ph, &echo)
			l.Line("secret line")
			l.Close()
			if b, _ := os.ReadFile(filepath.Join(outside, "f")); string(b) != "keep\n" {
				t.Errorf("wrote outside the private home: %q", b)
			}
			if b, _ := os.ReadFile(filepath.Join(ph, "daemon", "daemon.log")); bytes.Contains(b, []byte("secret")) {
				t.Errorf("wrote an unsafe daemon.log: %q", b)
			}
			if echo.String() != "secret line\n" {
				t.Errorf("stderr %q, want the line", echo.String())
			}
		})
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
