package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// runtimeKind maps a browser action to the runtime action it is on a backend.
// On herdr-core every action that types becomes a person's typed submit, and
// focus becomes show; nothing else exists there.
func runtimeKind(backend, typ string) string {
	if backend != runtime.BackendCore {
		return typ
	}
	switch typ {
	case "prompt", "text", runtime.ActionSubmit:
		return runtime.ActionSubmit
	case "focus", runtime.ActionShow:
		return runtime.ActionShow
	}
	return typ
}

// runtimeAction carries out an action keyed by a runtime reference, through
// the daemon's RuntimeService: the execution's backend decides, by its
// explicit action list, what the action may be. A Herdr TUI execution goes
// on to its pane as before; a herdr-core execution is shown through the
// provider or given one person's typed submit, with its confirmation, and
// anything else is refused by name. Nothing is ever forwarded generically.
func (q *Queue) runtimeAction(ctx context.Context, a Action) Result {
	if q.runtime == nil {
		return Result{Code: "unavailable", Error: "this server reaches no daemon, so it cannot act on an execution"}
	}
	got, err := q.runtime.GetExecution(ctx, &daemonv1.RuntimeServiceGetExecutionRequest{ExecutionId: a.Execution})
	if err != nil {
		return runtimeResult(err, "")
	}
	e := got.GetExecution()
	backend := e.GetRef().GetBackend()
	kind := runtimeKind(backend, a.Type)
	if !runtime.Allows(backend, kind) {
		return Result{Code: "not_on_backend", Error: fmt.Sprintf("%s takes no %q action; it offers %s", backend, a.Type, strings.Join(runtime.Actions(backend), ", "))}
	}
	who := e.GetAgent() + " (" + a.Execution + ")"
	if backend == runtime.BackendTUI {
		pane := e.GetRef().GetPane()
		if pane == "" {
			return Result{Code: "not_on_backend", Error: "the execution has no pane to act on"}
		}
		a.Pane, a.Execution = pane, ""
		return q.run(ctx, a)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var res Result
	switch kind {
	case runtime.ActionShow:
		_, err := q.runtime.Show(ctx, &daemonv1.RuntimeServiceShowRequest{ExecutionId: a.Execution})
		res = runtimeResult(err, "showed "+who+" in the herdr app")
	case runtime.ActionSubmit:
		line := strings.TrimRight(a.Text, "\r\n")
		if strings.TrimSpace(line) == "" || strings.ContainsAny(line, "\r\n") {
			return Result{Code: "invalid_action", Error: "a typed submit is one line of text"}
		}
		if !a.Confirmed {
			// The browser shows this and sends the action again with
			// confirmed set: one person's explicit yes for one line.
			return Result{Code: "needs_confirmation", Error: core.SubmitConfirmation}
		}
		sub, err := q.runtime.Submit(ctx, &daemonv1.RuntimeServiceSubmitRequest{ExecutionId: a.Execution, Text: line, Pid: int64(os.Getpid()), Confirmed: true, Again: a.Force})
		res = runtimeResult(err, fmt.Sprintf("typed submit to %s: %s", who, sub.GetState()))
		if err == nil && sub.GetState() == core.SubmitUnresolved {
			res.OK, res.Code, res.Error = false, "unresolved", "nobody can tell whether the line was typed: keep waiting, stop the execution, or send again knowing both may be typed"
		}
	}
	// The line itself is never logged: a submit's record keeps its digest only.
	q.hub.Log(Entry{At: q.now(), Source: "web", Kind: kind, Text: q.redactor.String(outcome(res.Summary, resultErr(res)))})
	if dir := routeDir(q.hub.Current(), e.GetRoute().GetValue()); dir != "" {
		_ = route.AppendLog(dir, "web: "+q.redactor.String(outcome(res.Summary, resultErr(res))), q.now())
	}
	q.hub.Refresh()
	return res
}

// runtimeResult is a RuntimeService answer as the browser gets it: the
// daemon's refusal code, or the summary.
func runtimeResult(err error, summary string) Result {
	if err == nil {
		return Result{OK: true, Summary: summary}
	}
	res := Result{Error: err.Error(), Summary: summary}
	var e *rpc.Error
	if errors.As(err, &e) {
		res.Code = string(e.Code)
	}
	return res
}

func resultErr(r Result) error {
	if r.OK {
		return nil
	}
	return errors.New(r.Error)
}

// routeDir is the folder of the live route with this name, or "".
func routeDir(st State, name string) string {
	if name == "" {
		return ""
	}
	for _, r := range st.Routes {
		if r.Live && r.Route != nil && (r.Name == name || strings.HasPrefix(filepath.Base(r.Dir), name+"-")) {
			return r.Dir
		}
	}
	return ""
}
