package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func ptrTo[T any](v T) *T { return &v }

func recordsRepo(name string) *corev1.ProjectRepo {
	return &corev1.ProjectRepo{Name: name, Path: "/src/" + name, DefaultBranch: "main", Remote: "origin"}
}

func byName(name string) *daemonv1.Scope { return &daemonv1.Scope{Project: name} }

func createProject(t *testing.T, c *client.Client, name string, repos ...string) *corev1.Project {
	t.Helper()
	req := &daemonv1.ProjectServiceCreateRequest{Name: name}
	for _, r := range repos {
		req.Repos = append(req.Repos, recordsRepo(r))
	}
	res, err := c.Project.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return res.GetProject()
}

func createTask(t *testing.T, c *client.Client, project, title string) *corev1.Task {
	t.Helper()
	res, err := c.Task.Create(context.Background(), &daemonv1.TaskServiceCreateRequest{Scope: byName(project), Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return res.GetTask()
}

func wantRecordsCode(t *testing.T, err error, code rpc.Code) {
	t.Helper()
	if codeOf(err) != code {
		t.Fatalf("error %v, want %s", err, code)
	}
}

func TestProjectCRUDThroughTheSDK(t *testing.T) {
	c := routeClient(t, newHarness(t))
	ctx := context.Background()
	made := createProject(t, c, "orchard", "api", "web")
	if made.GetId().GetValue() == "" || made.GetState() != "active" || len(made.GetRepos()) != 2 || made.GetRepos()[0].GetDefaultBranch() != "main" {
		t.Fatalf("%+v", made)
	}
	got, err := c.Project.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: byName("orchard")})
	if err != nil || got.GetProject().GetId().GetValue() != made.GetId().GetValue() {
		t.Fatalf("%+v %v", got, err)
	}
	updated, err := c.Project.Update(ctx, &daemonv1.ProjectServiceUpdateRequest{Scope: byName("orchard"), Name: ptrTo("grove"), Description: ptrTo("trees"),
		AddRepos: []*corev1.ProjectRepo{recordsRepo("docs")}, RemoveRepos: []string{"web"}})
	if err != nil || updated.GetProject().GetName() != "grove" || updated.GetProject().GetDescription() != "trees" || len(updated.GetProject().GetRepos()) != 2 {
		t.Fatalf("%+v %v", updated, err)
	}
	createProject(t, c, "second")
	if _, err := c.Project.Delete(ctx, &daemonv1.ProjectServiceDeleteRequest{Scope: byName("second"), Force: true}); err != nil {
		t.Fatal(err)
	}
	active, err := c.Project.List(ctx, &daemonv1.ProjectServiceListRequest{})
	if err != nil || len(active.GetProjects()) != 1 {
		t.Fatalf("%+v %v", active, err)
	}
	every, err := c.Project.List(ctx, &daemonv1.ProjectServiceListRequest{IncludeArchived: true})
	if err != nil || len(every.GetProjects()) != 2 {
		t.Fatalf("%+v %v", every, err)
	}
	if _, err := c.Project.Delete(ctx, &daemonv1.ProjectServiceDeleteRequest{Scope: byName("grove")}); err != nil {
		t.Fatal(err)
	}
	_, err = c.Project.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: byName("grove")})
	wantRecordsCode(t, err, rpc.NotFound)
}

// A bind reaches the operation engine, so this fails on a daemon whose engine
// does not know the records' step kinds.
func TestBindJournalsThroughTheDaemonEngine(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	made, err := c.Project.Create(ctx, &daemonv1.ProjectServiceCreateRequest{Name: "orchard", Folder: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(made.GetProject().GetFolders()) != 1 {
		t.Fatalf("%+v", made)
	}
	config := filepath.Join(dir, "herdsman.config.toml")
	text, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(text), made.GetProject().GetId().GetValue()) {
		t.Fatalf("%q %v", text, err)
	}
	here, err := c.Project.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: &daemonv1.Scope{Project: "@", Folder: dir}})
	if err != nil || here.GetProject().GetName() != "orchard" {
		t.Fatalf("%+v %v", here, err)
	}
	released, err := c.Project.Unbind(ctx, &daemonv1.ProjectServiceUnbindRequest{Folder: dir})
	if err != nil || len(released.GetProject().GetFolders()) != 0 {
		t.Fatalf("%+v %v", released, err)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("config stayed: %v", err)
	}
	rebound, err := c.Project.Bind(ctx, &daemonv1.ProjectServiceBindRequest{Scope: byName("orchard"), Folder: dir})
	if err != nil || len(rebound.GetProject().GetFolders()) != 1 {
		t.Fatalf("%+v %v", rebound, err)
	}
}

