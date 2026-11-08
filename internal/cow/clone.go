// Package cow duplicates a file tree so that regular files share their blocks
// with the source wherever the file system can do it, and are copied byte by
// byte where it cannot. Files never become hard links of each other: an
// in-place write through one path must not be visible through the other.
package cow

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Method says how one entry reached the destination.
type Method string

const (
	// Shared means the file's blocks are shared with the source until one side writes.
	Shared Method = "shared"
	// Copied means the entry was written out in full, so it cost its own space and time.
	Copied Method = "copied"
)

var (
	// ErrDestinationExists reports a destination path already in use. Nothing is changed.
	ErrDestinationExists = errors.New("destination already exists")

	// ErrDestinationInsideSource reports a destination within the source tree,
	// which a walk of that tree would otherwise reach.
	ErrDestinationInsideSource = errors.New("destination is inside the source")

	// errNoClone is what a clone function returns when a copy should be used instead.
	errNoClone = errors.New("clone unavailable")
)

// Entry is one regular file or symbolic link of the result.
type Entry struct {
	// Path is relative to the source, slash-separated; "." when the source is not a directory.
	Path   string
	Method Method
	// Size is the file's length in bytes, and zero for a symbolic link.
	Size int64
}

// Result lists what Clone did, in path order, and totals it.
type Result struct {
	Entries []Entry
	// Skipped names the pipes, sockets and devices, which are not duplicated.
	Skipped []string

	Shared, Copied           int
	SharedBytes, CopiedBytes int64
}

// Clone duplicates src, a file, a symbolic link or a directory, at dst. Parents
// of dst are created; dst itself must not exist. Modes and modification times
// are kept, except a symbolic link's own time, and links are not followed. When
// Clone fails, a directory it had started at dst is removed.
func Clone(src, dst string) (Result, error) {
	return run(src, dst, platformClone)
}

type cloneFunc func(src, dst string) error

type fileJob struct {
	index    int
	src, dst string
	info     fs.FileInfo
}

type directory struct {
	path string
	info fs.FileInfo
}

func run(src, dst string, clone cloneFunc) (Result, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return Result{}, fmt.Errorf("clone %s: %w", src, err)
	}
	if err := checkDestination(src, dst, info); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o777); err != nil {
		return Result{}, fmt.Errorf("clone to %s: %w", dst, err)
	}

	var result Result
	var jobs []fileJob
	var directories []directory
	walkErr := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		name := filepath.ToSlash(rel)
		switch {
		case info.IsDir():
			// Writable while it fills; the real mode is applied afterwards.
			if err := os.Mkdir(target, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			directories = append(directories, directory{target, info})
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
			result.Entries = append(result.Entries, Entry{Path: name, Method: Copied})
		case info.Mode().IsRegular():
			jobs = append(jobs, fileJob{len(result.Entries), path, target, info})
			result.Entries = append(result.Entries, Entry{Path: name, Size: info.Size()})
		default:
			result.Skipped = append(result.Skipped, name)
		}
		return nil
	})
	err = walkErr
	if err == nil {
		err = cloneFiles(jobs, result.Entries, clone)
	}
	if err == nil {
		err = finishDirectories(directories)
	}
	if err != nil {
		if len(directories) > 0 {
			err = errors.Join(err, os.RemoveAll(dst))
		}
		if errors.Is(err, fs.ErrExist) && len(directories) == 0 && len(jobs) == 0 {
			err = fmt.Errorf("%w: %w", ErrDestinationExists, err)
		}
		return Result{}, fmt.Errorf("clone %s to %s: %w", src, dst, err)
	}

	for _, entry := range result.Entries {
		if entry.Method == Shared {
			result.Shared++
			result.SharedBytes += entry.Size
		} else {
			result.Copied++
			result.CopiedBytes += entry.Size
		}
	}
	return result, nil
}

// checkDestination runs before anything is created, so that a refused clone
// leaves no trace, not even parent directories inside the source.
func checkDestination(src, dst string, info fs.FileInfo) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("clone %s to %s: %w", src, dst, ErrDestinationExists)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clone %s to %s: %w", src, dst, err)
	}
	if !info.IsDir() {
		return nil
	}
	realSrc, err := resolveExisting(src)
	if err != nil {
		return fmt.Errorf("clone %s: %w", src, err)
	}
	realDst, err := resolveExisting(dst)
	if err != nil {
		return fmt.Errorf("clone to %s: %w", dst, err)
	}
	rel, err := filepath.Rel(realSrc, realDst)
	if err != nil {
		return fmt.Errorf("clone %s to %s: %w", src, dst, err)
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("clone %s to %s: %w", src, dst, ErrDestinationInsideSource)
	}
	return nil
}

// resolveExisting follows the symbolic links of the longest part of path that
// exists and appends the remainder, which cannot contain links yet.
func resolveExisting(path string) (string, error) {
	current, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	remainder := ""
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(resolved, remainder), nil
		}
		parent := filepath.Dir(current)
		if !errors.Is(err, fs.ErrNotExist) || parent == current {
			return "", err
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

// cloneFiles fills in each job's entry from a pool of workers and stops
// handing out work at the first failure.
func cloneFiles(jobs []fileJob, entries []Entry, clone cloneFunc) error {
	var next atomic.Int64
	var failed atomic.Bool
	var firstErr error
	var once sync.Once
	var workers sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		workers.Go(func() {
			for !failed.Load() {
				index := int(next.Add(1)) - 1
				if index >= len(jobs) {
					return
				}
				job := jobs[index]
				method, err := cloneOrCopy(job, clone)
				if err != nil {
					once.Do(func() { firstErr = err })
					failed.Store(true)
					return
				}
				entries[job.index].Method = method
			}
		})
	}
	workers.Wait()
	return firstErr
}

func cloneOrCopy(job fileJob, clone cloneFunc) (Method, error) {
	err := clone(job.src, job.dst)
	if err == nil {
		return Shared, nil
	}
	if !errors.Is(err, errNoClone) {
		return "", fmt.Errorf("%s: %w", job.src, err)
	}
	if err := copyFile(job.src, job.dst, job.info); err != nil {
		return "", fmt.Errorf("copy %s: %w", job.src, err)
	}
	return Copied, nil
}

// copyFile writes dst from src. It refuses to overwrite anything, and opens
// without blocking and compares identity so that a source replaced since the
// walk, even by a pipe, is an error and not a hang or the wrong bytes.
func copyFile(src, dst string, info fs.FileInfo) (err error) {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	current, err := in.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return errors.New("source changed during the clone")
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(info.Mode().Perm())
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Chtimes(dst, time.Time{}, info.ModTime())
}

// finishDirectories applies modes and times children first, because creating
// an entry changes its parent's modification time and needs write permission.
func finishDirectories(directories []directory) error {
	for index := len(directories) - 1; index >= 0; index-- {
		entry := directories[index]
		if err := os.Chtimes(entry.path, time.Time{}, entry.info.ModTime()); err != nil {
			return err
		}
		if err := os.Chmod(entry.path, entry.info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}
