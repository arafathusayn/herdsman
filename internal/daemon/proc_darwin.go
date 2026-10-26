package daemon

import (
	"encoding/binary"
	"errors"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// systemProcs lists the user's processes with their argument vectors and
// environments natively, sysctl KERN_PROC_UID then KERN_PROCARGS2 per
// process (12.2), so an argument or a HERDSMAN_INBOX with spaces stays
// one string. A process that ends while it is read, or a zombie, is
// skipped; one whose arguments cannot be read counts as unreadable
// (busy).
func systemProcs() ([]Proc, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, err
	}
	var procs []Proc
	for _, kp := range kps {
		pid := int(kp.Proc.P_pid)
		if pid <= 0 || kp.Proc.P_stat == 5 { // SZOMB
			continue
		}
		raw, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			if errors.Is(err, syscall.ESRCH) || !alive(pid) {
				continue
			}
			procs = append(procs, Proc{PID: pid, Unreadable: true})
			continue
		}
		p, ok := parseProcArgs(raw)
		if !ok {
			procs = append(procs, Proc{PID: pid, Unreadable: true})
			continue
		}
		p.PID = pid
		procs = append(procs, p)
	}
	return procs, nil
}

// alive reports whether a process still exists.
func alive(pid int) bool { return syscall.Kill(pid, 0) != syscall.ESRCH }

// parseProcArgs reads a KERN_PROCARGS2 block: argc (32 bits), the
// executable path, NUL padding, argc arguments, then the environment.
func parseProcArgs(b []byte) (Proc, bool) {
	if len(b) < 4 {
		return Proc{}, false
	}
	argc := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	next := func() (string, bool) {
		for i, c := range b {
			if c == 0 {
				s := string(b[:i])
				b = b[i+1:]
				return s, true
			}
		}
		return "", false
	}
	if _, ok := next(); !ok { // the executable path
		return Proc{}, false
	}
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	p := Proc{Args: make([]string, 0, argc), Env: []string{}}
	for range argc {
		a, ok := next()
		if !ok {
			return Proc{}, false
		}
		p.Args = append(p.Args, a)
	}
	for len(b) > 0 && b[0] != 0 {
		e, ok := next()
		if !ok {
			break
		}
		p.Env = append(p.Env, e)
	}
	return p, true
}

// procIdentity is a process's argument vector and start time.
func procIdentity(pid int) ([]string, time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return nil, time.Time{}, err
	}
	if int(kp.Proc.P_pid) != pid {
		return nil, time.Time{}, syscall.ESRCH
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, time.Time{}, err
	}
	p, ok := parseProcArgs(raw)
	if !ok {
		return nil, time.Time{}, errors.New("unreadable arguments")
	}
	st := kp.Proc.P_starttime
	return p.Args, time.Unix(int64(st.Sec), int64(st.Usec)*1000), nil
}
