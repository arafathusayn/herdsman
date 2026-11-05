//go:build linux

package runcheck

import (
	"bytes"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// procNice is field 19 (the niceness) of a /proc/<pid>/stat line.
func procNice(t *testing.T, stat string) int {
	t.Helper()
	// The command name (field 2) is in parentheses and may hold blanks.
	f := strings.Fields(stat[strings.LastIndexByte(stat, ')')+1:])
	n, err := strconv.Atoi(f[16]) // fields 3.. follow the name: 19 is index 16
	if err != nil {
		t.Fatalf("stat %q: %v", stat, err)
	}
	return n
}

// PR #25 (P2): Linux sets the priority of one thread, and the child is
// forked from the thread that calls Start. Under several threads and
// goroutine churn, every check still starts at the lowered priority.
func TestCheckStartsAtTheLowerPriorityOnLinux(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(4))
	self, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skip("no /proc")
	}
	base := procNice(t, string(self))
	want := min(base+5, 19)
	stop := make(chan struct{})
	defer close(stop)
	for i := 0; i < 8; i++ {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					runtime.Gosched()
				}
			}
		}()
	}
	c := &Config{Nice: 5}
	for i := 0; i < 20; i++ {
		var out, errb bytes.Buffer
		if code := c.start([]string{"/bin/cat", "/proc/self/stat"}, nil, nil, &out, &errb, true); code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		if got := procNice(t, out.String()); got != want {
			t.Fatalf("run %d: the check started at nice %d, want %d", i, got, want)
		}
	}
}
