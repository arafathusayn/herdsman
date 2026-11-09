package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"github.com/arafathusayn/herdsman/internal/project"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// recordsRPC serves ProjectService and TaskService. A change that reaches a
// folder's config is planned, then journaled and run, so one call finishes
// before the next plans against the same rows.
type recordsRPC struct {
	d  *Daemon
	mu sync.Mutex
}

func (s *recordsRPC) store() *project.Store { return project.New(s.d.acctDB, s.d.ops) }

// recordsError serves a failure of the records under the code of its class.
func recordsError(err error) error {
	switch {
	case errors.Is(err, project.ErrNotFound):
		return rpc.Errorf(rpc.NotFound, "%s", err.Error())
	case errors.Is(err, project.ErrInvalid):
		return rpc.Errorf(rpc.InvalidArgument, "%s", err.Error())
	case errors.Is(err, project.ErrConflict):
		return rpc.Errorf(rpc.AlreadyExists, "%s", err.Error())
	case errors.Is(err, project.ErrRefused):
		return rpc.Errorf(rpc.FailedPrecondition, "%s", err.Error())
	}
	return rpc.Errorf(rpc.Internal, "%s", err.Error())
}

func recordsScope(store *project.Store, sc *daemonv1.Scope) (project.Scope, error) {
	out := project.Scope{Project: sc.GetProject(), Folder: sc.GetFolder()}
	if route := sc.GetRoute().GetValue(); route != "" {
		id, err := store.ProjectOfRoute(route)
		if err != nil {
			return out, err
		}
		out.RouteProject = id
	}
	return out, nil
}

func resolveProject(store *project.Store, sc *daemonv1.Scope) (*project.Project, error) {
	scope, err := recordsScope(store, sc)
	if err != nil {
		return nil, err
	}
	return store.ResolveProject(scope)
}

// taskAt names the task of the linked worktree holding folder.
func taskAt(folder string) (string, bool, error) {
	if folder == "" {
		return "", false, nil
	}
	list, err := (&WorktreeService{}).List(context.Background(), WorktreeListRequest{Dir: folder})
	if err != nil {
		return "", false, err
	}
	for _, row := range list.Rows {
		if !row.Main && row.Path != "" && (folder == row.Path || strings.HasPrefix(folder, row.Path+string(filepath.Separator))) {
			return row.Task, true, nil
		}
	}
	return "", false, nil
}

func resolveTask(store *project.Store, sc *daemonv1.Scope, ref string) (*project.Task, error) {
	scope, err := recordsScope(store, sc)
	if err != nil {
		return nil, err
	}
	return store.ResolveTask(ref, scope, taskAt)
}

func reposIn(list []*corev1.ProjectRepo) []project.Repo {
	out := make([]project.Repo, 0, len(list))
	for _, r := range list {
		out = append(out, project.Repo{Name: r.GetName(), Path: r.GetPath(), DefaultBranch: r.GetDefaultBranch(), Remote: r.GetRemote()})
	}
	return out
}

func projectOut(p *project.Project) *corev1.Project {
	out := &corev1.Project{Id: &corev1.ProjectId{Value: p.ID}, Name: p.Name, Description: p.Description, State: string(p.State),
		Folders: p.Folders, OpenTasks: int32(p.OpenTasks), Created: p.Created, Updated: p.Updated}
	for _, r := range p.Repos {
		out.Repos = append(out.Repos, &corev1.ProjectRepo{Name: r.Name, Path: r.Path, DefaultBranch: r.DefaultBranch, Remote: r.Remote})
	}
	return out
}

func taskOut(t *project.Task) *corev1.Task {
	out := &corev1.Task{Id: &corev1.TaskId{Value: t.ID}, Project: &corev1.ProjectId{Value: t.ProjectID}, Slug: t.Slug, Title: t.Title, Body: t.Body,
		Status: string(t.Status), Repos: t.Repos, Labels: t.Labels, Created: t.Created, Updated: t.Updated}
	if t.Route != "" {
		out.Route = &corev1.RouteId{Value: t.Route}
	}
	for _, l := range t.Links {
		out.Links = append(out.Links, &corev1.TaskLink{Repo: l.Repo, Kind: string(l.Kind), Target: l.Target})
	}
	return out
}

