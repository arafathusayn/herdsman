package project

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// Status is where a task stands.
type Status string

const (
	Todo           Status = "todo"
	Doing          Status = "doing"
	Review         Status = "review"
	Done           Status = "done"
	StatusArchived Status = "archived"
)

func (st Status) valid() bool {
	switch st {
	case Todo, Doing, Review, Done, StatusArchived:
		return true
	}
	return false
}

// LinkKind says what a Link points at.
type LinkKind string

const (
	LinkPR    LinkKind = "pr"
	LinkIssue LinkKind = "issue"
	LinkURL   LinkKind = "url"
)

// Link attaches a pull request, an issue or a URL to one repository of a task.
type Link struct {
	Repo   string
	Kind   LinkKind
	Target string
}

// Task is a stored task with its repositories, labels and links.
type Task struct {
	ID        string
	ProjectID string
	Slug      string
	Title     string
	Body      string
	Status    Status
	Route     string
	Repos     []string
	Labels    []string
	Links     []Link
	Created   int64
	Updated   int64
}

// TaskInput describes a new task. No Repos means every repository of the
// project; no Slug means one generated from the title.
type TaskInput struct {
	ProjectID string
	Title     string
	Slug      string
	Body      string
	Labels    []string
	Repos     []string
}

// TaskPatch changes a task; a nil pointer leaves the field alone. It has no
// project field on purpose: a task's project is written once.
type TaskPatch struct {
	Title        *string
	Body         *string
	Status       *Status
	Route        *string
	AddRepos     []string
	RemoveRepos  []string
	AddLabels    []string
	RemoveLabels []string
}

// TaskFilter narrows ListTasks; zero values do not filter.
type TaskFilter struct {
	ProjectID string
	Status    Status
	Repo      string
	Label     string
}

// CreateTask stores a new task under an active project.
func (s *Store) CreateTask(in TaskInput) (*Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: a task needs a title", ErrInvalid)
	}
	if in.Slug != "" && !ValidSlug(in.Slug) {
		return nil, fmt.Errorf("%w: slug %q", ErrInvalid, in.Slug)
	}
	labels, err := cleanLabels(in.Labels)
	if err != nil {
		return nil, err
	}
	id := NewID()
	var out *Task
	err = s.db.Tx(func(c store.Conn) error {
		p, err := activeProject(c, in.ProjectID)
		if err != nil {
			return err
		}
		repos, err := taskRepos(p, in.Repos)
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			return fmt.Errorf("%w: project %s has no repositories for a task to name", ErrInvalid, p.Name)
		}
		slug, err := chooseSlug(c, p.ID, in.Slug, title)
		if err != nil {
			return err
		}
		now := nowMillis()
		if _, err := c.Exec("INSERT INTO tasks (id, project_id, slug, title, body, status, created, updated) VALUES (?, ?, ?, ?, ?, 'todo', ?, ?)",
			id, p.ID, slug, title, in.Body, now, now); err != nil {
			return err
		}
		for _, r := range repos {
			if err := insertTaskRepo(c, id, p.ID, r); err != nil {
				return err
			}
		}
		if err := insertLabels(c, id, labels); err != nil {
			return err
		}
		out, err = loadTask(c, id)
		return err
	})
	return out, err
}

// taskRepos checks the named repositories against the project's, or returns
// all of the project's when none is named.
func taskRepos(p *Project, named []string) ([]string, error) {
	have := make(map[string]bool, len(p.Repos))
	all := make([]string, 0, len(p.Repos))
	for _, r := range p.Repos {
		have[r.Name] = true
		all = append(all, r.Name)
	}
	if len(named) == 0 {
		return all, nil
	}
	for _, r := range named {
		if !have[r] {
			return nil, fmt.Errorf("%w: project %s has no repository %s", ErrInvalid, p.Name, r)
		}
	}
	return named, nil
}

// chooseSlug keeps a requested slug or refuses it when taken; a generated one
// takes the first free numeric suffix.
func chooseSlug(c store.Conn, projectID, requested, title string) (string, error) {
	taken := func(slug string) (bool, error) {
		ids, err := column(c, "SELECT id FROM tasks WHERE project_id = ? AND slug = ?", projectID, slug)
		return len(ids) > 0, err
	}
	if requested != "" {
		used, err := taken(requested)
		if err != nil {
			return "", err
		}
		if used {
			return "", fmt.Errorf("%w: the project already has a task %s", ErrConflict, requested)
		}
		return requested, nil
	}
	base := Slugify(title)
	for n := 1; ; n++ {
		slug := base
		if n > 1 {
			slug = fmt.Sprintf("%s-%d", base, n)
		}
		used, err := taken(slug)
		if err != nil {
			return "", err
		}
		if !used {
			return slug, nil
		}
	}
}

func insertTaskRepo(c store.Conn, taskID, projectID, repo string) error {
	_, err := c.Exec("INSERT INTO task_repos (task_id, project_id, repo) VALUES (?, ?, ?)", taskID, projectID, repo)
	return err
}

