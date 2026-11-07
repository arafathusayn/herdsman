package inbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Source reads for a reader that must not trust the inbox's files (the
// daemon's adoption import): every folder from the inbox down is a real
// folder of this user that group and others cannot write, and the file
// is opened without following a link and without blocking on a FIFO,
// then checked on its descriptor: a regular file of this user, not
// writable by group or others, of at most max bytes. A missing file or
// folder gives an error wrapping os.ErrNotExist.
//
// The walk is anchored to descriptors: each folder is opened relative to
// the descriptor of the folder above it, with no link followed, and
// checked on its own descriptor; the file and a listing are opened under
// the last folder's descriptor. A folder an agent replaces by a link
// after its check is therefore never followed (the check and the use are
// the same open folder).

// sourceBeforeOpen is a test hook: called after the folders are checked
// and before the file or listing is opened under them.
var sourceBeforeOpen func()

// openSourceDir opens root and then each folder below it down to dir, in
// that order, each relative to the one above, so a link or unsafe folder
// is refused before anything below it is looked up. The first missing
// folder wraps os.ErrNotExist: every folder above it has passed, and
// nothing below it exists. The caller closes the descriptor.
func openSourceDir(root, dir string) (*os.File, error) {
	rest, ok := strings.CutPrefix(dir, root)
	if !ok || (rest != "" && rest[0] != '/') {
		return nil, fmt.Errorf("%s is not under %s", dir, root)
	}
	var parts []string
	if rest != "" {
		parts = strings.Split(strings.Trim(rest, "/"), "/")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("%s is not a clean path under %s", dir, root)
		}
	}
	fd, err := sourceOpenDir(unix.AT_FDCWD, root, root)
	if err != nil {
		return nil, err
	}
	p := root
	for _, part := range parts {
		p = filepath.Join(p, part)
		next, err := sourceOpenDir(fd, part, p)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), dir), nil
}

// sourceOpenDir opens name under the folder descriptor at (p is its path,
// for errors) as a folder, never through a link, and checks it on its
// descriptor.
func sourceOpenDir(at int, name, p string) (int, error) {
	fd, err := unix.Openat(at, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		var st unix.Stat_t
		lerr := unix.Fstatat(at, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case errors.Is(err, unix.ENOENT):
			return -1, fmt.Errorf("%s: %w", p, os.ErrNotExist)
		case lerr == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK:
			return -1, fmt.Errorf("%s is a link", p)
		case lerr == nil && st.Mode&unix.S_IFMT != unix.S_IFDIR:
			return -1, fmt.Errorf("%s is not a folder", p)
		}
		return -1, fmt.Errorf("%s: %v", p, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("%s: %v", p, err)
	}
	if err := sourceOwner(int(st.Uid), os.FileMode(st.Mode&0o777), p); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// SourceDir checks root and then each folder below it down to dir (see
// openSourceDir).
func SourceDir(root, dir string) error {
	d, err := openSourceDir(root, dir)
	if err != nil {
		return err
	}
	return d.Close()
}

// ListSource lists the checked folder dir under root, sorted, from its
// own descriptor.
func ListSource(root, dir string) ([]string, error) {
	d, err := openSourceDir(root, dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if sourceBeforeOpen != nil {
		sourceBeforeOpen()
	}
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", dir, err)
	}
	sort.Strings(names)
	return names, nil
}

// ReadSource reads the file p under root as a source file.
func ReadSource(root, p string, max int64) ([]byte, error) {
	d, err := openSourceDir(root, filepath.Dir(p))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if sourceBeforeOpen != nil {
		sourceBeforeOpen()
	}
	fd, err := unix.Openat(int(d.Fd()), filepath.Base(p), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, fmt.Errorf("%s: %w", p, os.ErrNotExist)
		}
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%s is a link", p)
		}
		return nil, fmt.Errorf("%s: %v", p, err)
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	uid := -1
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		uid = int(s.Uid)
	}
	if err := sourceOwner(uid, st.Mode().Perm(), p); err != nil {
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
func sourceOwner(uid int, perm os.FileMode, p string) error {
	if uid != os.Getuid() {
		return fmt.Errorf("%s is not owned by this user", p)
	}
	if perm&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others (mode %o)", p, perm)
	}
	return nil
}