// projectRPC is the ProjectService face of the records.
type projectRPC struct{ *recordsRPC }

// Create stores a project and, when asked, binds a folder to it. A bind that
// fails removes the project again, so a refused call leaves nothing.
func (s projectRPC) Create(_ context.Context, req *daemonv1.ProjectServiceCreateRequest) (*daemonv1.ProjectServiceCreateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.store()
	p, err := store.CreateProject(project.ProjectInput{Name: req.GetName(), Description: req.GetDescription(), Repos: reposIn(req.GetRepos())})
	if err != nil {
		return nil, recordsError(err)
	}
	if req.GetFolder() != "" {
		bound, err := store.Bind(p.ID, req.GetFolder())
		if err != nil {
			return nil, recordsError(errors.Join(err, store.DeleteProject(p.ID, false)))
		}
		p = bound
	}
	return &daemonv1.ProjectServiceCreateResponse{Project: projectOut(p)}, nil
}

// Get returns the project a scope names.
func (s projectRPC) Get(_ context.Context, req *daemonv1.ProjectServiceGetRequest) (*daemonv1.ProjectServiceGetResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := resolveProject(s.store(), req.GetScope())
	if err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.ProjectServiceGetResponse{Project: projectOut(p)}, nil
}

// List returns the projects by name.
func (s projectRPC) List(_ context.Context, req *daemonv1.ProjectServiceListRequest) (*daemonv1.ProjectServiceListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.store().ListProjects(req.GetIncludeArchived())
	if err != nil {
		return nil, recordsError(err)
	}
	out := &daemonv1.ProjectServiceListResponse{}
	for _, p := range list {
		out.Projects = append(out.Projects, projectOut(p))
	}
	return out, nil
}

// Update changes a project's fields and repositories.
func (s projectRPC) Update(_ context.Context, req *daemonv1.ProjectServiceUpdateRequest) (*daemonv1.ProjectServiceUpdateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.store()
	p, err := resolveProject(store, req.GetScope())
	if err != nil {
		return nil, recordsError(err)
	}
	updated, err := store.UpdateProject(p.ID, project.ProjectPatch{Name: req.Name, Description: req.Description, AddRepos: reposIn(req.GetAddRepos()), RemoveRepos: req.GetRemoveRepos()})
	if err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.ProjectServiceUpdateResponse{Project: projectOut(updated)}, nil
}

// Delete removes a project, or archives it when forced.
func (s projectRPC) Delete(_ context.Context, req *daemonv1.ProjectServiceDeleteRequest) (*daemonv1.ProjectServiceDeleteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.store()
	p, err := resolveProject(store, req.GetScope())
	if err != nil {
		return nil, recordsError(err)
	}
	if err := store.DeleteProject(p.ID, req.GetForce()); err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.ProjectServiceDeleteResponse{}, nil
}

// Bind ties a folder to a project.
func (s projectRPC) Bind(_ context.Context, req *daemonv1.ProjectServiceBindRequest) (*daemonv1.ProjectServiceBindResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.store()
	p, err := resolveProject(store, req.GetScope())
	if err != nil {
		return nil, recordsError(err)
	}
	bound, err := store.Bind(p.ID, req.GetFolder())
	if err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.ProjectServiceBindResponse{Project: projectOut(bound)}, nil
}

// Unbind releases a folder.
func (s projectRPC) Unbind(_ context.Context, req *daemonv1.ProjectServiceUnbindRequest) (*daemonv1.ProjectServiceUnbindResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.store().Unbind(req.GetFolder())
	if err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.ProjectServiceUnbindResponse{Project: projectOut(p)}, nil
}

// taskRPC is the TaskService face of the records.
type taskRPC struct{ *recordsRPC }