func insertLabels(c store.Conn, taskID string, labels []string) error {
	for _, l := range labels {
		if _, err := c.Exec("INSERT OR IGNORE INTO task_labels (task_id, label) VALUES (?, ?)", taskID, l); err != nil {
			return err
		}
	}
	return nil
}

// GetTask finds a task by id, or by slug within projectID. With no projectID a
// slug must be unique across projects.
func (s *Store) GetTask(projectID, ref string) (*Task, error) {
	return findTask(s.db, projectID, ref)
}

func findTask(c store.Conn, projectID, ref string) (*Task, error) {
	if IsID(ref) {
		return loadTask(c, ref)
	}
	q, args := "SELECT id FROM tasks WHERE slug = ?", []any{ref}
	if projectID != "" {
		q, args = q+" AND project_id = ?", append(args, projectID)
	}
	ids, err := column(c, q, args...)
	if err != nil {
		return nil, err
	}
	switch len(ids) {
	case 0:
		return nil, fmt.Errorf("%w: task %s", ErrNotFound, ref)
	case 1:
		return loadTask(c, ids[0])
	}
	names, err := column(c, "SELECT p.name FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.slug = ? ORDER BY p.name", ref)
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: task %s exists in several projects: %s", ErrConflict, ref, strings.Join(names, ", "))
}

