package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const projectHelp = `usage: herdsman project create <name> [--description <text>] [--repo <spec>]... [--folder <path>] [--bind] [--json]
       herdsman project show [<project>] [--json]
       herdsman project list [--archived] [--json]
       herdsman project update [<project>] [--name <name>] [--description <text>]
                               [--add-repo <spec>]... [--remove-repo <name>]... [--json]
       herdsman project delete [<project>] [--force]
       herdsman project bind [<project>] [--folder <path>] [--json]
       herdsman project unbind [--folder <path>] [--json]

Keeps the projects, the folders bound to them and their repositories. A <spec>
is [<name>=]<path>[:<branch>[:<remote>]]; the name defaults to the folder's.
create binds --folder, or this folder with --bind.
<project> is a name, an id or @; left out, it is the project bound to this
folder. delete refuses a project with open tasks or tasks on a running route;
--force archives it instead and keeps its records.

exit codes:
  0  done
  1  the record was not found, the change was refused, or the call failed
  2  bad usage
`

const taskHelp = `usage: herdsman task create <title> [--project <project>] [--id <slug>] [--slug <slug>]
                            [--body <text>] [--body-file <path>]
                            [--label <label>]... [--repo <name>]... [--json]
       herdsman task show <task> [--project <project>] [--json]
       herdsman task list [--project <project>] [--all] [--status <status>]
                          [--repo <name>] [--label <label>] [--json]
       herdsman task update <task> [--project <project>] [--title <text>] [--body <text>]
                            [--status <status>] [--add-repo <name>]... [--remove-repo <name>]...
                            [--label <label>]... [--unlabel <label>]...
                            [--add-label <label>]... [--remove-label <label>]... [--json]
       herdsman task delete <task> [--project <project>] [--force]
       herdsman task link <task> <link> [--project <project>] [--repo <name>] [--json]

Keeps the tasks of each project. <task> is a slug, an id, or @ for the task of
the current worktree. A <status> is todo, doing, review, done or archived. A
<link> is pr:<n>, issue:<n> or a URL; --repo may be left out when the task
names one repository. A task's project cannot change. delete refuses a task
that still has a worktree or runs in a route, and names down. --slug is --id,
--add-label is --label and --remove-label is --unlabel. With no arguments on a
terminal, task create opens a form.

exit codes:
  0  done
  1  the record was not found, the change was refused, or the call failed
  2  bad usage
`

const recordsCallLimit = time.Minute

// recordsDial opens the daemon's record services.
var recordsDial = func() (daemonv1.ProjectService, daemonv1.TaskService, func(), error) {
	sdk, err := client.Dial(client.Options{})
	if err != nil {
		return nil, nil, nil, err
	}
	return sdk.Project, sdk.Task, sdk.Close, nil
}

func init() {
	register(command{name: "project", summary: "Keep projects, their folders and their repositories.", help: projectHelp, run: runProject})
	register(command{name: "task", summary: "Keep the tasks of each project.", help: taskHelp, run: runTask})
}

// recordCall is one parsed verb, ready to send.
type recordCall func(context.Context, daemonv1.ProjectService, daemonv1.TaskService) (proto.Message, error)

// sendRecord runs a call and prints its response.
func sendRecord(stdout, stderr io.Writer, asJSON bool, call recordCall, show func(io.Writer, proto.Message)) int {
	projects, tasks, closeAll, err := recordsDial()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeAll()
	ctx, cancel := context.WithTimeout(context.Background(), recordsCallLimit)
	defer cancel()
	res, err := call(ctx, projects, tasks)
	if err != nil {
		return fail(stderr, err)
	}
	if asJSON {
		text, err := (protojson.MarshalOptions{Multiline: true, UseProtoNames: true}).Marshal(res)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, string(text))
		return 0
	}
	if show != nil {
		show(stdout, res)
	}
	return 0
}

// recordVerb reads a verb's flags and refuses the wrong number of positionals.
func recordVerb(fs *flag.FlagSet, stderr io.Writer, args []string, least, most int) ([]string, bool) {
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return nil, false
	}
	if len(positional) < least || len(positional) > most {
		usageError(stderr, fs.Name()+": wrong number of arguments")
		return nil, false
	}
	return positional, true
}