func TestCreateWithABadFolderLeavesNoProject(t *testing.T) {
	c := routeClient(t, newHarness(t))
	ctx := context.Background()
	_, err := c.Project.Create(ctx, &daemonv1.ProjectServiceCreateRequest{Name: "orchard", Folder: "relative/path"})
	wantRecordsCode(t, err, rpc.InvalidArgument)
	list, err := c.Project.List(ctx, &daemonv1.ProjectServiceListRequest{IncludeArchived: true})
	if err != nil || len(list.GetProjects()) != 0 {
		t.Fatalf("a refused create left %+v %v", list, err)
	}
}

func TestTaskCRUDThroughTheSDK(t *testing.T) {
	c := routeClient(t, newHarness(t))
	ctx := context.Background()
	createProject(t, c, "orchard", "api", "web")
	made, err := c.Task.Create(ctx, &daemonv1.TaskServiceCreateRequest{Scope: byName("orchard"), Title: "Fix lights", Body: "b", Labels: []string{"x"}, Repos: []string{"api", "web"}})
	if err != nil {
		t.Fatal(err)
	}
	task := made.GetTask()
	if task.GetSlug() != "fix-lights" || task.GetStatus() != "todo" || task.GetBody() != "b" || len(task.GetRepos()) != 2 || task.GetProject().GetValue() == "" {
		t.Fatalf("%+v", task)
	}
	got, err := c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: byName("orchard"), Task: "fix-lights"})
	if err != nil || got.GetTask().GetId().GetValue() != task.GetId().GetValue() {
		t.Fatalf("%+v %v", got, err)
	}
	linked, err := c.Task.Link(ctx, &daemonv1.TaskServiceLinkRequest{Scope: byName("orchard"), Task: "fix-lights", Repo: "web", Link: "pr:7"})
	if err != nil || len(linked.GetTask().GetLinks()) != 1 || linked.GetTask().GetLinks()[0].GetTarget() != "7" || linked.GetTask().GetLinks()[0].GetKind() != "pr" {
		t.Fatalf("%+v %v", linked, err)
	}
	updated, err := c.Task.Update(ctx, &daemonv1.TaskServiceUpdateRequest{Scope: byName("orchard"), Task: "fix-lights", Title: ptrTo("New"), Body: ptrTo("c"), Status: ptrTo("doing"),
		RemoveRepos: []string{"web"}, AddLabels: []string{"y"}, RemoveLabels: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	u := updated.GetTask()
	if u.GetTitle() != "New" || u.GetBody() != "c" || u.GetStatus() != "doing" || len(u.GetRepos()) != 1 || len(u.GetLinks()) != 0 || len(u.GetLabels()) != 1 || u.GetLabels()[0] != "y" {
		t.Fatalf("%+v", u)
	}
	createProject(t, c, "other", "api")
	createTask(t, c, "other", "Elsewhere")
	for _, tc := range []struct {
		req  *daemonv1.TaskServiceListRequest
		want int
	}{
		{&daemonv1.TaskServiceListRequest{Scope: byName("orchard")}, 1},
		{&daemonv1.TaskServiceListRequest{All: true}, 2},
		{&daemonv1.TaskServiceListRequest{All: true, Status: "doing"}, 1},
		{&daemonv1.TaskServiceListRequest{All: true, Repo: "web"}, 0},
		{&daemonv1.TaskServiceListRequest{All: true, Label: "y"}, 1},
	} {
		list, err := c.Task.List(ctx, tc.req)
		if err != nil || len(list.GetTasks()) != tc.want {
			t.Fatalf("%v: %+v %v", tc.req, list, err)
		}
	}
	if _, err := c.Task.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Scope: byName("orchard"), Task: "fix-lights"}); err != nil {
		t.Fatal(err)
	}
	_, err = c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: byName("orchard"), Task: "fix-lights"})
	wantRecordsCode(t, err, rpc.NotFound)
}

