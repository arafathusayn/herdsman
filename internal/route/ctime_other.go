//go:build !darwin && !linux

package route

import (
	"os"
	"time"
)

// changeTime is the zero time where the system gives no change time: there
// the file, size and modification time decide.
func changeTime(os.FileInfo) time.Time { return time.Time{} }
