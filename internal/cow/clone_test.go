//go:build darwin || linux

package cow

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var stamp = time.Date(2021, 3, 4, 5, 6, 7, 123456789, time.UTC)

func noClone(string, string) error { return errNoClone }

func write(t *testing.T, path, body string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// sampleTree lays out one of each kind of entry, including the read-only
// directory that module caches contain.
func sampleTree(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	write(t, filepath.Join(src, "alpha.txt"), "alpha", 0o644)
	write(t, filepath.Join(src, "nested", "tool.sh"), "#!/bin/sh\n", 0o755)
	write(t, filepath.Join(src, "locked", "pinned.txt"), "pinned", 0o444)
	if err := os.Mkdir(filepath.Join(src, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("alpha.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("no/such/target", filepath.Join(src, "dangling")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"locked", "nested", "empty"} {
		if err := os.Chtimes(filepath.Join(src, dir), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(src, "locked")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	return src
}

func unlockAfter(t *testing.T, dir string) {
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func assertSameTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		want, err := entry.Info()
		if err != nil {
			return err
		}
		got, err := os.Lstat(filepath.Join(dst, rel))
		if err != nil {
			return err
		}
		if got.Mode() != want.Mode() {
			t.Errorf("%s: mode %v, want %v", rel, got.Mode(), want.Mode())
		}
		switch {
		case want.Mode()&fs.ModeSymlink != 0:
			from, _ := os.Readlink(path)
			to, _ := os.Readlink(filepath.Join(dst, rel))
			if from != to {
				t.Errorf("%s: link %q, want %q", rel, to, from)
			}
		case want.Mode().IsRegular():
			if read(t, path) != read(t, filepath.Join(dst, rel)) {
				t.Errorf("%s: contents differ", rel)
			}
			if os.SameFile(want, got) {
				t.Errorf("%s: source and destination are one inode", rel)
			}
			if links := got.Sys().(*syscall.Stat_t).Nlink; links != 1 {
				t.Errorf("%s: %d links, want 1", rel, links)
			}
		}
		if rel != "." && !got.ModTime().Equal(want.ModTime()) {
			t.Errorf("%s: mtime %v, want %v", rel, got.ModTime(), want.ModTime())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCloneReproducesEveryEntryKind(t *testing.T) {
	src := sampleTree(t)
	dst := filepath.Join(t.TempDir(), "deeper", "dst")

	result, err := Clone(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	unlockAfter(t, filepath.Join(dst, "locked"))
	assertSameTree(t, src, dst)

	var paths []string
	for _, entry := range result.Entries {
		paths = append(paths, entry.Path)
	}
	want := []string{"alpha.txt", "dangling", "link", "locked/pinned.txt", "nested/tool.sh"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("entries %v, want %v", paths, want)
	}
	if result.Shared+result.Copied != len(want) {
		t.Fatalf("shared %d + copied %d, want %d", result.Shared, result.Copied, len(want))
	}
}

func TestCloneOnAPFSSharesRegularFilesAndWritesStayApart(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the temporary directory is only known to clone on APFS")
	}
	src := filepath.Join(t.TempDir(), "src")
	write(t, filepath.Join(src, "a.bin"), "original", 0o644)
	write(t, filepath.Join(src, "sub", "b.bin"), "second", 0o644)
	dst := filepath.Join(t.TempDir(), "dst")

	result, err := Clone(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if result.Shared != 2 || result.Copied != 0 {
		t.Fatalf("shared %d copied %d, want 2 and 0", result.Shared, result.Copied)
	}
	if result.SharedBytes != int64(len("original")+len("second")) {
		t.Fatalf("shared bytes %d", result.SharedBytes)
	}
	for _, entry := range result.Entries {
		if entry.Method != Shared {
			t.Fatalf("%s: %s", entry.Path, entry.Method)
		}
	}
	overwriteInPlace(t, filepath.Join(dst, "a.bin"), "CHANGED!")
	if got := read(t, filepath.Join(src, "a.bin")); got != "original" {
		t.Fatalf("source changed to %q", got)
	}
}

func overwriteInPlace(t *testing.T, path, body string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte(body), 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackCopiesAndReportsCopied(t *testing.T) {
	src := sampleTree(t)
	dst := filepath.Join(t.TempDir(), "dst")

	result, err := run(src, dst, noClone)
	if err != nil {
		t.Fatal(err)
	}
	unlockAfter(t, filepath.Join(dst, "locked"))
	assertSameTree(t, src, dst)
	if result.Shared != 0 || result.Copied != 5 {
		t.Fatalf("shared %d copied %d", result.Shared, result.Copied)
	}
	if want := int64(len("alpha") + len("pinned") + len("#!/bin/sh\n")); result.CopiedBytes != want {
		t.Fatalf("copied bytes %d, want %d", result.CopiedBytes, want)
	}
}

func TestSingleFileSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "only.txt")
	write(t, src, "only", 0o640)
	dst := filepath.Join(t.TempDir(), "made", "for", "you.txt")

	result, err := run(src, dst, noClone)
	if err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, src, dst)
	if len(result.Entries) != 1 || result.Entries[0].Path != "." || result.Entries[0].Method != Copied {
		t.Fatalf("entries %+v", result.Entries)
	}
}

func TestSocketsAndPipesAreListedNotOpened(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	write(t, filepath.Join(src, "keep.txt"), "keep", 0o644)
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")

	result, err := run(src, dst, noClone)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Skipped, []string{"pipe"}) {
		t.Fatalf("skipped %v", result.Skipped)
	}
	if _, err := os.Lstat(filepath.Join(dst, "pipe")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("pipe was recreated: %v", err)
	}
}

func TestRefusesAnExistingDestinationAndLeavesItAlone(t *testing.T) {
	src := sampleTree(t)
	parent := t.TempDir()
	for name, make := range map[string]func(string) error{
		"directory": func(path string) error { return os.Mkdir(path, 0o755) },
		"file":      func(path string) error { return os.WriteFile(path, []byte("mine"), 0o644) },
		"link":      func(path string) error { return os.Symlink(src, path) },
	} {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(parent, name)
			if err := make(dst); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(dst)
			if _, err := run(src, dst, noClone); !errors.Is(err, ErrDestinationExists) {
				t.Fatalf("err = %v", err)
			}
			after, err := os.Lstat(dst)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("destination disturbed: %v", err)
			}
		})
	}
}

func TestRefusesADestinationInsideTheSource(t *testing.T) {
	src := sampleTree(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(src, alias); err != nil {
		t.Fatal(err)
	}
	for _, dst := range []string{
		filepath.Join(src, "brand", "new"),
		filepath.Join(alias, "through-link"),
	} {
		if _, err := run(src, dst, noClone); !errors.Is(err, ErrDestinationInsideSource) {
			t.Fatalf("%s: err = %v", dst, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(src, "brand")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused clone created parents in the source: %v", err)
	}
}

func TestSiblingWithSharedPrefixIsNotInsideTheSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "cache")
	write(t, filepath.Join(src, "x"), "x", 0o644)
	if _, err := run(src, filepath.Join(root, "cache-copy"), noClone); err != nil {
		t.Fatal(err)
	}
}

func TestMissingSourceIsAnError(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "dst")
	if _, err := Clone(filepath.Join(root, "absent"), dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destination appeared: %v", err)
	}
}

func TestACloneFailureRemovesThePartialTree(t *testing.T) {
	src := sampleTree(t)
	dst := filepath.Join(t.TempDir(), "dst")
	broken := errors.New("disk on fire")

	_, err := run(src, dst, func(from, _ string) error {
		if strings.HasSuffix(from, "tool.sh") {
			return broken
		}
		return errNoClone
	})
	if !errors.Is(err, broken) || !strings.Contains(err.Error(), "tool.sh") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Lstat(dst); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("partial tree left behind: %v", statErr)
	}
}

func TestCopyRefusesASourceSwappedAfterTheWalk(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "f"), "first", 0o644)

	_, err := run(src, filepath.Join(root, "dst"), func(from, _ string) error {
		if err := os.Remove(from); err != nil {
			t.Fatal(err)
		}
		write(t, from, "second", 0o644)
		return errNoClone
	})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("err = %v", err)
	}
}

