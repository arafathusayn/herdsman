package project

import (
	"fmt"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
)

// openStatuses is the SQL list of task statuses that still count as work.
const openStatuses = "('todo','doing','review')"

// Store reads and writes the records on one account-database connection. Like
// the connection it holds, it is used by one goroutine at a time.
type Store struct {
	db     store.Conn
	engine *ops.Engine
}

// New returns a Store on the account database. Changes that reach a folder's
// config run on engine, which is built with Effects(db).
func New(db store.Conn, engine *ops.Engine) *Store { return &Store{db: db, engine: engine} }

func nowMillis() int64 { return time.Now().UnixMilli() }

// CreateProject stores a new active project.
func (s *Store) CreateProject(in ProjectInput) (*Project, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return nil, err
	}
	repos, err := cleanRepos(in.Repos)
	if err != nil {
		return nil, err
	}
	id := NewID()
	var out *Project
	err = s.db.Tx(func(c store.Conn) error {
		if err := nameFree(c, name, id); err != nil {
			return err
		}
		now := nowMillis()
		if _, err := c.Exec("INSERT INTO projects (id, name, description, state, created, updated) VALUES (?, ?, ?, 'active', ?, ?)",
			id, name, in.Description, now, now); err != nil {
			return err
		}
		if err := insertRepos(c, id, repos); err != nil {
			return err
		}
		out, err = loadProject(c, id)
		return err
	})
	return out, err
}

// GetProject finds a project by id, or an active one by name.
func (s *Store) GetProject(ref string) (*Project, error) {
	return findProject(s.db, ref)
}

