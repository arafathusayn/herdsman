// Package approvals remembers which project hook templates a person approved.
// An approval is for one exact template, so any edit asks again.
package approvals

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/arafathusayn/herdsman/sdk/config"
)

// FileName is the store's name inside the user configuration folder.
const FileName = "approvals.toml"

// Store is one approvals file.
type Store struct {
	path string
}

// Open returns the store at path. The file need not exist yet.
func Open(path string) *Store { return &Store{path: path} }

// Default returns the store in the user configuration folder for environ.
func Default(environ []string) (*Store, error) {
	dir := config.UserDir(environ)
	if dir == "" {
		return nil, errors.New("approvals: no user configuration folder: HOME is unset")
	}
	return Open(filepath.Join(dir, FileName)), nil
}

// Path is the file behind the store.
func (s *Store) Path() string { return s.path }

// List returns every approval, by project.
func (s *Store) List() (map[string][]string, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	approvals, err := config.DecodeApprovals(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return approvals, nil
}

// Approved reports whether this exact template was approved for the project.
func (s *Store) Approved(project, template string) (bool, error) {
	approvals, err := s.List()
	if err != nil {
		return false, err
	}
	return slices.Contains(approvals[project], template), nil
}

// Approve records a template. Approving it twice changes nothing.
func (s *Store) Approve(project, template string) error {
	return s.locked(func() error {
		approvals, err := s.List()
		if err != nil {
			return err
		}
		if slices.Contains(approvals[project], template) {
			return nil
		}
		approvals[project] = append(approvals[project], template)
		return s.write(approvals)
	})
}

// Clear removes one project's approvals, or every approval when project is
// empty, and returns how many templates were removed.
func (s *Store) Clear(project string) (int, error) {
	removed := 0
	err := s.locked(func() error {
		approvals, err := s.List()
		if err != nil {
			return err
		}
		for name, templates := range approvals {
			if project == "" || project == name {
				removed += len(templates)
				delete(approvals, name)
			}
		}
		if removed == 0 {
			return nil
		}
		return s.write(approvals)
	})
	return removed, err
}

// locked runs change while holding the store's lock, so that a whole
// read-modify-write is one step across goroutines and processes. The lock is
// on a separate file because the store itself is replaced by rename. The
// kernel drops it if the process dies.
func (s *Store) locked(change func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("lock %s: %w", s.path, err)
	}
	return change()
}

// write replaces the file whole, readable only by its owner.
func (s *Store) write(approvals map[string][]string) error {
	data, err := config.EncodeApprovals(approvals)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".approvals-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), s.path)
}
