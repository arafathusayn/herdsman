package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Approvals records, per plugin, the one executable checksum a person accepted.
// A changed executable is unapproved until someone accepts it again.
type Approvals struct {
	Path string
}

func (a Approvals) read() (map[string]string, error) {
	approved := map[string]string{}
	data, err := os.ReadFile(a.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return approved, nil
	}
	if err != nil {
		return nil, fmt.Errorf("plugin: read approvals: %w", err)
	}
	if err := json.Unmarshal(data, &approved); err != nil {
		return nil, fmt.Errorf("plugin: approvals file %s: %w", a.Path, err)
	}
	return approved, nil
}

// write replaces the file in one step, so a crash leaves the old approvals.
func (a Approvals) write(approved map[string]string) error {
	data, err := json.MarshalIndent(approved, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o700); err != nil {
		return fmt.Errorf("plugin: approvals folder: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(a.Path), ".approvals-*")
	if err != nil {
		return fmt.Errorf("plugin: write approvals: %w", err)
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(append(data, '\n'))
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), a.Path)
	}
	if err != nil {
		return fmt.Errorf("plugin: write approvals: %w", err)
	}
	return nil
}

// Approved says whether this exact checksum was accepted for the plugin.
func (a Approvals) Approved(name, checksum string) (bool, error) {
	approved, err := a.read()
	if err != nil {
		return false, err
	}
	return approved[name] == checksum && checksum != "", nil
}

// Approve accepts the checksum, in place of any earlier one.
func (a Approvals) Approve(name, checksum string) error {
	approved, err := a.read()
	if err != nil {
		return err
	}
	approved[name] = checksum
	return a.write(approved)
}

// Revoke forgets the plugin's approval.
func (a Approvals) Revoke(name string) error {
	approved, err := a.read()
	if err != nil {
		return err
	}
	if _, ok := approved[name]; !ok {
		return nil
	}
	delete(approved, name)
	return a.write(approved)
}
