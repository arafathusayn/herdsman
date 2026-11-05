package route

import (
	"os"
	"syscall"
	"time"
)

// changeTime is a file's status change time; an edit that puts the
// modification time back still changes it.
func changeTime(fi os.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Ctim.Unix())
	}
	return time.Time{}
}
