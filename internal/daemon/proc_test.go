package daemon

import (
	"errors"
	"os"
	"slices"
	"syscall"
	"testing"
)

// TestSystemProcsNative: the system's process list carries this test's
// own argument vector and environment as separate strings, so a value
// with spaces (a private home with a space) stays one string.
func TestSystemProcsNative(t *testing.T) {
	t.Setenv("HERDSMAN_INBOX", "/a private home/repo-r")
	procs, err := systemProcs()
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skip("sandbox: the process list was denied")
	}
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(procs, func(p Proc) bool { return p.PID == os.Getpid() })
	if i < 0 {
		t.Fatal("this process is not listed")
	}
	p := procs[i]
	if p.Unreadable {
		t.Skip("sandbox: this process's arguments were denied")
	}
	if !slices.Equal(p.Args, os.Args) {
		t.Fatalf("args %q, want %q", p.Args, os.Args)
	}
	// The environment is the one at exec; t.Setenv changed only ours, so
	// check a variable the process started with.
	if home := os.Getenv("PATH"); home != "" && !slices.Contains(p.Env, "PATH="+home) {
		t.Fatalf("env misses PATH")
	}
}

// TestDrainSpacedInbox: a channel command whose HERDSMAN_INBOX has a
// space matches that inbox exactly, and no other.
func TestDrainSpacedInbox(t *testing.T) {
	h := newHarness(t)
	dir := "/a private home/repo-r"
	h.d.cfg.Procs = func() ([]Proc, error) {
		return []Proc{{PID: 4242, Args: []string{"/usr/local/bin/herdsman", "message", "read", "tok-secret"}, Env: []string{"HERDSMAN_INBOX=" + dir, "HERDSMAN_CREDENTIAL=s3cret"}}}, nil
	}
	busy := h.d.drain(t.Context(), dir, false)
	if len(busy) != 1 || busy[0] != "pid 4242 herdsman message read" {
		t.Fatalf("busy %q", busy)
	}
	if b := h.d.drain(t.Context(), "/a private home/repo", false); len(b) != 0 {
		t.Fatalf("another inbox: %q", b)
	}
	// A process whose arguments cannot be read counts as busy.
	h.d.cfg.Procs = func() ([]Proc, error) { return []Proc{{PID: 7, Unreadable: true}}, nil }
	if b := h.d.drain(t.Context(), dir, false); len(b) != 1 || b[0] != "pid 7 (arguments unreadable)" {
		t.Fatalf("unreadable: %q", b)
	}
}

// TestProcLabelRedacts: the busy label keeps the pid, the executable and
// the action, never a token or a value from the environment.
func TestProcLabelRedacts(t *testing.T) {
	for _, c := range []struct {
		p    Proc
		want string
	}{
		{Proc{PID: 1, Command: "herdsman message ack tok-1 m1 rec"}, "pid 1 herdsman message ack"},
		{Proc{PID: 2, Command: "/bin/bash /x/message-courier.sh --token t"}, "pid 2 bash message-courier.sh"},
		{Proc{PID: 3, Args: []string{"/x/herdsman", "message"}}, "pid 3 herdsman"},
		{Proc{PID: 4, Args: []string{}}, "pid 4"},
	} {
		if got := c.p.label(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.p, got, c.want)
		}
	}
}
