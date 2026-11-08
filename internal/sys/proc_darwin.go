package sys

import (
	"time"

	"golang.org/x/sys/unix"
)

func processStart(pid int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		// The kernel answers a missing pid with an empty result, which the
		// wrapper reports as EIO.
		return time.Time{}, mapGone(err, unix.ESRCH, unix.EIO)
	}
	// The pid travels as 32 bits: the answer can be for another process.
	if int(kp.Proc.P_pid) != pid {
		return time.Time{}, ErrNoProcess
	}
	st := kp.Proc.P_starttime
	return time.Unix(st.Sec, int64(st.Usec)*int64(time.Microsecond)), nil
}