func firstOr(list []string, fallback string) string {
	if len(list) > 0 {
		return list[0]
	}
	return fallback
}

// setFlag returns the value only when the flag was given, so that an empty
// value can clear a field and an absent flag leaves it alone.
func setFlag(fs *flag.FlagSet, name string, value *string) *string {
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	if !given {
		return nil
	}
	return value
}

// fromHere makes a path absolute against this folder, because the daemon has
// a different working folder.
func fromHere(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	here, err := workingFolder()
	if err != nil {
		return "", err
	}
	return filepath.Join(here, path), nil
}

var errBadRepoSpec = errors.New("a repository is [<name>=]<path>[:<branch>[:<remote>]]")

// parseRepoSpec reads one repository.
func parseRepoSpec(spec string) (*corev1.ProjectRepo, error) {
	name, rest, named := strings.Cut(spec, "=")
	if !named {
		name, rest = "", spec
	}
	fields := strings.SplitN(rest, ":", 3)
	if (named && name == "") || fields[0] == "" {
		return nil, errBadRepoSpec
	}
	path, err := fromHere(fields[0])
	if err != nil {
		return nil, err
	}
	out := &corev1.ProjectRepo{Name: name, Path: path}
	if len(fields) > 1 {
		out.DefaultBranch = fields[1]
	}
	if len(fields) > 2 {
		out.Remote = fields[2]
	}
	return out, nil
}

// parseRepoSpecs separates a bad spec, which is usage, from a failed lookup.
func parseRepoSpecs(specs []string, stderr io.Writer) ([]*corev1.ProjectRepo, int) {
	out := make([]*corev1.ProjectRepo, 0, len(specs))
	for _, spec := range specs {
		repo, err := parseRepoSpec(spec)
		if errors.Is(err, errBadRepoSpec) {
			return nil, usageError(stderr, err.Error())
		}
		if err != nil {
			return nil, fail(stderr, err)
		}
		out = append(out, repo)
	}
	return out, 0
}

func herescope(project string) (*daemonv1.Scope, error) {
	folder, err := workingFolder()
	if err != nil {
		return nil, err
	}
	return &daemonv1.Scope{Project: project, Folder: folder}, nil
}

func showProject(w io.Writer, p *corev1.Project) {
	fmt.Fprintf(w, "%s\t%s\t%s\n", p.GetName(), p.GetState(), p.GetId().GetValue())
	for _, r := range p.GetRepos() {
		fmt.Fprintf(w, "  repo %s\t%s\t%s\n", r.GetName(), r.GetPath(), r.GetDefaultBranch())
	}
	for _, f := range p.GetFolders() {
		fmt.Fprintf(w, "  folder %s\n", f)
	}
}

func showTask(w io.Writer, t *corev1.Task) {
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.GetSlug(), t.GetStatus(), strings.Join(t.GetRepos(), ","), t.GetTitle())
	for _, l := range t.GetLinks() {
		if l.GetKind() == "url" {
			fmt.Fprintf(w, "  %s %s\n", l.GetRepo(), l.GetTarget())
			continue
		}
		fmt.Fprintf(w, "  %s %s:%s\n", l.GetRepo(), l.GetKind(), l.GetTarget())
	}
}

// tabbed aligns what show writes.
func tabbed(w io.Writer, show func(io.Writer)) {
	t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	show(t)
	t.Flush()
}

// printTaskDetail is the show view: the row, its labels, then the description.
func printTaskDetail(w io.Writer, res proto.Message) {
	printTask(w, res)
	task := res.(taskHolder).GetTask()
	if labels := task.GetLabels(); len(labels) > 0 {
		fmt.Fprintf(w, "labels: %s\n", strings.Join(labels, ", "))
	}
	if body := task.GetBody(); body != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimRight(body, "\n"))
	}
}

type projectHolder interface{ GetProject() *corev1.Project }

func printProject(w io.Writer, res proto.Message) {
	tabbed(w, func(t io.Writer) { showProject(t, res.(projectHolder).GetProject()) })
}

type taskHolder interface{ GetTask() *corev1.Task }

