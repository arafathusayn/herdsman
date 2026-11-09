package config

import "os"

// writableFile is what the writers need of a file.
type writableFile interface {
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Close() error
	Name() string
}

// createTemp and openNew are variables so that a test can make the disk fail.
var (
	createTemp = func(dir, pattern string) (writableFile, error) { return os.CreateTemp(dir, pattern) }
	openNew    = func(path string) (writableFile, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	}
)
