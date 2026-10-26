package runcheck

import "syscall"

// getNice returns this process's niceness.
func getNice() (int, error) { return syscall.Getpriority(syscall.PRIO_PROCESS, 0) }

func setNice(n int) error { return syscall.Setpriority(syscall.PRIO_PROCESS, 0, n) }
