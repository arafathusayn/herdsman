package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Approvals records, per project, the exact hook templates a person accepted.
// Approving the template rather than its expansion keeps an approval valid for
// every task.
type Approvals struct {
	Path string
}

type approvalFile struct {
	Projects map[string][]string `json:"projects"`
}

func (a *Approvals) read() (approvalFile, error) {
	file := approvalFile{Projects: map[string][]string{}}
	data, err := os.ReadFile(a.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, fmt.Errorf("hooks: read approvals: %w", err)
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("hooks: approvals file %s: %w", a.Path, err)
	}
	if file.Projects == nil {
		file.Projects = map[string][]string{}
	}
	return file, nil
}

// Approved says whether the exact template was accepted for the project.
func (a *Approvals) Approved(project, template string) (bool, error) {
	file, err := a.read()
	if err != nil {
		return false, err
	}
	return slices.Contains(file.Projects[project], template), nil
}

// Unapproved lists the plan's templates that were not accepted, without repeats.
func (a *Approvals) Unapproved(project string, plan Plan) ([]string, error) {
	file, err := a.read()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, step := range plan {
		for _, command := range step {
			if !slices.Contains(file.Projects[project], command.Template) && !slices.Contains(missing, command.Template) {
				missing = append(missing, command.Template)
			}
		}
	}
	return missing, nil
}

// Approve accepts the templates for the project.
func (a *Approvals) Approve(project string, templates []string) error {
	file, err := a.read()
	if err != nil {
		return err
	}
	merged := slices.Concat(file.Projects[project], templates)
	slices.Sort(merged)
	file.Projects[project] = slices.Compact(merged)
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o700); err != nil {
		return fmt.Errorf("hooks: approvals folder: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(a.Path), ".approvals-*")
	if err != nil {
		return fmt.Errorf("hooks: write approvals: %w", err)
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
		return fmt.Errorf("hooks: write approvals: %w", err)
	}
	return nil
}
