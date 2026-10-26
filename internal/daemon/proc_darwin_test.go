package daemon

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestParseProcArgs(t *testing.T) {
	blk := func(argc uint32, rest string) []byte {
		b := binary.LittleEndian.AppendUint32(nil, argc)
		return append(b, rest...)
	}
	p, ok := parseProcArgs(blk(2, "/bin/x\x00\x00\x00a b\x00c\x00K=v w\x00\x00junk"))
	if !ok || !slices.Equal(p.Args, []string{"a b", "c"}) || !slices.Equal(p.Env, []string{"K=v w"}) {
		t.Fatalf("%+v %v", p, ok)
	}
	for _, b := range [][]byte{nil, blk(1, "/bin/x"), blk(3, "/bin/x\x00a\x00")} {
		if _, ok := parseProcArgs(b); ok {
			t.Errorf("%q parsed", b)
		}
	}
}

// TestParseProcArgsEnvEnd: an environment block cut short ends the list.
func TestParseProcArgsEnvEnd(t *testing.T) {
	b := binary.LittleEndian.AppendUint32(nil, 1)
	b = append(b, "/bin/x\x00a\x00K=v\x00L=cut"...)
	p, ok := parseProcArgs(b)
	if !ok || !slices.Equal(p.Env, []string{"K=v"}) {
		t.Fatalf("%+v %v", p, ok)
	}
}

// TestProcIdentityDarwin: this process's identity is its arguments and a
// start time in the past; a pid that no longer exists is an error; a
// zombie is not listed.
func TestProcIdentityDarwin(t *testing.T) {
	args, start, err := procIdentity(os.Getpid())
	if errors.Is(err, syscall.EPERM) {
		t.Skip("sandbox: sysctl denied")
	}
	if err != nil || !slices.Equal(args, os.Args) || !start.Before(time.Now()) || time.Since(start) > time.Hour {
		t.Fatalf("%q %v %v", args, start, err)
	}
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	for i := 0; i < 200; i++ { // until it exits and stays a zombie (not waited for)
		if kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid); err == nil && kp.Proc.P_stat == 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	procs, err := systemProcs()
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(procs, func(p Proc) bool { return p.PID == pid }) {
		t.Fatal("a zombie was listed")
	}
	_ = cmd.Wait()
	if _, _, err := procIdentity(pid); err == nil {
		t.Fatal("an ended process has an identity")
	}
}
