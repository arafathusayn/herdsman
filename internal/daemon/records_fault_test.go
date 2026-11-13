package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/project"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

var errRecordsFault = errors.New("injected fault")

type failingNth struct {
	store.Conn
	at    int
	count *int
}

func (f failingNth) hit() bool { *f.count++; return *f.count == f.at }

func (f failingNth) Exec(q string, args ...any) (int64, error) {
	if f.hit() {
		return 0, errRecordsFault
	}
	return f.Conn.Exec(q, args...)
}

func (f failingNth) Query(q string, args []any, fn func(store.Row) error) error {
	if f.hit() {
		return errRecordsFault
	}
	return f.Conn.Query(q, args, fn)
}

func (f failingNth) Tx(fn func(store.Conn) error) error {
	return f.Conn.Tx(func(c store.Conn) error { return fn(failingNth{c, f.at, f.count}) })
}

// A failing statement at any point of any handler is an internal error, and a
// handler that met none succeeds.
func TestRecordsHandlersReportEveryStatementFailure(t *testing.T) {
	ctx := context.Background()
	named := &daemonv1.Scope{Project: "orchard"}
	calls := map[string]func(projectRPC, taskRPC, string) error{
		"create": func(p projectRPC, _ taskRPC, dir string) error {
			_, err := p.Create(ctx, &daemonv1.ProjectServiceCreateRequest{Name: "new", Folder: dir})
			return err
		},
		"get": func(p projectRPC, _ taskRPC, _ string) error {
			_, err := p.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: named})
			return err
		},
		"list": func(p projectRPC, _ taskRPC, _ string) error {
			_, err := p.List(ctx, &daemonv1.ProjectServiceListRequest{})
			return err
		},
		"update": func(p projectRPC, _ taskRPC, _ string) error {
			_, err := p.Update(ctx, &daemonv1.ProjectServiceUpdateRequest{Scope: named, Description: ptrTo("d")})
			return err
		},
		"bind": func(p projectRPC, _ taskRPC, dir string) error {
			_, err := p.Bind(ctx, &daemonv1.ProjectServiceBindRequest{Scope: named, Folder: dir})
			return err
		},
		"unbind": func(p projectRPC, _ taskRPC, _ string) error {
			_, err := p.Unbind(ctx, &daemonv1.ProjectServiceUnbindRequest{Folder: "/bound"})
			if codeOf(err) == rpc.NotFound {
				return nil
			}
			return err
		},
		"delete": func(p projectRPC, _ taskRPC, _ string) error {
			_, err := p.Delete(ctx, &daemonv1.ProjectServiceDeleteRequest{Scope: named, Force: true})
			return err
		},
		"task create": func(_ projectRPC, k taskRPC, _ string) error {
			_, err := k.Create(ctx, &daemonv1.TaskServiceCreateRequest{Scope: named, Title: "New"})
			return err
		},
		"task get": func(_ projectRPC, k taskRPC, _ string) error {
			_, err := k.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: named, Task: "one"})
			return err
		},
		"task list": func(_ projectRPC, k taskRPC, _ string) error {
			_, err := k.List(ctx, &daemonv1.TaskServiceListRequest{Scope: named})
			return err
		},
		"task update": func(_ projectRPC, k taskRPC, _ string) error {
			_, err := k.Update(ctx, &daemonv1.TaskServiceUpdateRequest{Scope: named, Task: "one", Title: ptrTo("T")})
			return err
		},
		"task delete": func(_ projectRPC, k taskRPC, _ string) error {
			_, err := k.Delete(ctx, &daemonv1.TaskServiceDeleteRequest{Scope: named, Task: "one"})
			return err
		},
		"task link": func(_ projectRPC, k taskRPC, _ string) error {
			_, err := k.Link(ctx, &daemonv1.TaskServiceLinkRequest{Scope: named, Task: "one", Link: "pr:1"})
			return err
		},
		"route scope": func(p projectRPC, _ taskRPC, _ string) error {
			_, err := p.Get(ctx, &daemonv1.ProjectServiceGetRequest{Scope: &daemonv1.Scope{Project: "orchard", Route: routeID("live")}})
			return err
		},
		"task route scope": func(_ projectRPC, k taskRPC, _ string) error {
			_, err := k.Get(ctx, &daemonv1.TaskServiceGetRequest{Scope: &daemonv1.Scope{Project: "orchard", Route: routeID("live")}, Task: "one"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			for at := 1; ; at++ {
				if at > 200 {
					t.Fatal("never ran out of statements")
				}
				dir, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				db, err := store.Open(filepath.Join(shortDir(t), "a.db"), store.Writer, store.KindAccount)
				if err != nil {
					t.Fatal(err)
				}
				engine := ops.NewEngine(db, ops.Options{Effects: project.Effects(db)})
				seed := project.New(db, engine)
				p, err := seed.CreateProject(project.ProjectInput{Name: "orchard", Repos: []project.Repo{{Name: "api", Path: "/src/api"}}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := seed.CreateTask(project.TaskInput{ProjectID: p.ID, Title: "One"}); err != nil {
					t.Fatal(err)
				}
				count := 0
				records := &recordsRPC{d: &Daemon{acctDB: failingNth{db, at, &count}, ops: engine}}
				err = call(projectRPC{records}, taskRPC{records}, dir)
				db.Close()
				if count < at {
					if err != nil {
						t.Fatalf("no fault reached at %d: %v", at, err)
					}
					return
				}
				if codeOf(err) != rpc.Internal {
					t.Fatalf("statement %d: %v", at, err)
				}
			}
		})
	}
}