// ListTasks returns the matching tasks by slug.
func (s *Store) ListTasks(f TaskFilter) ([]*Task, error) {
	if f.Status != "" && !f.Status.valid() {
		return nil, fmt.Errorf("%w: status %s", ErrInvalid, f.Status)
	}
	var where []string
	var args []any
	add := func(clause, value string) {
		if value != "" {
			where = append(where, clause)
			args = append(args, value)
		}
	}
	add("project_id = ?", f.ProjectID)
	add("status = ?", string(f.Status))
	add("id IN (SELECT task_id FROM task_repos WHERE repo = ?)", f.Repo)
	add("id IN (SELECT task_id FROM task_labels WHERE label = ?)", f.Label)
	q := "SELECT id FROM tasks"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	ids, err := column(s.db, q+" ORDER BY slug, id", args...)
	if err != nil {
		return nil, err
	}
	out := make([]*Task, 0, len(ids))
	for _, id := range ids {
		t, err := loadTask(s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// UpdateTask applies patch in one transaction; any refusal leaves the task as
// it was.
func (s *Store) UpdateTask(ref string, patch TaskPatch) (*Task, error) {
	if patch.Status != nil && !patch.Status.valid() {
		return nil, fmt.Errorf("%w: status %s", ErrInvalid, *patch.Status)
	}
	if patch.Title != nil && strings.TrimSpace(*patch.Title) == "" {
		return nil, fmt.Errorf("%w: a task needs a title", ErrInvalid)
	}
	addLabels, err := cleanLabels(patch.AddLabels)
	if err != nil {
		return nil, err
	}
	var out *Task
	err = s.db.Tx(func(c store.Conn) error {
		t, err := findTask(c, "", ref)
		if err != nil {
			return err
		}
		if err := applyTaskFields(c, t.ID, patch); err != nil {
			return err
		}
		if err := changeTaskRepos(c, t, patch); err != nil {
			return err
		}
		if err := insertLabels(c, t.ID, addLabels); err != nil {
			return err
		}
		for _, l := range patch.RemoveLabels {
			if _, err := c.Exec("DELETE FROM task_labels WHERE task_id = ? AND label = ?", t.ID, strings.TrimSpace(l)); err != nil {
				return err
			}
		}
		if _, err := c.Exec("UPDATE tasks SET updated = ? WHERE id = ?", nowMillis(), t.ID); err != nil {
			return err
		}
		out, err = loadTask(c, t.ID)
		return err
	})
	return out, err
}

func applyTaskFields(c store.Conn, id string, patch TaskPatch) error {
	set := func(column string, value *string) error {
		if value == nil {
			return nil
		}
		_, err := c.Exec("UPDATE tasks SET "+column+" = ? WHERE id = ?", *value, id)
		return err
	}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if err := set("title", &title); err != nil {
			return err
		}
	}
	if err := set("body", patch.Body); err != nil {
		return err
	}
	if patch.Status != nil {
		status := string(*patch.Status)
		if err := set("status", &status); err != nil {
			return err
		}
	}
	return set("route", patch.Route)
}

func changeTaskRepos(c store.Conn, t *Task, patch TaskPatch) error {
	if len(patch.AddRepos) > 0 {
		p, err := loadProject(c, t.ProjectID)
		if err != nil {
			return err
		}
		if _, err := taskRepos(p, patch.AddRepos); err != nil {
			return err
		}
	}
	current := make(map[string]bool, len(t.Repos))
	for _, r := range t.Repos {
		current[r] = true
	}
	for _, r := range patch.AddRepos {
		if current[r] {
			return fmt.Errorf("%w: task %s already names repository %s", ErrConflict, t.Slug, r)
		}
		current[r] = true
		if err := insertTaskRepo(c, t.ID, t.ProjectID, r); err != nil {
			return err
		}
	}
	for _, r := range patch.RemoveRepos {
		if !current[r] {
			return fmt.Errorf("%w: task %s does not name repository %s", ErrNotFound, t.Slug, r)
		}
		delete(current, r)
		if _, err := c.Exec("DELETE FROM task_links WHERE task_id = ? AND repo = ?", t.ID, r); err != nil {
			return err
		}
		if _, err := c.Exec("DELETE FROM task_repos WHERE task_id = ? AND repo = ?", t.ID, r); err != nil {
			return err
		}
	}
	if len(patch.RemoveRepos) > 0 && len(current) == 0 {
		return fmt.Errorf("%w: that would remove the last repository of task %s", ErrRefused, t.Slug)
	}
	return nil
}

// DeleteTask removes a task. It refuses while the task runs in a route, unless
// forced.
func (s *Store) DeleteTask(ref string, force bool) error {
	return s.db.Tx(func(c store.Conn) error {
		t, err := findTask(c, "", ref)
		if err != nil {
			return err
		}
		if t.Route != "" && !force {
			return fmt.Errorf("%w: task %s runs in route %s; take it down first with `herdsman down %s`", ErrRefused, t.Slug, t.Route, t.Slug)
		}
		return purgeTask(c, t.ID)
	})
}

func purgeTask(c store.Conn, id string) error {
	for _, table := range []string{"task_links", "task_labels", "task_repos"} {
		if _, err := c.Exec("DELETE FROM "+table+" WHERE task_id = ?", id); err != nil {
			return err
		}
	}
	_, err := c.Exec("DELETE FROM tasks WHERE id = ?", id)
	return err
}

var linkNumber = regexp.MustCompile(`^[1-9][0-9]*$`)

// parseLink reads pr:<n>, issue:<n> or an http(s) URL.
func parseLink(spec string) (LinkKind, string, error) {
	if n, ok := strings.CutPrefix(spec, "pr:"); ok && linkNumber.MatchString(n) {
		return LinkPR, n, nil
	}
	if n, ok := strings.CutPrefix(spec, "issue:"); ok && linkNumber.MatchString(n) {
		return LinkIssue, n, nil
	}
	if strings.HasPrefix(spec, "https://") || strings.HasPrefix(spec, "http://") {
		return LinkURL, spec, nil
	}
	return "", "", fmt.Errorf("%w: link %q: want pr:<n>, issue:<n> or a URL", ErrInvalid, spec)
}

// LinkTask attaches a link to one of the task's repositories. The repository
// may be left empty when the task has exactly one. Repeating a link is a no-op.
func (s *Store) LinkTask(ref, repo, spec string) (*Task, error) {
	kind, target, err := parseLink(spec)
	if err != nil {
		return nil, err
	}
	var out *Task
	err = s.db.Tx(func(c store.Conn) error {
		t, err := findTask(c, "", ref)
		if err != nil {
			return err
		}
		repo, err := linkRepo(t, repo)
		if err != nil {
			return err
		}
		if _, err := c.Exec("INSERT OR IGNORE INTO task_links (task_id, repo, kind, target) VALUES (?, ?, ?, ?)", t.ID, repo, string(kind), target); err != nil {
			return err
		}
		out, err = loadTask(c, t.ID)
		return err
	})
	return out, err
}

func linkRepo(t *Task, repo string) (string, error) {
	if repo == "" {
		if len(t.Repos) != 1 {
			return "", fmt.Errorf("%w: task %s has %d repositories; name the repository to link", ErrInvalid, t.Slug, len(t.Repos))
		}
		return t.Repos[0], nil
	}
	for _, r := range t.Repos {
		if r == repo {
			return repo, nil
		}
	}
	return "", fmt.Errorf("%w: task %s does not name repository %s", ErrInvalid, t.Slug, repo)
}

func loadTask(c store.Conn, id string) (*Task, error) {
	var t *Task
	err := c.Query("SELECT project_id, slug, title, body, status, route, created, updated FROM tasks WHERE id = ?", []any{id}, func(r store.Row) error {
		t = &Task{ID: id, ProjectID: r.Text(0), Slug: r.Text(1), Title: r.Text(2), Body: r.Text(3), Status: Status(r.Text(4)), Route: r.Text(5), Created: r.Int64(6), Updated: r.Int64(7)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("%w: task %s", ErrNotFound, id)
	}
	if t.Repos, err = column(c, "SELECT repo FROM task_repos WHERE task_id = ? ORDER BY repo", id); err != nil {
		return nil, err
	}
	if t.Labels, err = column(c, "SELECT label FROM task_labels WHERE task_id = ? ORDER BY label", id); err != nil {
		return nil, err
	}
	err = c.Query("SELECT repo, kind, target FROM task_links WHERE task_id = ? ORDER BY repo, kind, target", []any{id}, func(r store.Row) error {
		t.Links = append(t.Links, Link{Repo: r.Text(0), Kind: LinkKind(r.Text(1)), Target: r.Text(2)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}