func printTask(w io.Writer, res proto.Message) {
	tabbed(w, func(t io.Writer) { showTask(t, res.(taskHolder).GetTask()) })
}

func runProject(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usageError(stderr, "project needs a verb: create, show, list, update, delete, bind or unbind")
	}
	verbs := map[string]func([]string, io.Writer, io.Writer) int{
		"create": runProjectCreate, "show": runProjectShow, "list": runProjectList, "update": runProjectUpdate,
		"delete": runProjectDelete, "bind": runProjectBind, "unbind": runProjectUnbind,
	}
	verb, ok := verbs[args[0]]
	if !ok {
		return usageError(stderr, "unknown project verb "+args[0])
	}
	return verb(args[1:], stdout, stderr)
}

func runProjectCreate(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("project create", stderr)
	description := fs.String("description", "", "what the project is")
	var specs repeatedFlag
	fs.Var(&specs, "repo", "a repository of the project; repeatable")
	folder := fs.String("folder", "", "a folder to bind to the project")
	bind := fs.Bool("bind", false, "bind this folder to the project")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, ok := recordVerb(fs, stderr, args, 1, 1)
	if !ok {
		return 2
	}
	if *bind && *folder != "" {
		return usageError(stderr, "give --folder or --bind, not both")
	}
	repos, code := parseRepoSpecs(specs, stderr)
	if code != 0 {
		return code
	}
	req := &daemonv1.ProjectServiceCreateRequest{Name: positional[0], Description: *description, Repos: repos}
	if *bind || *folder != "" {
		target, err := fromHere(*folder)
		if err != nil {
			return fail(stderr, err)
		}
		req.Folder = target
	}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, p daemonv1.ProjectService, _ daemonv1.TaskService) (proto.Message, error) {
		return p.Create(ctx, req)
	}, printProject)
}

func runProjectShow(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("project show", stderr)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, ok := recordVerb(fs, stderr, args, 0, 1)
	if !ok {
		return 2
	}
	scope, err := herescope(firstOr(positional, ""))
	if err != nil {
		return fail(stderr, err)
	}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, p daemonv1.ProjectService, _ daemonv1.TaskService) (proto.Message, error) {
		return p.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: scope})
	}, printProject)
}

func runProjectList(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("project list", stderr)
	archived := fs.Bool("archived", false, "include archived projects")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if _, ok := recordVerb(fs, stderr, args, 0, 0); !ok {
		return 2
	}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, p daemonv1.ProjectService, _ daemonv1.TaskService) (proto.Message, error) {
		return p.List(ctx, &daemonv1.ProjectServiceListRequest{IncludeArchived: *archived})
	}, func(w io.Writer, res proto.Message) {
		tabbed(w, func(t io.Writer) {
			for _, p := range res.(*daemonv1.ProjectServiceListResponse).GetProjects() {
				showProject(t, p)
			}
		})
	})
}

func runProjectUpdate(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("project update", stderr)
	name := fs.String("name", "", "the new name")
	description := fs.String("description", "", "the new description")
	var add, remove repeatedFlag
	fs.Var(&add, "add-repo", "a repository to add; repeatable")
	fs.Var(&remove, "remove-repo", "a repository to remove; repeatable")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, ok := recordVerb(fs, stderr, args, 0, 1)
	if !ok {
		return 2
	}
	repos, code := parseRepoSpecs(add, stderr)
	if code != 0 {
		return code
	}
	scope, err := herescope(firstOr(positional, ""))
	if err != nil {
		return fail(stderr, err)
	}
	req := &daemonv1.ProjectServiceUpdateRequest{Scope: scope, Name: setFlag(fs, "name", name), Description: setFlag(fs, "description", description), AddRepos: repos, RemoveRepos: remove}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, p daemonv1.ProjectService, _ daemonv1.TaskService) (proto.Message, error) {
		return p.Update(ctx, req)
	}, printProject)
}

