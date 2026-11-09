package context

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

var errFault = errors.New("injected failure")

// failingFile fails one step of publishing an event.
type failingFile struct {
	*os.File
	step string
}

func (f failingFile) fail(step string) error {
	if f.step == step {
		return errFault
	}
	return nil
}

func (f failingFile) Write(b []byte) (int, error) {
	if err := f.fail("write"); err != nil {
		return 0, err
	}
	return f.File.Write(b)
}

func (f failingFile) Chmod(m os.FileMode) error {
	if err := f.fail("chmod"); err != nil {
		return err
	}
	return f.File.Chmod(m)
}

func (f failingFile) Sync() error {
	if err := f.fail("sync"); err != nil {
		return err
	}
	return f.File.Sync()
}

func (f failingFile) Close() error {
	return errors.Join(f.File.Close(), f.fail("close"))
}

func replace[T any](t *testing.T, target *T, value T) {
	t.Helper()
	old := *target
	*target = value
	t.Cleanup(func() { *target = old })
}

func eventFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, EventsDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() != ".gitkeep" {
			names = append(names, entry.Name())
		}
	}
	return names
}

var faultInput = Input{Kind: KindNote, Actor: "someone", Body: "text"}

// A failed publication leaves neither the event nor its temporary behind.
func TestAppendPublishesAllOrNothing(t *testing.T) {
	realCreate := createTemp
	for _, step := range []string{"write", "chmod", "sync", "close", "link"} {
		t.Run(step, func(t *testing.T) {
			dir := newRepo(t)
			replace(t, &createTemp, func(dir, pattern string) (tempFile, error) {
				f, err := realCreate(dir, pattern)
				if err != nil {
					return nil, err
				}
				return failingFile{File: f.(*os.File), step: step}, nil
			})
			if step == "link" {
				replace(t, &linkFile, func(string, string) error { return errFault })
			}
			if _, err := Append(dir, faultInput); !errors.Is(err, errFault) {
				t.Fatalf("error %v", err)
			}
			if names := eventFiles(t, dir); len(names) != 0 {
				t.Fatalf("left behind: %v", names)
			}
		})
	}
}

// Once linked the event exists; a failure to sync the folder is still reported.
func TestAppendReportsAFailureAfterPublishing(t *testing.T) {
	dir := newRepo(t)
	replace(t, &openDir, func(string) (*os.File, error) { return nil, errFault })
	if _, err := Append(dir, faultInput); !errors.Is(err, errFault) {
		t.Fatalf("error %v", err)
	}
	names := eventFiles(t, dir)
	if len(names) != 1 {
		t.Fatalf("files: %v", names)
	}
	info, err := os.Stat(filepath.Join(dir, EventsDir, names[0]))
	if err != nil || info.Size() == 0 {
		t.Fatalf("published file: %+v, %v", info, err)
	}
}

func TestAppendLock(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		dir := newRepo(t)
		replace(t, &flock, func(int, int) error { return syscall.EIO })
		if _, err := Append(dir, faultInput); !errors.Is(err, syscall.EIO) {
			t.Fatalf("error %v", err)
		}
		if names := eventFiles(t, dir); len(names) != 0 {
			t.Fatalf("written without the lock: %v", names)
		}
	})
	t.Run("interrupted", func(t *testing.T) {
		dir := newRepo(t)
		calls := 0
		replace(t, &flock, func(int, int) error {
			if calls++; calls == 1 {
				return syscall.EINTR
			}
			return nil
		})
		if _, err := Append(dir, faultInput); err != nil || calls != 2 {
			t.Fatalf("error %v after %d calls", err, calls)
		}
	})
}