// Create stores a task under the project the scope names.
func (s taskRPC) Create(_ context.Context, req *daemonv1.TaskServiceCreateRequest) (*daemonv1.TaskServiceCreateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.store()
	p, err := resolveProject(store, req.GetScope())
	if err != nil {
		return nil, recordsError(err)
	}
	t, err := store.CreateTask(project.TaskInput{ProjectID: p.ID, Title: req.GetTitle(), Slug: req.GetSlug(), Body: req.GetBody(), Labels: req.GetLabels(), Repos: req.GetRepos()})
	if err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.TaskServiceCreateResponse{Task: taskOut(t)}, nil
}

// Get returns one task.
func (s taskRPC) Get(_ context.Context, req *daemonv1.TaskServiceGetRequest) (*daemonv1.TaskServiceGetResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := resolveTask(s.store(), req.GetScope(), req.GetTask())
	if err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.TaskServiceGetResponse{Task: taskOut(t)}, nil
}

// List returns the matching tasks: every project's when asked, otherwise those
// of the project the scope names.
func (s taskRPC) List(_ context.Context, req *daemonv1.TaskServiceListRequest) (*daemonv1.TaskServiceListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.store()
	filter := project.TaskFilter{Status: project.Status(req.GetStatus()), Repo: req.GetRepo(), Label: req.GetLabel()}
	if !req.GetAll() {
		p, err := resolveProject(store, req.GetScope())
		if err != nil {
			return nil, recordsError(err)
		}
		filter.ProjectID = p.ID
	}
	list, err := store.ListTasks(filter)
	if err != nil {
		return nil, recordsError(err)
	}
	out := &daemonv1.TaskServiceListResponse{}
	for _, t := range list {
		out.Tasks = append(out.Tasks, taskOut(t))
	}
	return out, nil
}

// Update changes a task. A scope naming another project is refused rather than
// read as a move, because a task's project is written once.
func (s taskRPC) Update(_ context.Context, req *daemonv1.TaskServiceUpdateRequest) (*daemonv1.TaskServiceUpdateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.store()
	t, err := resolveTask(store, req.GetScope(), req.GetTask())
	if err != nil {
		return nil, recordsError(err)
	}
	if named := req.GetScope().GetProject(); named != "" && named != "@" {
		p, err := store.GetProject(named)
		if err != nil {
			return nil, recordsError(err)
		}
		if p.ID != t.ProjectID {
			return nil, rpc.Errorf(rpc.FailedPrecondition, "task %s belongs to another project, and a task's project cannot change", t.Slug)
		}
	}
	var status *project.Status
	if req.Status != nil {
		v := project.Status(req.GetStatus())
		status = &v
	}
	updated, err := store.UpdateTask(t.ID, project.TaskPatch{Title: req.Title, Body: req.Body, Status: status,
		AddRepos: req.GetAddRepos(), RemoveRepos: req.GetRemoveRepos(), AddLabels: req.GetAddLabels(), RemoveLabels: req.GetRemoveLabels()})
	if err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.TaskServiceUpdateResponse{Task: taskOut(updated)}, nil
}

// Delete removes a task.
func (s taskRPC) Delete(_ context.Context, req *daemonv1.TaskServiceDeleteRequest) (*daemonv1.TaskServiceDeleteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.store()
	t, err := resolveTask(store, req.GetScope(), req.GetTask())
	if err != nil {
		return nil, recordsError(err)
	}
	if err := store.DeleteTask(t.ID, req.GetForce()); err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.TaskServiceDeleteResponse{}, nil
}

// Link attaches a pull request, an issue or a URL to a task.
func (s taskRPC) Link(_ context.Context, req *daemonv1.TaskServiceLinkRequest) (*daemonv1.TaskServiceLinkResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.store()
	t, err := resolveTask(store, req.GetScope(), req.GetTask())
	if err != nil {
		return nil, recordsError(err)
	}
	linked, err := store.LinkTask(t.ID, req.GetRepo(), req.GetLink())
	if err != nil {
		return nil, recordsError(err)
	}
	return &daemonv1.TaskServiceLinkResponse{Task: taskOut(linked)}, nil
}
