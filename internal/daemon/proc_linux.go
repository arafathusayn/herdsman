package daemon

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// systemProcs lists the user's processes with their argument vectors and
// environments from /proc (12.2). A process that ends while it is read
// is skipped; one whose arguments cannot be read counts as unreadable
// (busy), and one whose environment cannot be read as matching.
func systemProcs() ([]Proc, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var procs []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := "/proc/" + e.Name()
		st, err := os.Stat(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // ended
			}
			procs = append(procs, Proc{PID: pid, Unreadable: true})
			continue
		}
		if s, ok := st.Sys().(*syscall.Stat_t); !ok || int(s.Uid) != os.Getuid() {
			continue
		}
		p, gone := readProc(dir, pid)
		if !gone {
			procs = append(procs, p)
		}
	}
	return procs, nil
}

// readProc reads one /proc/<pid>; gone reports that it ended.
func readProc(dir string, pid int) (p Proc, gone bool) {
	p.PID = pid
	cmd, err := os.ReadFile(dir + "/cmdline")
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH):
		return p, true
	case err != nil:
		p.Unreadable = true
		return p, false
	case len(cmd) == 0:
		return p, true // a zombie or a kernel thread: it runs nothing
	}
	p.Args = splitNUL(cmd)
	if env, err := os.ReadFile(dir + "/environ"); err == nil {
		p.Env = splitNUL(env)
	}
	return p, false
}

// procIdentity is a process's argument vector and start time: field 22
// of /proc/<pid>/stat (clock ticks after boot) plus the boot time.
func procIdentity(pid int) ([]string, time.Time, error) {
	dir := "/proc/" + strconv.Itoa(pid)
	p, gone := readProc(dir, pid)
	if gone {
		return nil, time.Time{}, syscall.ESRCH
	}
	if p.Unreadable {
		return nil, time.Time{}, errors.New("unreadable arguments")
	}
	stat, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return nil, time.Time{}, err
	}
	// The command name in parentheses may hold spaces: fields start after
	// the last ')'.
	i := bytes.LastIndexByte(stat, ')')
	f := strings.Fields(string(stat[i+1:]))
	if i < 0 || len(f) < 20 {
		return nil, time.Time{}, errors.New("unreadable stat")
	}
	ticks, err := strconv.ParseInt(f[19], 10, 64)
	if err != nil {
		return nil, time.Time{}, err
	}
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil, time.Time{}, err
	}
	var boot int64
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			boot, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	if boot == 0 {
		return nil, time.Time{}, errors.New("no boot time")
	}
	const hz = 100 // USER_HZ on every Linux the release builds for
	return p.Args, time.Unix(boot+ticks/hz, (ticks%hz)*int64(time.Second/hz)), nil
}

// splitNUL splits a NUL-separated block (an argument vector or an
// environment) into its strings.
func splitNUL(b []byte) []string {
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return []string{}
	}
	return strings.Split(string(b), "\x00")
}