func runProjectDelete(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("project delete", stderr)
	force := fs.Bool("force", false, "archive the project and keep its records")
	positional, ok := recordVerb(fs, stderr, args, 0, 1)
	if !ok {
		return 2
	}
	scope, err := herescope(firstOr(positional, ""))
	if err != nil {
		return fail(stderr, err)
	}
	return sendRecord(stdout, stderr, false, func(ctx context.Context, p daemonv1.ProjectService, _ daemonv1.TaskService) (proto.Message, error) {
		return p.Delete(ctx, &daemonv1.ProjectServiceDeleteRequest{Scope: scope, Force: *force})
	}, nil)
}

// folderOr returns the named folder, or this one.
func folderOr(named string) (string, error) {
	if named != "" {
		return named, nil
	}
	return workingFolder()
}

func runProjectBind(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("project bind", stderr)
	folder := fs.String("folder", "", "the folder to bind (default: this one)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, ok := recordVerb(fs, stderr, args, 0, 1)
	if !ok {
		return 2
	}
	scope, err := herescope(firstOr(positional, ""))
	if err != nil {
		return fail(stderr, err)
	}
	target := *folder
	if target == "" {
		target = scope.GetFolder()
	}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, p daemonv1.ProjectService, _ daemonv1.TaskService) (proto.Message, error) {
		return p.Bind(ctx, &daemonv1.ProjectServiceBindRequest{Scope: scope, Folder: target})
	}, printProject)
}

func runProjectUnbind(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("project unbind", stderr)
	folder := fs.String("folder", "", "the folder to release (default: this one)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if _, ok := recordVerb(fs, stderr, args, 0, 0); !ok {
		return 2
	}
	target, err := folderOr(*folder)
	if err != nil {
		return fail(stderr, err)
	}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, p daemonv1.ProjectService, _ daemonv1.TaskService) (proto.Message, error) {
		return p.Unbind(ctx, &daemonv1.ProjectServiceUnbindRequest{Folder: target})
	}, printProject)
}

func runTask(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usageError(stderr, "task needs a verb: create, show, list, update, delete or link")
	}
	if args[0] == "create" {
		rest := args[1:]
		if code, done := openTaskCreateForm(&rest, stdin, stdout, stderr); done {
			return code
		}
		return runTaskCreate(rest, stdout, stderr)
	}
	verbs := map[string]func([]string, io.Writer, io.Writer) int{
		"show": runTaskShow, "list": runTaskList, "update": runTaskUpdate, "delete": runTaskDelete, "link": runTaskLink,
	}
	verb, ok := verbs[args[0]]
	if !ok {
		return usageError(stderr, "unknown task verb "+args[0])
	}
	return verb(args[1:], stdout, stderr)
}

func runTaskCreate(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("task create", stderr)
	project := fs.String("project", "", "the project (default: the bound folder's)")
	id := fs.String("id", "", "the slug (default: made from the title)")
	slug := fs.String("slug", "", "the same as --id")
	body := fs.String("body", "", "the description")
	bodyFile := fs.String("body-file", "", "a file holding the description")
	var labels, repos repeatedFlag
	fs.Var(&labels, "label", "a label; repeatable")
	fs.Var(&repos, "repo", "a repository; repeatable (default: all of the project's)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, ok := recordVerb(fs, stderr, args, 1, 1)
	if !ok {
		return 2
	}
	if *id != "" && *slug != "" {
		return usageError(stderr, "give --id or --slug, not both")
	}
	if *body != "" && *bodyFile != "" {
		return usageError(stderr, "give --body or --body-file, not both")
	}
	if *bodyFile != "" {
		text, err := readBodyFile(*bodyFile)
		if err != nil {
			return fail(stderr, err)
		}
		*body = text
	}
	scope, err := herescope(*project)
	if err != nil {
		return fail(stderr, err)
	}
	req := &daemonv1.TaskServiceCreateRequest{Scope: scope, Title: positional[0], Slug: *id + *slug, Body: *body, Labels: labels, Repos: repos}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, _ daemonv1.ProjectService, t daemonv1.TaskService) (proto.Message, error) {
		return t.Create(ctx, req)
	}, printTask)
}

func runTaskShow(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("task show", stderr)
	project := fs.String("project", "", "the project (default: the bound folder's)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, ok := recordVerb(fs, stderr, args, 1, 1)
	if !ok {
		return 2
	}
	scope, err := herescope(*project)
	if err != nil {
		return fail(stderr, err)
	}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, _ daemonv1.ProjectService, t daemonv1.TaskService) (proto.Message, error) {
		return t.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: scope, Task: positional[0]})
	}, printTaskDetail)
}

