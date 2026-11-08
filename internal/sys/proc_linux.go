package sys

import "time"

func processStart(pid int) (time.Time, error) {
	return startFromProc("/proc", pid)
}