// ListProjects returns the projects by name; archived ones only when asked.
func (s *Store) ListProjects(includeArchived bool) ([]*Project, error) {
	q := "SELECT id FROM projects WHERE state = 'active' ORDER BY name, id"
	if includeArchived {
		q = "SELECT id FROM projects ORDER BY name, id"
	}
	ids, err := column(s.db, q)
	if err != nil {
		return nil, err
	}
	out := make([]*Project, 0, len(ids))
	for _, id := range ids {
		p, err := loadProject(s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// UpdateProject applies patch in one transaction, and carries a new name to
// the configs of the folders bound to the project.
func (s *Store) UpdateProject(ref string, patch ProjectPatch) (*Project, error) {
	added, err := cleanRepos(patch.AddRepos)
	if err != nil {
		return nil, err
	}
	var out *Project
	err = s.txFiles(func(c store.Conn, files *fileEdits) error {
		p, err := activeProject(c, ref)
		if err != nil {
			return err
		}
		if err := applyProjectFields(c, p, patch); err != nil {
			return err
		}
		if err := removeRepos(c, p.ID, patch.RemoveRepos); err != nil {
			return err
		}
		for _, r := range added {
			if err := repoFree(c, p.ID, r.Name); err != nil {
				return err
			}
		}
		if err := insertRepos(c, p.ID, added); err != nil {
			return err
		}
		if _, err := c.Exec("UPDATE projects SET updated = ? WHERE id = ?", nowMillis(), p.ID); err != nil {
			return err
		}
		if out, err = loadProject(c, p.ID); err != nil {
			return err
		}
		if patch.Name == nil {
			return nil
		}
		for _, folder := range out.Folders {
			if err := renameBinding(files, folder, out.ID, out.Name); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func applyProjectFields(c store.Conn, p *Project, patch ProjectPatch) error {
	if patch.Name != nil {
		name, err := cleanName(*patch.Name)
		if err != nil {
			return err
		}
		if err := nameFree(c, name, p.ID); err != nil {
			return err
		}
		if _, err := c.Exec("UPDATE projects SET name = ? WHERE id = ?", name, p.ID); err != nil {
			return err
		}
	}
	if patch.Description != nil {
		if _, err := c.Exec("UPDATE projects SET description = ? WHERE id = ?", *patch.Description, p.ID); err != nil {
			return err
		}
	}
	return nil
}

func removeRepos(c store.Conn, projectID string, names []string) error {
	for _, name := range names {
		users, err := column(c, "SELECT t.slug FROM task_repos r JOIN tasks t ON t.id = r.task_id WHERE r.project_id = ? AND r.repo = ? ORDER BY t.slug", projectID, name)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("%w: repository %s is used by tasks: %s", ErrRefused, name, strings.Join(users, ", "))
		}
		n, err := c.Exec("DELETE FROM project_repos WHERE project_id = ? AND name = ?", projectID, name)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: repository %s", ErrNotFound, name)
		}
	}
	return nil
}

// DeleteProject removes a project with no open tasks and no task on a route
// that still exists, whatever that task's status. With force it instead
// archives the project and its open tasks, keeping every row and freeing the
// name. Either way the bound folders are released.
func (s *Store) DeleteProject(ref string, force bool) error {
	return s.txFiles(func(c store.Conn, files *fileEdits) error {
		p, err := activeProject(c, ref)
		if err != nil {
			return err
		}
		if !force {
			if err := refuseBusyProject(c, p); err != nil {
				return err
			}
		}
		for _, folder := range p.Folders {
			if err := clearBinding(files, folder, p.ID); err != nil {
				return err
			}
		}
		if _, err := c.Exec("DELETE FROM project_folders WHERE project_id = ?", p.ID); err != nil {
			return err
		}
		if force {
			return archiveProject(c, p.ID)
		}
		return purgeProject(c, p.ID)
	})
}

func refuseBusyProject(c store.Conn, p *Project) error {
	open, err := column(c, "SELECT slug FROM tasks WHERE project_id = ? AND status IN "+openStatuses+" ORDER BY slug", p.ID)
	if err != nil {
		return err
	}
	if len(open) > 0 {
		return fmt.Errorf("%w: project %s has open tasks: %s", ErrRefused, p.Name, strings.Join(open, ", "))
	}
	running, err := column(c, "SELECT t.slug || ' on route ' || t.route FROM tasks t JOIN routes r ON r.route = t.route WHERE t.project_id = ? AND r.state <> 'removed' ORDER BY t.slug", p.ID)
	if err != nil {
		return err
	}
	if len(running) > 0 {
		return fmt.Errorf("%w: project %s has tasks on running routes: %s", ErrRefused, p.Name, strings.Join(running, ", "))
	}
	return nil
}

func archiveProject(c store.Conn, id string) error {
	now := nowMillis()
	if _, err := c.Exec("UPDATE tasks SET status = 'archived', updated = ? WHERE project_id = ? AND status IN "+openStatuses, now, id); err != nil {
		return err
	}
	_, err := c.Exec("UPDATE projects SET state = 'archived', updated = ? WHERE id = ?", now, id)
	return err
}

func purgeProject(c store.Conn, id string) error {
	tasks, err := column(c, "SELECT id FROM tasks WHERE project_id = ?", id)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := purgeTask(c, task); err != nil {
			return err
		}
	}
	if _, err := c.Exec("DELETE FROM project_repos WHERE project_id = ?", id); err != nil {
		return err
	}
	_, err = c.Exec("DELETE FROM projects WHERE id = ?", id)
	return err
}

func nameFree(c store.Conn, name, self string) error {
	var holder string
	err := c.Query("SELECT id FROM projects WHERE name = ? AND state = 'active'", []any{name}, func(r store.Row) error {
		holder = r.Text(0)
		return nil
	})
	if err != nil {
		return err
	}
	if holder != "" && holder != self {
		return fmt.Errorf("%w: a project is already named %s", ErrConflict, name)
	}
	return nil
}

func repoFree(c store.Conn, projectID, name string) error {
	names, err := column(c, "SELECT name FROM project_repos WHERE project_id = ? AND name = ?", projectID, name)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return fmt.Errorf("%w: the project already has repository %s", ErrConflict, name)
	}
	return nil
}

func insertRepos(c store.Conn, projectID string, repos []Repo) error {
	for _, r := range repos {
		if _, err := c.Exec("INSERT INTO project_repos (project_id, name, path, default_branch, remote) VALUES (?, ?, ?, ?, ?)",
			projectID, r.Name, r.Path, r.DefaultBranch, r.Remote); err != nil {
			return err
		}
	}
	return nil
}

func findProject(c store.Conn, ref string) (*Project, error) {
	id := ref
	if !IsID(ref) {
		ids, err := column(c, "SELECT id FROM projects WHERE name = ? AND state = 'active'", ref)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("%w: project %s", ErrNotFound, ref)
		}
		id = ids[0]
	}
	return loadProject(c, id)
}

func activeProject(c store.Conn, ref string) (*Project, error) {
	p, err := findProject(c, ref)
	if err != nil {
		return nil, err
	}
	if p.State != Active {
		return nil, fmt.Errorf("%w: project %s is archived", ErrRefused, p.Name)
	}
	return p, nil
}

func loadProject(c store.Conn, id string) (*Project, error) {
	var p *Project
	err := c.Query("SELECT name, description, state, created, updated FROM projects WHERE id = ?", []any{id}, func(r store.Row) error {
		p = &Project{ID: id, Name: r.Text(0), Description: r.Text(1), State: State(r.Text(2)), Created: r.Int64(3), Updated: r.Int64(4)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", ErrNotFound, id)
	}
	if p.Folders, err = column(c, "SELECT path FROM project_folders WHERE project_id = ? ORDER BY path", id); err != nil {
		return nil, err
	}
	err = c.Query("SELECT name, path, default_branch, remote FROM project_repos WHERE project_id = ? ORDER BY name", []any{id}, func(r store.Row) error {
		p.Repos = append(p.Repos, Repo{Name: r.Text(0), Path: r.Text(1), DefaultBranch: r.Text(2), Remote: r.Text(3)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = c.Query("SELECT count(*) FROM tasks WHERE project_id = ? AND status IN "+openStatuses, []any{id}, func(r store.Row) error {
		p.OpenTasks = int(r.Int64(0))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// column runs a query and returns its first column as text.
func column(c store.Conn, q string, args ...any) ([]string, error) {
	var out []string
	err := c.Query(q, args, func(r store.Row) error {
		out = append(out, r.Text(0))
		return nil
	})
	return out, err
}