// A task's project is written once: the update message has nowhere to name a
// new one, and a scope that disagrees with the task is refused, not ignored.
func TestTaskUpdateCannotChangeTheProject(t *testing.T) {
	c := routeClient(t, newHarness(t))
	ctx := context.Background()
	home := createProject(t, c, "orchard", "api")
	createProject(t, c, "other", "api")
	task := createTask(t, c, "orchard", "Stay")

	fields := (&daemonv1.TaskServiceUpdateRequest{}).ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		name := string(fields.Get(i).Name())
		if strings.Contains(name, "project") {
			t.Errorf("the update request has a field %s", name)
		}
	}
	if fields.ByName(protoreflect.Name("scope")) == nil {
		t.Error("the update request lost its scope")
	}

	_, err := c.Task.Update(ctx, &daemonv1.TaskServiceUpdateRequest{Scope: byName("other"), Task: task.GetId().GetValue(), Title: ptrTo("Moved")})
	wantRecordsCode(t, err, rpc.FailedPrecondition)
	_, err = c.Task.Update(ctx, &daemonv1.TaskServiceUpdateRequest{Scope: byName("other"), Task: task.GetSlug(), Title: ptrTo("Moved")})
	wantRecordsCode(t, err, rpc.NotFound)

	got, err := c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Task: task.GetId().GetValue()})
	if err != nil || got.GetTask().GetTitle() != "Stay" || got.GetTask().GetProject().GetValue() != home.GetId().GetValue() {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRecordsMapFailuresToCodes(t *testing.T) {
	c := routeClient(t, newHarness(t))
	ctx := context.Background()
	createProject(t, c, "orchard", "api")
	createTask(t, c, "orchard", "Open")

	_, err := c.Project.Create(ctx, &daemonv1.ProjectServiceCreateRequest{Name: " "})
	wantRecordsCode(t, err, rpc.InvalidArgument)
	_, err = c.Project.Create(ctx, &daemonv1.ProjectServiceCreateRequest{Name: "orchard"})
	wantRecordsCode(t, err, rpc.AlreadyExists)
	_, err = c.Project.Delete(ctx, &daemonv1.ProjectServiceDeleteRequest{Scope: byName("orchard")})
	wantRecordsCode(t, err, rpc.FailedPrecondition)
	_, err = c.Project.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: byName("nope")})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Project.Update(ctx, &daemonv1.ProjectServiceUpdateRequest{Scope: byName("nope")})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Project.Bind(ctx, &daemonv1.ProjectServiceBindRequest{Scope: byName("nope"), Folder: "/"})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Project.Unbind(ctx, &daemonv1.ProjectServiceUnbindRequest{Folder: "/not/bound"})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Task.Create(ctx, &daemonv1.TaskServiceCreateRequest{Scope: byName("nope"), Title: "x"})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Task.List(ctx, &daemonv1.TaskServiceListRequest{Scope: byName("nope")})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Task.List(ctx, &daemonv1.TaskServiceListRequest{All: true, Status: "bad"})
	wantRecordsCode(t, err, rpc.InvalidArgument)
	_, err = c.Task.Update(ctx, &daemonv1.TaskServiceUpdateRequest{Scope: byName("orchard"), Task: "open", Status: ptrTo("bad")})
	wantRecordsCode(t, err, rpc.InvalidArgument)
	_, err = c.Task.Update(ctx, &daemonv1.TaskServiceUpdateRequest{Scope: byName("orchard"), Task: "nope"})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Task.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Scope: byName("orchard"), Task: "nope"})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Task.Link(ctx, &daemonv1.TaskServiceLinkRequest{Scope: byName("orchard"), Task: "nope", Link: "pr:1"})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Task.Link(ctx, &daemonv1.TaskServiceLinkRequest{Scope: byName("orchard"), Task: "open", Link: "bad"})
	wantRecordsCode(t, err, rpc.InvalidArgument)
}

func TestRecordsScopeComesFromARoute(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	made := createProject(t, c, "orchard", "api")
	task := createTask(t, c, "orchard", "Run")
	if _, err := h.d.acctDB.Exec("UPDATE tasks SET route = 'live' WHERE id = ?", task.GetId().GetValue()); err != nil {
		t.Fatal(err)
	}
	scope := &daemonv1.Scope{Route: &corev1.RouteId{Value: "live"}}
	got, err := c.Project.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: scope})
	if err != nil || got.GetProject().GetId().GetValue() != made.GetId().GetValue() {
		t.Fatalf("%+v %v", got, err)
	}
	if got.GetProject().GetOpenTasks() != 1 {
		t.Fatalf("open tasks %d", got.GetProject().GetOpenTasks())
	}
	byRoute, err := c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: scope, Task: "run"})
	if err != nil || byRoute.GetTask().GetRoute().GetValue() != "live" {
		t.Fatalf("%+v %v", byRoute, err)
	}

	createProject(t, c, "other", "api")
	second := createTask(t, c, "other", "Also")
	if _, err := h.d.acctDB.Exec("UPDATE tasks SET route = 'live' WHERE id = ?", second.GetId().GetValue()); err != nil {
		t.Fatal(err)
	}
	_, err = c.Project.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: scope})
	wantRecordsCode(t, err, rpc.AlreadyExists)
	_, err = c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: scope, Task: "run"})
	wantRecordsCode(t, err, rpc.AlreadyExists)
}

func recordsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCurrentTaskComesFromTheWorktree(t *testing.T) {
	c := routeClient(t, newHarness(t))
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	recordsGit(t, repo, "init", "-q", "-b", "main")
	recordsGit(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	tree := filepath.Join(root, "fix-lights")
	recordsGit(t, repo, "worktree", "add", "-q", "-b", "fix-lights", tree)

	createProject(t, c, "orchard", "api")
	task := createTask(t, c, "orchard", "Fix lights")

	got, err := c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: &daemonv1.Scope{Project: "orchard", Folder: tree}, Task: "@"})
	if err != nil || got.GetTask().GetId().GetValue() != task.GetId().GetValue() {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: &daemonv1.Scope{Project: "orchard", Folder: repo}, Task: "@"})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: &daemonv1.Scope{Project: "orchard"}, Task: "@"})
	wantRecordsCode(t, err, rpc.NotFound)
	_, err = c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: &daemonv1.Scope{Project: "orchard", Folder: filepath.Join(root, "gone")}, Task: "@"})
	if err == nil {
		t.Fatal("a folder that is not a repository named a task")
	}
}

// restartInspecting brings the daemon back in inspect mode, which is fixed
// when it starts, and returns a client of the new one.
func restartInspecting(t *testing.T, h *harness) *client.Client {
	t.Helper()
	h.stop()
	h.cfg.Inspect = true
	h.start()
	return routeClient(t, h)
}

func TestInspectDaemonChangesNoRecords(t *testing.T) {
	c := restartInspecting(t, newHarness(t))
	ctx := context.Background()
	calls := []func() error{
		func() error {
			_, err := c.Project.Create(ctx, &daemonv1.ProjectServiceCreateRequest{Name: "x"})
			return err
		},
		func() error {
			_, err := c.Project.Update(ctx, &daemonv1.ProjectServiceUpdateRequest{Scope: byName("x")})
			return err
		},
		func() error {
			_, err := c.Project.Delete(ctx, &daemonv1.ProjectServiceDeleteRequest{Scope: byName("x")})
			return err
		},
		func() error {
			_, err := c.Project.Bind(ctx, &daemonv1.ProjectServiceBindRequest{Scope: byName("x"), Folder: "/"})
			return err
		},
		func() error {
			_, err := c.Project.Unbind(ctx, &daemonv1.ProjectServiceUnbindRequest{Folder: "/"})
			return err
		},
		func() error {
			_, err := c.Task.Create(ctx, &daemonv1.TaskServiceCreateRequest{Scope: byName("x"), Title: "t"})
			return err
		},
		func() error {
			_, err := c.Task.Update(ctx, &daemonv1.TaskServiceUpdateRequest{Scope: byName("x"), Task: "t"})
			return err
		},
		func() error {
			_, err := c.Task.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Scope: byName("x"), Task: "t"})
			return err
		},
		func() error {
			_, err := c.Task.Link(ctx, &daemonv1.TaskServiceLinkRequest{Scope: byName("x"), Task: "t", Link: "pr:1"})
			return err
		},
	}
	for i, call := range calls {
		if err := call(); codeOf(err) != rpc.FailedPrecondition {
			t.Errorf("call %d: %v", i, err)
		}
	}
	if _, err := c.Project.List(ctx, &daemonv1.ProjectServiceListRequest{}); err != nil {
		t.Errorf("a read was refused: %v", err)
	}
}

func putOnRoute(t *testing.T, h *harness, route string, tasks ...*corev1.Task) {
	t.Helper()
	for _, task := range tasks {
		if _, err := h.d.acctDB.Exec("UPDATE tasks SET route = ? WHERE id = ?", route, task.GetId().GetValue()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExplicitScopeWinsOverAnAmbiguousRoute(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	first := createProject(t, c, "orchard", "api")
	createProject(t, c, "other", "api")
	putOnRoute(t, h, "live", createTask(t, c, "orchard", "One"), createTask(t, c, "other", "Two"))

	named := &daemonv1.Scope{Project: "orchard", Route: routeID("live")}
	got, err := c.Project.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: named})
	if err != nil || got.GetProject().GetId().GetValue() != first.GetId().GetValue() {
		t.Fatalf("a named project lost to the route: %+v %v", got, err)
	}

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Project.Bind(ctx, &daemonv1.ProjectServiceBindRequest{Scope: byName("orchard"), Folder: dir}); err != nil {
		t.Fatal(err)
	}
	bound := &daemonv1.Scope{Project: "@", Folder: dir, Route: routeID("live")}
	got, err = c.Project.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: bound})
	if err != nil || got.GetProject().GetId().GetValue() != first.GetId().GetValue() {
		t.Fatalf("a bound folder lost to the route: %+v %v", got, err)
	}
}

