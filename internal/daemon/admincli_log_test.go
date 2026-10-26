package daemon

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAskTimeout(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 5 * time.Minute, "90": 90 * time.Second, "007": 7 * time.Second, "2m": 2 * time.Minute, "3h": 3 * time.Hour, "1d": 24 * time.Hour, "9d": 24 * time.Hour, "99999999999999999h": 24 * time.Hour} {
		if got, err := askTimeout(v); err != nil || got != want {
			t.Errorf("%q: %v %v", v, got, err)
		}
	}
}

// TestDaemonLogBounded: the daemon log keeps at most logMax bytes plus
// one previous file, however many lines are written.
func TestDaemonLogBounded(t *testing.T) {
	ph := t.TempDir()
	var echo bytes.Buffer
	l := newBoundedLog(ph, &echo)
	l.max = 4096
	line := strings.Repeat("x", 99)
	for range 1000 {
		l.Line(line)
	}
	l.Line(strings.Repeat("y", 10000)) // one line longer than the bound
	l.Close()
	ents, _ := os.ReadDir(filepath.Join(ph, "daemon"))
	if len(ents) != 2 {
		t.Fatalf("files %v", ents)
	}
	for _, e := range ents {
		st, _ := os.Stat(filepath.Join(ph, "daemon", e.Name()))
		if st.Size() > l.max || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %d bytes %v", e.Name(), st.Size(), st.Mode())
		}
	}
	if echo.Len() != 1000*100+10001 {
		t.Fatalf("stderr copy %d", echo.Len())
	}
	// An existing log over the bound rotates before the first new line.
	big := filepath.Join(ph, "daemon", "daemon.log")
	_ = os.WriteFile(big, bytes.Repeat([]byte("z"), 5000), 0o600)
	l = newBoundedLog(ph, io.Discard)
	l.max = 4096
	l.Line("new")
	l.Close()
	if b, _ := os.ReadFile(big); string(b) != "new\n" {
		t.Fatalf("not rotated: %d bytes", len(b))
	}
}
