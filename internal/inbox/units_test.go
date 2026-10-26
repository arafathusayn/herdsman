package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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
