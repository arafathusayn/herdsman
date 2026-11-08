package sys

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

var (
	// ErrCloneUnsupported reports that the two paths cannot share blocks: they
	// are on different volumes, or the filesystem has no clones. The caller
	// copies instead. The destination does not exist after this error.
	ErrCloneUnsupported = errors.New("copy-on-write clone not supported here")

	// ErrNotRegular reports a source that is not a regular file.
	ErrNotRegular = errors.New("not a regular file")
)

// CloneFile creates dst as a copy-on-write clone of the regular file src, with
// src's permission bits and modification time, owned by the caller. Writing to
// either file later leaves the other unchanged. A dst that exists is an error
// and is not touched. A symbolic link is not followed.
func CloneFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return wrapCloneError(src, dst, err)
	}
	if !info.Mode().IsRegular() {
		return wrapCloneError(src, dst, ErrNotRegular)
	}
	if err := cloneFile(src, dst, info); err != nil {
		return wrapCloneError(src, dst, err)
	}
	return nil
}

// wrapCloneError names both paths and marks the errors a copy can recover from.
func wrapCloneError(src, dst string, err error) error {
	for _, unsupported := range []error{syscall.EXDEV, syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.ENOTTY, syscall.EINVAL, syscall.ENOSYS} {
		if errors.Is(err, unsupported) {
			return fmt.Errorf("clone %s to %s: %w: %w", src, dst, ErrCloneUnsupported, err)
		}
	}
	return fmt.Errorf("clone %s to %s: %w", src, dst, err)
}
