package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// fakeRecords remembers the last request and answers with one record.
type fakeRecords struct {
	daemonv1.ProjectService
	daemonv1.TaskService
	last any
	err  error
}

var (
	oneProject = &corev1.Project{Id: &corev1.ProjectId{Value: "P1"}, Name: "orchard", State: "active", Folders: []string{"/w"}, Repos: []*corev1.ProjectRepo{{Name: "api", Path: "/src/api"}}}
	oneTask    = &corev1.Task{Id: &corev1.TaskId{Value: "T1"}, Slug: "lights", Title: "Lights", Status: "todo", Repos: []string{"api"}, Labels: []string{"x"},
		Links: []*corev1.TaskLink{{Repo: "api", Kind: "pr", Target: "7"}}}
)

func answer[T any](f *fakeRecords, req any, res T) (T, error) {
	f.last = req
	if f.err != nil {
		var zero T
		return zero, f.err
	}
	return res, nil
}

type fakeProjects struct{ *fakeRecords }

func (f fakeProjects) Create(_ context.Context, r *daemonv1.ProjectServiceCreateRequest) (*daemonv1.ProjectServiceCreateResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.ProjectServiceCreateResponse{Project: oneProject})
}
func (f fakeProjects) Get(_ context.Context, r *daemonv1.ProjectServiceGetRequest) (*daemonv1.ProjectServiceGetResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.ProjectServiceGetResponse{Project: oneProject})
}
func (f fakeProjects) List(_ context.Context, r *daemonv1.ProjectServiceListRequest) (*daemonv1.ProjectServiceListResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.ProjectServiceListResponse{Projects: []*corev1.Project{oneProject}})
}
func (f fakeProjects) Update(_ context.Context, r *daemonv1.ProjectServiceUpdateRequest) (*daemonv1.ProjectServiceUpdateResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.ProjectServiceUpdateResponse{Project: oneProject})
}
func (f fakeProjects) Delete(_ context.Context, r *daemonv1.ProjectServiceDeleteRequest) (*daemonv1.ProjectServiceDeleteResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.ProjectServiceDeleteResponse{})
}
func (f fakeProjects) Bind(_ context.Context, r *daemonv1.ProjectServiceBindRequest) (*daemonv1.ProjectServiceBindResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.ProjectServiceBindResponse{Project: oneProject})
}
func (f fakeProjects) Unbind(_ context.Context, r *daemonv1.ProjectServiceUnbindRequest) (*daemonv1.ProjectServiceUnbindResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.ProjectServiceUnbindResponse{Project: oneProject})
}

type fakeTasks struct{ *fakeRecords }

func (f fakeTasks) Create(_ context.Context, r *daemonv1.TaskServiceCreateRequest) (*daemonv1.TaskServiceCreateResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.TaskServiceCreateResponse{Task: oneTask})
}
func (f fakeTasks) Get(_ context.Context, r *daemonv1.TaskServiceGetRequest) (*daemonv1.TaskServiceGetResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.TaskServiceGetResponse{Task: oneTask})
}
func (f fakeTasks) List(_ context.Context, r *daemonv1.TaskServiceListRequest) (*daemonv1.TaskServiceListResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.TaskServiceListResponse{Tasks: []*corev1.Task{oneTask}})
}
func (f fakeTasks) Update(_ context.Context, r *daemonv1.TaskServiceUpdateRequest) (*daemonv1.TaskServiceUpdateResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.TaskServiceUpdateResponse{Task: oneTask})
}
func (f fakeTasks) Delete(_ context.Context, r *daemonv1.TaskServiceDeleteRequest) (*daemonv1.TaskServiceDeleteResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.TaskServiceDeleteResponse{})
}
func (f fakeTasks) Link(_ context.Context, r *daemonv1.TaskServiceLinkRequest) (*daemonv1.TaskServiceLinkResponse, error) {
	return answer(f.fakeRecords, r, &daemonv1.TaskServiceLinkResponse{Task: oneTask})
}

func withRecords(t *testing.T, f *fakeRecords) {
	t.Helper()
	old := recordsDial
	recordsDial = func() (daemonv1.ProjectService, daemonv1.TaskService, func(), error) {
		if f == nil {
			return nil, nil, nil, errors.New("the daemon is not running")
		}
		return fakeProjects{f}, fakeTasks{f}, func() {}, nil
	}
	t.Cleanup(func() { recordsDial = old })
}

func runRecords(run func([]string, *bytes.Buffer, *bytes.Buffer) int, args ...string) (int, string, string) {
	var out, errs bytes.Buffer
	code := run(args, &out, &errs)
	return code, out.String(), errs.String()
}

