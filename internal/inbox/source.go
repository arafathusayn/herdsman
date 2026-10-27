package inbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Source reads for a reader that must not trust the inbox's files (the
// daemon's adoption import): every folder from the inbox down is a real
// folder of this user that group and others cannot write, and the file
// is opened without following a link and without blocking on a FIFO,
// then checked on its descriptor: a regular file of this user, not
// writable by group or others, of at most max bytes. A missing file or
// folder gives an error wrapping os.ErrNotExist.

// SourceDir checks root and then each folder below it down to dir, in
// that order, so a link or unsafe folder is refused before anything below
// it is looked up. The first missing folder wraps os.ErrNotExist: every
// folder above it has passed, and nothing below it exists.
func SourceDir(root, dir string) error {
	rest, ok := strings.CutPrefix(dir, root)
	if !ok || (rest != "" && rest[0] != '/') {
		return fmt.Errorf("%s is not under %s", dir, root)
	}
	p := root
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if rest == "" {
		parts = nil
	}
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			if parts[i] == "" || parts[i] == "." || parts[i] == ".." {
				return fmt.Errorf("%s is not a clean path under %s", dir, root)
			}
			p = filepath.Join(p, parts[i])
		}
		st, err := os.Lstat(p)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("%s: %w", p, os.ErrNotExist)
		case err != nil:
			return err
		case st.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s is a link", p)
		case !st.IsDir():
			return fmt.Errorf("%s is not a folder", p)
		}
		if err := sourceOwner(st, p); err != nil {
			return err
		}
	}
	return nil
}

// ReadSource reads the file p under root as a source file.
func ReadSource(root, p string, max int64) ([]byte, error) {
	if err := SourceDir(root, filepath.Dir(p)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", p, os.ErrNotExist)
		}
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a link", p)
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	if err := sourceOwner(st, p); err != nil {
		return nil, err
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, max)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, max)
	}
	return b, nil
}

// sourceOwner requires this user's file or folder that group and others
// cannot write.
func sourceOwner(st os.FileInfo, p string) error {
	if s, ok := st.Sys().(*syscall.Stat_t); ok && int(s.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by this user", p)
	}
	if st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others (mode %o)", p, st.Mode().Perm())
	}
	return nil
}
