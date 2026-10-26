//go:build !darwin && !linux

package inbox

import (
	"os"
	"time"
)

// fileIdentity has no change time here, so every digest reads the file.
func fileIdentity(os.FileInfo) (dev, ino uint64, ctime time.Time, ok bool) {
	return 0, 0, time.Time{}, false
}