// Some file systems hand a freed inode number straight to the next new file,
// so a replaced source can look like the same file by device and inode. These
// cases keep the inode on purpose, which reproduces that on every file system.
func TestCopyRefusesASourceChangedUnderTheSameInode(t *testing.T) {
	later := stamp.Add(time.Hour)
	for name, change := range map[string]func(t *testing.T, path string){
		"size": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("longer body"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		},
		"mtime": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("FIRST"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, later, later); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			src := filepath.Join(root, "src")
			write(t, filepath.Join(src, "f"), "first", 0o644)
			dst := filepath.Join(root, "dst")

			_, err := run(src, dst, func(from, _ string) error {
				before, statErr := os.Stat(from)
				if statErr != nil {
					t.Fatal(statErr)
				}
				change(t, from)
				after, statErr := os.Stat(from)
				if statErr != nil {
					t.Fatal(statErr)
				}
				if !os.SameFile(before, after) {
					t.Fatal("the fixture was meant to keep the inode")
				}
				return errNoClone
			})
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("err = %v", err)
			}
			if _, statErr := os.Lstat(dst); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("partial tree left behind: %v", statErr)
			}
		})
	}
}

func TestCopyNeverOverwritesAFileThatAppeared(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "f"), "source", 0o644)
	dst := filepath.Join(root, "dst")

	_, err := run(src, dst, func(_, to string) error {
		if err := os.WriteFile(to, []byte("appeared"), 0o644); err != nil {
			t.Fatal(err)
		}
		return errNoClone
	})
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err = %v", err)
	}
}

