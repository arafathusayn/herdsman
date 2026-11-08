package project

import (
	"errors"
	"fmt"
	"path/filepath"
)

// current is the reference that means "the one I am in".
const current = "@"

// errNoProject says nothing named a project, which a task lookup may survive.
var errNoProject = fmt.Errorf("%w: no project: none named, no bound folder, no route", ErrNotFound)

// Binding is what a folder's config says about its project.
type Binding struct {
	Folder string
	ID     string
	Name   string
}

// Scope is where a command stands when it names a project or a task.
type Scope struct {
	Project      string // the --project value: a name, an id or @
	Folder       string // the absolute folder the command runs in
	RouteProject string // the project of the route the command runs in
}

// WorktreeTask names the task of the worktree holding folder.
type WorktreeTask func(folder string) (slug string, ok bool, err error)

// FindBinding walks up from start to the nearest folder whose config has a
// [project] section. A folder with a config but no section does not stop it.
func FindBinding(start string) (Binding, bool, error) {
	if !filepath.IsAbs(start) {
		return Binding{}, false, fmt.Errorf("%w: folder %q is not absolute", ErrInvalid, start)
	}
	for dir := filepath.Clean(start); ; dir = filepath.Dir(dir) {
		b, ok, err := bindingIn(dir)
		if ok || err != nil {
			return b, ok, err
		}
		if dir == filepath.Dir(dir) {
			return Binding{}, false, nil
		}
	}
}

func bindingIn(dir string) (Binding, bool, error) {
	path := filepath.Join(dir, ConfigFile)
	text, exists, err := readConfig(path)
	if err != nil || !exists {
		return Binding{}, false, err
	}
	cfg, err := parseConfig(path, text)
	if err != nil {
		return Binding{}, false, err
	}
	if cfg.start < 0 {
		return Binding{}, false, nil
	}
	if cfg.id == "" {
		return Binding{}, false, fmt.Errorf("%w: %s: [project] has no id", ErrInvalid, path)
	}
	return Binding{Folder: dir, ID: cfg.id, Name: cfg.name}, true, nil
}

// ResolveProject applies the precedence: the named project, then the nearest
// bound folder, then the route's project.
func (s *Store) ResolveProject(sc Scope) (*Project, error) {
	if sc.Project != "" && sc.Project != current {
		return s.GetProject(sc.Project)
	}
	if sc.Folder != "" {
		b, ok, err := FindBinding(sc.Folder)
		if err != nil {
			return nil, err
		}
		if ok {
			p, err := s.GetProject(b.ID)
			if errors.Is(err, ErrNotFound) {
				return nil, fmt.Errorf("%w: %s is bound to project %s, which no longer exists", ErrNotFound, b.Folder, b.ID)
			}
			return p, err
		}
	}
	if sc.RouteProject != "" {
		return s.GetProject(sc.RouteProject)
	}
	return nil, errNoProject
}

// ResolveTask finds a task from an id, a slug in the scope's project, or @ for
// the task of the current worktree. A slug with no project in scope must be
// unique across projects.
func (s *Store) ResolveTask(ref string, sc Scope, worktree WorktreeTask) (*Task, error) {
	if IsID(ref) {
		return s.GetTask("", ref)
	}
	if ref == current {
		if worktree == nil {
			return nil, fmt.Errorf("%w: no worktree state to find the current task from", ErrRefused)
		}
		slug, ok, err := worktree(sc.Folder)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s is not in a task's worktree", ErrNotFound, sc.Folder)
		}
		ref = slug
	}
	p, err := s.ResolveProject(sc)
	if errors.Is(err, errNoProject) {
		return s.GetTask("", ref)
	}
	if err != nil {
		return nil, err
	}
	return s.GetTask(p.ID, ref)
}
