//go:build !unix

package web

// openDirFlag is unset where the system has no O_DIRECTORY; herdsman ships
// for macOS and Linux only.
const openDirFlag = 0
