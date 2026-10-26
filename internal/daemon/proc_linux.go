package daemon

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"syscall"
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
