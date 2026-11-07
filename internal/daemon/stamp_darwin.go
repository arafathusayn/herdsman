package daemon

import "syscall"

// changeTime is a file's status change time in nanoseconds.
func changeTime(s *syscall.Stat_t) int64 { return s.Ctimespec.Nano() }