func runTaskList(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("task list", stderr)
	project := fs.String("project", "", "the project (default: the bound folder's)")
	all := fs.Bool("all", false, "list the tasks of every project")
	status := fs.String("status", "", "only tasks with this status")
	repo := fs.String("repo", "", "only tasks naming this repository")
	label := fs.String("label", "", "only tasks with this label")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if _, ok := recordVerb(fs, stderr, args, 0, 0); !ok {
		return 2
	}
	scope, err := herescope(*project)
	if err != nil {
		return fail(stderr, err)
	}
	req := &daemonv1.TaskServiceListRequest{Scope: scope, All: *all, Status: *status, Repo: *repo, Label: *label}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, _ daemonv1.ProjectService, t daemonv1.TaskService) (proto.Message, error) {
		return t.List(ctx, req)
	}, func(w io.Writer, res proto.Message) {
		tabbed(w, func(out io.Writer) {
			for _, task := range res.(*daemonv1.TaskServiceListResponse).GetTasks() {
				showTask(out, task)
			}
		})
	})
}

func runTaskUpdate(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("task update", stderr)
	project := fs.String("project", "", "the project (default: the bound folder's)")
	title := fs.String("title", "", "the new title")
	body := fs.String("body", "", "the new description")
	status := fs.String("status", "", "the new status")
	var addRepos, removeRepos, addLabels, removeLabels repeatedFlag
	fs.Var(&addRepos, "add-repo", "a repository to add; repeatable")
	fs.Var(&removeRepos, "remove-repo", "a repository to remove; repeatable")
	fs.Var(&addLabels, "label", "a label to add; repeatable")
	fs.Var(&addLabels, "add-label", "the same as --label")
	fs.Var(&removeLabels, "unlabel", "a label to remove; repeatable")
	fs.Var(&removeLabels, "remove-label", "the same as --unlabel")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, ok := recordVerb(fs, stderr, args, 1, 1)
	if !ok {
		return 2
	}
	scope, err := herescope(*project)
	if err != nil {
		return fail(stderr, err)
	}
	req := &daemonv1.TaskServiceUpdateRequest{Scope: scope, Task: positional[0], Title: setFlag(fs, "title", title), Body: setFlag(fs, "body", body), Status: setFlag(fs, "status", status),
		AddRepos: addRepos, RemoveRepos: removeRepos, AddLabels: addLabels, RemoveLabels: removeLabels}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, _ daemonv1.ProjectService, t daemonv1.TaskService) (proto.Message, error) {
		return t.Update(ctx, req)
	}, printTask)
}

func runTaskDelete(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("task delete", stderr)
	project := fs.String("project", "", "the project (default: the bound folder's)")
	force := fs.Bool("force", false, "delete a task that still runs in a route")
	positional, ok := recordVerb(fs, stderr, args, 1, 1)
	if !ok {
		return 2
	}
	scope, err := herescope(*project)
	if err != nil {
		return fail(stderr, err)
	}
	return sendRecord(stdout, stderr, false, func(ctx context.Context, _ daemonv1.ProjectService, t daemonv1.TaskService) (proto.Message, error) {
		return t.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Scope: scope, Task: positional[0], Force: *force})
	}, nil)
}

func runTaskLink(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("task link", stderr)
	project := fs.String("project", "", "the project (default: the bound folder's)")
	repo := fs.String("repo", "", "the repository the link belongs to")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, ok := recordVerb(fs, stderr, args, 2, 2)
	if !ok {
		return 2
	}
	scope, err := herescope(*project)
	if err != nil {
		return fail(stderr, err)
	}
	req := &daemonv1.TaskServiceLinkRequest{Scope: scope, Task: positional[0], Link: positional[1], Repo: *repo}
	return sendRecord(stdout, stderr, *asJSON, func(ctx context.Context, _ daemonv1.ProjectService, t daemonv1.TaskService) (proto.Message, error) {
		return t.Link(ctx, req)
	}, printTask)
}