func TestRootCreatedByAnotherProcessDuringTheWalkIsNotRemoved(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "file")
	write(t, src, "x", 0o644)
	dst := filepath.Join(root, "dst")

	_, err := run(src, dst, func(_, to string) error {
		if err := os.WriteFile(to, []byte("theirs"), 0o644); err != nil {
			t.Fatal(err)
		}
		return errNoClone
	})
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, dst); got != "theirs" {
		t.Fatalf("the other process's file became %q", got)
	}
}

func TestUnreadableSourceFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "secret"), "x", 0o000)
	if _, err := run(src, filepath.Join(root, "dst"), noClone); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
}

func TestDestinationBelowAFileFails(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "f"), "x", 0o644)
	blocker := filepath.Join(root, "blocker")
	write(t, blocker, "x", 0o644)

	_, err := run(src, filepath.Join(blocker, "dst"), noClone)
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("err = %v", err)
	}
}

func TestDestinationParentThatCannotBeCreatedFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "f"), "x", 0o644)
	sealed := filepath.Join(root, "sealed")
	if err := os.Mkdir(sealed, 0o555); err != nil {
		t.Fatal(err)
	}
	unlockAfter(t, sealed)

	_, err := run(src, filepath.Join(sealed, "parent", "dst"), noClone)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnreadableDirectoryFailsAndRemovesThePartialTree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "closed", "f"), "x", 0o644)
	closed := filepath.Join(src, "closed")
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	unlockAfter(t, closed)
	dst := filepath.Join(root, "dst")

	if _, err := run(src, dst, noClone); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial tree left behind: %v", err)
	}
}

func TestPlatformCloneReportsRealErrors(t *testing.T) {
	root := t.TempDir()
	err := platformClone(filepath.Join(root, "absent"), filepath.Join(root, "dst"))
	if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, errNoClone) {
		t.Fatalf("err = %v", err)
	}
}

