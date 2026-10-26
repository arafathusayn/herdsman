//go:build !unix

package web

// openDirFlag and openNonblockFlag are unset where the system has no
// O_DIRECTORY or O_NONBLOCK; herdsman ships for macOS and Linux only.
const (
	openDirFlag      = 0
	openNonblockFlag = 0
)