func projectArgs(args ...string) (int, string, string) {
	return runRecords(func(a []string, o, e *bytes.Buffer) int { return runProject(a, strings.NewReader(""), o, e) }, args...)
}

func taskArgs(args ...string) (int, string, string) {
	return runRecords(func(a []string, o, e *bytes.Buffer) int { return runTask(a, strings.NewReader(""), o, e) }, args...)
}

func TestProjectVerbsSendTheirRequests(t *testing.T) {
	f := &fakeRecords{}
	withRecords(t, f)
	here, err := workingFolder()
	if err != nil {
		t.Fatal(err)
	}

	if code, out, errs := projectArgs("create", "orchard", "--description", "d", "--repo", "/src/api", "--repo", "docs=/src/d:trunk:up", "--bind"); code != 0 || !strings.Contains(out, "orchard") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	c := f.last.(*daemonv1.ProjectServiceCreateRequest)
	if c.GetName() != "orchard" || c.GetDescription() != "d" || c.GetFolder() != here || len(c.GetRepos()) != 2 {
		t.Fatalf("%v", c)
	}
	if r := c.GetRepos()[0]; r.GetName() != "" || r.GetPath() != "/src/api" {
		t.Fatalf("%v", r)
	}
	if r := c.GetRepos()[1]; r.GetName() != "docs" || r.GetPath() != "/src/d" || r.GetDefaultBranch() != "trunk" || r.GetRemote() != "up" {
		t.Fatalf("%v", r)
	}

	if code, _, _ := projectArgs("show"); code != 0 || f.last.(*daemonv1.ProjectServiceGetRequest).GetScope().GetFolder() != here {
		t.Fatalf("show %d %v", code, f.last)
	}
	if code, _, _ := projectArgs("show", "orchard"); code != 0 || f.last.(*daemonv1.ProjectServiceGetRequest).GetScope().GetProject() != "orchard" {
		t.Fatalf("show named %d %v", code, f.last)
	}
	if code, out, _ := projectArgs("list", "--archived"); code != 0 || !f.last.(*daemonv1.ProjectServiceListRequest).GetIncludeArchived() || !strings.Contains(out, "orchard") {
		t.Fatalf("list %d %q", code, out)
	}

	if code, _, errs := projectArgs("update", "orchard", "--name", "grove", "--description", "", "--add-repo", "web=/src/w", "--remove-repo", "api"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	u := f.last.(*daemonv1.ProjectServiceUpdateRequest)
	if u.Name == nil || u.GetName() != "grove" || u.Description == nil || u.GetDescription() != "" || len(u.GetAddRepos()) != 1 || u.GetRemoveRepos()[0] != "api" {
		t.Fatalf("%v", u)
	}
	if code, _, _ := projectArgs("update", "orchard"); code != 0 || f.last.(*daemonv1.ProjectServiceUpdateRequest).Name != nil {
		t.Fatalf("an unset field was sent: %d %v", code, f.last)
	}

	if code, _, _ := projectArgs("delete", "--force", "orchard"); code != 0 || !f.last.(*daemonv1.ProjectServiceDeleteRequest).GetForce() {
		t.Fatalf("delete %d %v", code, f.last)
	}
	if code, _, _ := projectArgs("bind", "orchard"); code != 0 || f.last.(*daemonv1.ProjectServiceBindRequest).GetFolder() != here {
		t.Fatalf("bind %d %v", code, f.last)
	}
	if code, _, _ := projectArgs("bind", "orchard", "--folder", "/w"); code != 0 || f.last.(*daemonv1.ProjectServiceBindRequest).GetFolder() != "/w" {
		t.Fatalf("bind folder %d %v", code, f.last)
	}
	if code, _, _ := projectArgs("unbind"); code != 0 || f.last.(*daemonv1.ProjectServiceUnbindRequest).GetFolder() != here {
		t.Fatalf("unbind %d %v", code, f.last)
	}
	if code, _, _ := projectArgs("unbind", "--folder", "/w"); code != 0 || f.last.(*daemonv1.ProjectServiceUnbindRequest).GetFolder() != "/w" {
		t.Fatalf("unbind folder %d %v", code, f.last)
	}
}

func TestRepoSpecRelativePathBecomesAbsolute(t *testing.T) {
	f := &fakeRecords{}
	withRecords(t, f)
	here, err := workingFolder()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, errs := projectArgs("create", "orchard", "--repo", "sub"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if got := f.last.(*daemonv1.ProjectServiceCreateRequest).GetRepos()[0].GetPath(); got != filepath.Join(here, "sub") {
		t.Fatalf("%s", got)
	}
}

func TestTaskVerbsSendTheirRequests(t *testing.T) {
	f := &fakeRecords{}
	withRecords(t, f)
	here, err := workingFolder()
	if err != nil {
		t.Fatal(err)
	}

	if code, out, errs := taskArgs("create", "Fix lights", "--project", "orchard", "--slug", "s", "--body", "b", "--label", "x", "--repo", "api"); code != 0 || !strings.Contains(out, "lights") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	c := f.last.(*daemonv1.TaskServiceCreateRequest)
	if c.GetTitle() != "Fix lights" || c.GetScope().GetProject() != "orchard" || c.GetScope().GetFolder() != here || c.GetSlug() != "s" || c.GetBody() != "b" || c.GetLabels()[0] != "x" || c.GetRepos()[0] != "api" {
		t.Fatalf("%v", c)
	}

	if code, out, _ := taskArgs("show", "lights"); code != 0 || f.last.(*daemonv1.TaskServiceGetRequest).GetTask() != "lights" || !strings.Contains(out, "pr:7") {
		t.Fatalf("show %d %q", code, out)
	}
	if code, out, _ := taskArgs("list", "--all", "--status", "todo", "--repo", "api", "--label", "x"); code != 0 || !strings.Contains(out, "lights") {
		t.Fatalf("list %d %q", code, out)
	}
	l := f.last.(*daemonv1.TaskServiceListRequest)
	if !l.GetAll() || l.GetStatus() != "todo" || l.GetRepo() != "api" || l.GetLabel() != "x" {
		t.Fatalf("%v", l)
	}

	if code, _, errs := taskArgs("update", "lights", "--title", "T", "--body", "", "--status", "doing", "--add-repo", "a", "--remove-repo", "b", "--add-label", "c", "--remove-label", "d"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	u := f.last.(*daemonv1.TaskServiceUpdateRequest)
	if u.GetTask() != "lights" || u.GetTitle() != "T" || u.Body == nil || u.GetStatus() != "doing" || u.GetAddRepos()[0] != "a" || u.GetRemoveRepos()[0] != "b" || u.GetAddLabels()[0] != "c" || u.GetRemoveLabels()[0] != "d" {
		t.Fatalf("%v", u)
	}
	if code, _, _ := taskArgs("update", "lights"); code != 0 || f.last.(*daemonv1.TaskServiceUpdateRequest).Title != nil {
		t.Fatalf("an unset field was sent: %d %v", code, f.last)
	}
	if code, _, _ := taskArgs("delete", "lights", "--force"); code != 0 || !f.last.(*daemonv1.TaskServiceDeleteRequest).GetForce() {
		t.Fatalf("delete %d %v", code, f.last)
	}
	if code, _, _ := taskArgs("link", "lights", "pr:7", "--repo", "api"); code != 0 {
		t.Fatalf("link %d", code)
	}
	k := f.last.(*daemonv1.TaskServiceLinkRequest)
	if k.GetTask() != "lights" || k.GetLink() != "pr:7" || k.GetRepo() != "api" {
		t.Fatalf("%v", k)
	}
}

func TestRecordVerbsPrintJSON(t *testing.T) {
	withRecords(t, &fakeRecords{})
	for _, c := range []struct {
		run  func(...string) (int, string, string)
		args []string
		key  string
	}{
		{projectArgs, []string{"create", "orchard", "--json"}, "project"},
		{projectArgs, []string{"show", "--json"}, "project"},
		{projectArgs, []string{"list", "--json"}, "projects"},
		{projectArgs, []string{"update", "--json"}, "project"},
		{projectArgs, []string{"bind", "--json"}, "project"},
		{projectArgs, []string{"unbind", "--json"}, "project"},
		{taskArgs, []string{"create", "t", "--json"}, "task"},
		{taskArgs, []string{"show", "t", "--json"}, "task"},
		{taskArgs, []string{"list", "--json"}, "tasks"},
		{taskArgs, []string{"update", "t", "--json"}, "task"},
		{taskArgs, []string{"link", "t", "pr:1", "--json"}, "task"},
	} {
		code, out, _ := c.run(c.args...)
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 || got[c.key] == nil {
			t.Fatalf("%v: %d %v %q", c.args, code, err, out)
		}
	}
}

func TestRecordVerbUsageErrors(t *testing.T) {
	withRecords(t, &fakeRecords{})
	for _, args := range [][]string{
		{}, {"nope"}, {"create"}, {"create", "a", "b"}, {"create", "--bad"}, {"create", "a", "--repo", "x=:"},
		{"show", "a", "b"}, {"show", "--bad"}, {"list", "a"}, {"list", "--bad"},
		{"update", "a", "b"}, {"update", "--bad"}, {"update", "a", "--add-repo", "="},
		{"delete", "a", "b"}, {"delete", "--bad"}, {"bind", "a", "b"}, {"bind", "--bad"}, {"unbind", "a"}, {"unbind", "--bad"},
	} {
		if code, _, _ := projectArgs(args...); code != 2 {
			t.Errorf("project %v: code %d", args, code)
		}
	}
	for _, args := range [][]string{
		{}, {"nope"}, {"create", "a", "b"}, {"create", "--bad"},
		{"show"}, {"show", "a", "b"}, {"show", "--bad"}, {"list", "a"}, {"list", "--bad"},
		{"update"}, {"update", "a", "b"}, {"update", "--bad"},
		{"delete"}, {"delete", "--bad"}, {"link"}, {"link", "a"}, {"link", "a", "b", "c"}, {"link", "--bad"},
	} {
		if code, _, _ := taskArgs(args...); code != 2 {
			t.Errorf("task %v: code %d", args, code)
		}
	}
}

func everyRecordCall() [][2]any {
	var out [][2]any
	for _, a := range [][]string{{"create", "a"}, {"show"}, {"list"}, {"update"}, {"delete"}, {"bind"}, {"unbind"}} {
		out = append(out, [2]any{"project", a})
	}
	for _, a := range [][]string{{"create", "a"}, {"show", "a"}, {"list"}, {"update", "a"}, {"delete", "a"}, {"link", "a", "pr:1"}} {
		out = append(out, [2]any{"task", a})
	}
	return out
}

func callRecord(c [2]any) (int, string, string) {
	if c[0] == "project" {
		return projectArgs(c[1].([]string)...)
	}
	return taskArgs(c[1].([]string)...)
}

func TestRecordVerbsReportFailures(t *testing.T) {
	withRecords(t, nil)
	for _, c := range everyRecordCall() {
		if code, _, errs := callRecord(c); code != 1 || !strings.Contains(errs, "not running") {
			t.Errorf("%v: %d %s", c, code, errs)
		}
	}
	withRecords(t, &fakeRecords{err: errors.New("refused")})
	for _, c := range everyRecordCall() {
		if code, _, errs := callRecord(c); code != 1 || !strings.Contains(errs, "refused") {
			t.Errorf("%v: %d %s", c, code, errs)
		}
	}
}

func TestRecordVerbsReportAWorkingFolderThatIsGone(t *testing.T) {
	withRecords(t, &fakeRecords{})
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	for _, c := range everyRecordCall() {
		if verb := c[1].([]string)[0]; c[0] == "project" && (verb == "list" || verb == "create") {
			continue // needs no folder
		}
		if code, _, _ := callRecord(c); code != 1 {
			t.Errorf("%v: code %d", c, code)
		}
	}
	if code, _, _ := projectArgs("create", "a", "--bind"); code != 1 {
		t.Errorf("bind with no folder: code %d", code)
	}
	if code, _, _ := projectArgs("create", "a", "--repo", "rel"); code != 1 {
		t.Errorf("relative repository: code %d", code)
	}
	if code, _, _ := projectArgs("update", "--add-repo", "rel"); code != 1 {
		t.Errorf("relative added repository: code %d", code)
	}
}

func TestTaskShowPrintsAURLLinkWhole(t *testing.T) {
	saved := oneTask.Links
	t.Cleanup(func() { oneTask.Links = saved })
	oneTask.Links = []*corev1.TaskLink{{Repo: "api", Kind: "url", Target: "https://x.test/1"}}
	withRecords(t, &fakeRecords{})
	if code, out, _ := taskArgs("show", "lights"); code != 0 || !strings.Contains(out, "api https://x.test/1") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestRecordsDialOpensTheDaemonClient(t *testing.T) {
	t.Setenv("HERDSMAN_PRIVATE_HOME", t.TempDir())
	projects, tasks, closeAll, err := recordsDial()
	if err != nil || projects == nil || tasks == nil {
		t.Fatalf("%v %v %v", projects, tasks, err)
	}
	closeAll()
	t.Setenv("HERDSMAN_PRIVATE_HOME", "")
	t.Setenv("HOME", "")
	if _, _, _, err := recordsDial(); err == nil {
		t.Fatal("no error without a home folder")
	}
}