// secondVolume returns a writable directory on a different file system from
// the temporary directory. A CI machine must have one; elsewhere it is optional.
func secondVolume(t *testing.T) string {
	t.Helper()
	missing := func(reason string) string {
		if os.Getenv("CI") != "" {
			t.Fatalf("no second volume on CI: %s", reason)
		}
		t.Skip("no second volume: " + reason)
		return ""
	}
	if runtime.GOOS == "linux" {
		dir, err := os.MkdirTemp("/dev/shm", "cow-")
		if err != nil {
			return missing(err.Error())
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	root, err := os.MkdirTemp("", "cow-volume-")
	if err != nil {
		return missing(err.Error())
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	image := filepath.Join(root, "volume.dmg")
	mount := filepath.Join(root, "mount")
	if out, err := exec.Command("hdiutil", "create", "-size", "16m", "-fs", "APFS", "-volname", "cowtest", image).CombinedOutput(); err != nil {
		return missing(fmt.Sprintf("hdiutil create: %v: %s", err, out))
	}
	if out, err := exec.Command("hdiutil", "attach", "-nobrowse", "-mountpoint", mount, image).CombinedOutput(); err != nil {
		return missing(fmt.Sprintf("hdiutil attach: %v: %s", err, out))
	}
	t.Cleanup(func() { _ = exec.Command("hdiutil", "detach", "-force", mount).Run() })
	return mount
}

func TestCrossVolumeDestinationReportsCopied(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	write(t, filepath.Join(src, "sub", "file.txt"), "across", 0o644)
	volume := secondVolume(t)
	dst := filepath.Join(volume, "dst")

	srcInfo, _ := os.Stat(src)
	volumeInfo, err := os.Stat(volume)
	if err != nil {
		t.Fatal(err)
	}
	if srcInfo.Sys().(*syscall.Stat_t).Dev == volumeInfo.Sys().(*syscall.Stat_t).Dev {
		t.Fatal("the fixture directories are on one volume")
	}

	result, err := Clone(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, src, dst)
	if result.Shared != 0 || result.Copied != 1 || result.Entries[0].Method != Copied {
		t.Fatalf("result %+v", result)
	}
}

const (
	largeFiles    = 16
	largeFileSize = 128 << 20
	largeTotal    = largeFiles * largeFileSize
)

func availableBytes(t *testing.T, path string) int64 {
	t.Helper()
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		t.Fatal(err)
	}
	return int64(stat.Bavail) * int64(stat.Bsize)
}

// assertLargeTreeShares fills root with two gigabytes of distinct data and
// proves the clone used next to no space. Free space is read around the clone
// only, with dirty pages flushed first, so the fixture's own writes do not count.
func assertLargeTreeShares(t *testing.T, root string) {
	t.Helper()
	src := filepath.Join(root, "large-src")
	dst := filepath.Join(root, "large-dst")
	t.Cleanup(func() {
		_ = os.RemoveAll(src)
		_ = os.RemoveAll(dst)
	})
	block := make([]byte, 1<<20)
	for file := range largeFiles {
		path := filepath.Join(src, fmt.Sprintf("d%d", file%4), fmt.Sprintf("f%d.bin", file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		out, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		for index := range largeFileSize / len(block) {
			for offset := 0; offset < len(block); offset += 4096 {
				copy(block[offset:], fmt.Sprintf("%d/%d/%d", file, index, offset))
			}
			if _, err := out.Write(block); err != nil {
				t.Fatal(err)
			}
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	syscall.Sync()
	before := availableBytes(t, root)

	result, err := Clone(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	syscall.Sync()
	used := before - availableBytes(t, root)

	if result.Shared != largeFiles || result.Copied != 0 || result.SharedBytes != largeTotal {
		t.Fatalf("shared %d (%d bytes) copied %d", result.Shared, result.SharedBytes, result.Copied)
	}
	if used >= largeTotal/2 {
		t.Fatalf("the clone consumed %d of %d bytes", used, int64(largeTotal))
	}
	sample := filepath.Join("d0", "f0.bin")
	info, err := os.Stat(filepath.Join(dst, sample))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatalf("mtime %v", info.ModTime())
	}
	original, err := os.ReadFile(filepath.Join(src, sample))
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := os.ReadFile(filepath.Join(dst, sample))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, cloned) {
		t.Fatal("the clone's contents differ")
	}
	overwriteInPlace(t, filepath.Join(dst, sample), "written through the clone")
	after, err := os.ReadFile(filepath.Join(src, sample))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("an in-place write reached the source")
	}
}

func largeRequested() bool {
	return os.Getenv("CI") != "" || os.Getenv("HERDSMAN_COW_LARGE") != ""
}

func TestLargeTreeSharesOnAPFS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("APFS case")
	}
	if !largeRequested() {
		t.Skip("writes two gigabytes; set HERDSMAN_COW_LARGE=1 (always on in CI)")
	}
	assertLargeTreeShares(t, t.TempDir())
}

// btrfsLoopback mounts a throwaway btrfs image for the test and returns the
// mount point. It needs passwordless sudo, which the Linux CI runners have.
func btrfsLoopback(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, out)
		}
	}
	if _, err := exec.LookPath("mkfs.btrfs"); err != nil {
		must("sudo", "apt-get", "install", "-y", "btrfs-progs")
	}
	image := filepath.Join(root, "btrfs.img")
	mount := filepath.Join(root, "mount")
	must("truncate", "-s", "4G", image)
	must("mkfs.btrfs", "-q", image)
	must("mkdir", mount)
	must("sudo", "mount", "-o", "loop", image, mount)
	t.Cleanup(func() { _ = exec.Command("sudo", "umount", mount).Run() })
	must("sudo", "chown", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), mount)
	return mount
}

func TestLargeTreeSharesOnBtrfs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("btrfs case")
	}
	if os.Getenv("CI") == "" {
		t.Skip("mounts a loopback image with sudo; runs on CI only")
	}
	assertLargeTreeShares(t, btrfsLoopback(t))
}

// Cloning is kernel work that blocks its thread, so the pool must not shrink
// with the core count: a small machine would clone one file at a time.
func TestCloneRunsSeveralFilesAtOnceOnOneCore(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	src := t.TempDir()
	for i := range minCloneWorkers {
		if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("f%d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var arrived sync.WaitGroup
	arrived.Add(minCloneWorkers)
	clone := func(_, dst string) error {
		arrived.Done()
		done := make(chan struct{})
		go func() { arrived.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			return errors.New("the files were cloned one at a time")
		}
		return os.WriteFile(dst, nil, 0o600)
	}
	if _, err := run(src, filepath.Join(t.TempDir(), "dst"), clone); err != nil {
		t.Fatal(err)
	}
}