func recordsRepoWithWorktree(t *testing.T, branch, folder string) (repo, tree string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, tree = filepath.Join(root, "repo"), filepath.Join(root, folder)
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	recordsGit(t, repo, "init", "-q", "-b", "main")
	recordsGit(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	recordsGit(t, repo, "worktree", "add", "-q", "-b", branch, tree)
	return repo, tree
}

func TestCurrentTaskLookupWritesNothingOnAnInspectDaemon(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo, tree := recordsRepoWithWorktree(t, "fix-lights", "fix-lights")
	task := createTask(t, c, createProject(t, c, "orchard", "api").GetName(), "Fix lights")
	c = restartInspecting(t, h)

	got, err := c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: &daemonv1.Scope{Project: "orchard", Folder: filepath.Join(tree)}, Task: "@"})
	if err != nil || got.GetTask().GetId().GetValue() != task.GetId().GetValue() {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "herdsman")); !os.IsNotExist(err) {
		t.Fatalf("the lookup wrote worktree state: %v", err)
	}
}

func TestTaskDeleteRefusesAnExistingWorktree(t *testing.T) {
	c := routeClient(t, newHarness(t))
	ctx := context.Background()
	repo, tree := recordsRepoWithWorktree(t, "one", "one")
	made, err := c.Project.Create(ctx, &daemonv1.ProjectServiceCreateRequest{Name: "orchard", Repos: []*corev1.ProjectRepo{{Name: "api", Path: repo}}})
	if err != nil {
		t.Fatal(err)
	}
	task := createTask(t, c, made.GetProject().GetName(), "One")
	byID := task.GetId().GetValue()

	_, err = c.Task.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Task: byID})
	wantRecordsCode(t, err, rpc.FailedPrecondition)
	if !strings.Contains(err.Error(), "down") || !strings.Contains(err.Error(), tree) {
		t.Fatalf("the refusal does not name the way out: %v", err)
	}
	if _, err := c.Task.Get(ctx, &daemonv1.TaskServiceGetRequest{Task: byID}); err != nil {
		t.Fatalf("the refused delete lost the task: %v", err)
	}

	recordsGit(t, repo, "worktree", "remove", tree)
	if _, err := c.Task.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Task: byID}); err != nil {
		t.Fatalf("no worktree remains: %v", err)
	}
}

func TestTaskDeleteFindsAWorktreeByItsBranch(t *testing.T) {
	c := routeClient(t, newHarness(t))
	ctx := context.Background()
	repo, _ := recordsRepoWithWorktree(t, "feat/one", "elsewhere")
	made, err := c.Project.Create(ctx, &daemonv1.ProjectServiceCreateRequest{Name: "orchard", Repos: []*corev1.ProjectRepo{{Name: "api", Path: repo}}})
	if err != nil {
		t.Fatal(err)
	}
	task := createTask(t, c, made.GetProject().GetName(), "One")
	_, err = c.Task.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Task: task.GetId().GetValue()})
	wantRecordsCode(t, err, rpc.FailedPrecondition)
	if _, err := c.Task.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Task: task.GetId().GetValue(), Force: true}); err != nil {
		t.Fatalf("force: %v", err)
	}
}

func TestTaskDeleteIgnoresARepositoryThatIsGoneOrNotGit(t *testing.T) {
	c := routeClient(t, newHarness(t))
	ctx := context.Background()
	plain, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	made, err := c.Project.Create(ctx, &daemonv1.ProjectServiceCreateRequest{Name: "orchard", Repos: []*corev1.ProjectRepo{{Name: "plain", Path: plain}, {Name: "gone", Path: filepath.Join(plain, "gone")}}})
	if err != nil {
		t.Fatal(err)
	}
	task := createTask(t, c, made.GetProject().GetName(), "One")
	if _, err := c.Task.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Task: task.GetId().GetValue()}); err != nil {
		t.Fatal(err)
	}
}
