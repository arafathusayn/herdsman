package inbox

import (
	"os"
	"syscall"
	"time"
)

func fileIdentity(fi os.FileInfo) (dev, ino uint64, ctime time.Time, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, time.Time{}, false
	}
	return uint64(st.Dev), st.Ino, time.Unix(st.Ctim.Unix()), true
}
