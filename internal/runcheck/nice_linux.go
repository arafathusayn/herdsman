package runcheck

import "syscall"

// getNice returns this process's niceness. Linux's raw getpriority returns
// 20 minus the niceness.
func getNice() (int, error) {
	p, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	return 20 - p, err
}

func setNice(n int) error { return syscall.Setpriority(syscall.PRIO_PROCESS, 0, n) }
