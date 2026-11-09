package herdrtest

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// World is a fake herdr layout on top of a Fixture: workspaces, tabs and panes
// that exist only as far as a test created them. A marked launch step submitted
// to a pane runs in a real shell when Run is left on, so that marker files
// appear as they do under herdr; any other text is only recorded.
type World struct {
	f *Fixture

	mu         sync.Mutex
	seq        int
	workspaces []map[string]any
	tabs       []map[string]any
	panes      map[string]*worldPane
	order      []string
	starts     map[string]int
	inputs     map[string][]string
	process    map[string]map[string]any
	run        bool
	onInput    func(pane, text string) bool
}

type worldPane struct {
	workspace, tab, label, cwd string
	env                        map[string]string
}

// ServeWorld installs the layout methods on a Fixture.
func ServeWorld(f *Fixture) *World {
	w := &World{f: f, panes: map[string]*worldPane{}, starts: map[string]int{}, inputs: map[string][]string{}, process: map[string]map[string]any{}, run: true}
	s := f.srv
	s.Handle("session.snapshot", w.snapshot)
	s.Handle("workspace.create", w.workspaceCreate)
	s.Handle("tab.create", w.tabCreate)
	s.Handle("pane.split", w.paneSplit)
	s.Handle("pane.rename", w.paneRename)
	s.Handle("pane.send_input", w.paneSendInput)
	s.Handle("pane.process_info", w.paneProcessInfo)
	s.Handle("pane.close", w.paneClose)
	s.Handle("workspace.close", w.workspaceClose)
	s.Handle("agent.start", w.agentStart)
	return w
}

// SetRun turns the running of submitted text on or off.
func (w *World) SetRun(on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.run = on
}

// OnInput sets a hook that sees submitted text first; it returns false to
// keep the text from running.
func (w *World) OnInput(fn func(pane, text string) bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onInput = fn
}

// Starts returns how many times agent.start succeeded for a name.
func (w *World) Starts(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.starts[name]
}

// Inputs returns the text submitted to a pane.
func (w *World) Inputs(pane string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.inputs[pane]...)
}

// Env returns the environment a pane was created with.
func (w *World) Env(pane string) map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := w.panes[pane]; p != nil {
		return maps.Clone(p.env)
	}
	return nil
}

// PaneByLabel returns the id of the pane with a label, or "".
func (w *World) PaneByLabel(label string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range w.order {
		if p := w.panes[id]; p != nil && p.label == label {
			return id
		}
	}
	return ""
}

// AddPane adds a pane outside any route, its process info answering with tty.
func (w *World) AddPane(label, tty string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.newPane("outside", "outside", label, "", nil)
	w.process[id] = map[string]any{"pane_id": id, "tty": tty, "foreground_processes": []map[string]any{}}
	return id
}

// Destroy removes a pane and its agent, as closing it in herdr would.
func (w *World) Destroy(pane string) {
	w.mu.Lock()
	delete(w.panes, pane)
	w.mu.Unlock()
	w.f.SetAgent(pane, nil)
}

// SetProcess sets what pane.process_info answers for a pane.
func (w *World) SetProcess(pane string, shellPID int, foreground ...int) {
	procs := make([]map[string]any, 0, len(foreground))
	for _, pid := range foreground {
		procs = append(procs, map[string]any{"pid": pid, "name": "herdsman"})
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.process[pane] = map[string]any{"pane_id": pane, "shell_pid": shellPID, "tty": "/dev/ttys-fake", "foreground_processes": procs}
}

func str(p map[string]any, key string) string {
	v, _ := p[key].(string)
	return v
}

func envOf(p map[string]any) map[string]string {
	out := map[string]string{}
	if m, ok := p["env"].(map[string]any); ok {
		for k, v := range m {
			out[k], _ = v.(string)
		}
	}
	return out
}

func (w *World) next(prefix string) string {
	w.seq++
	return fmt.Sprintf("%s%d", prefix, w.seq)
}

// newPane adds a pane; the caller holds w.mu.
func (w *World) newPane(workspace, tab, label, cwd string, env map[string]string) string {
	id := w.next("p")
	w.panes[id] = &worldPane{workspace: workspace, tab: tab, label: label, cwd: cwd, env: env}
	w.order = append(w.order, id)
	return id
}

func (w *World) workspaceCreate(p map[string]any) (any, string) {
	if c := w.f.failed("workspace.create"); c != "" {
		return nil, c
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	ws, tab := w.next("w"), w.next("t")
	label := str(p, "label")
	w.workspaces = append(w.workspaces, map[string]any{"workspace_id": ws, "label": label})
	w.tabs = append(w.tabs, map[string]any{"tab_id": tab, "workspace_id": ws, "label": ""})
	pane := w.newPane(ws, tab, "", str(p, "cwd"), envOf(p))
	return map[string]any{"type": "workspace_created", "workspace": map[string]any{"workspace_id": ws, "label": label},
		"tab": map[string]any{"tab_id": tab}, "root_pane": map[string]any{"pane_id": pane}}, ""
}

func (w *World) tabCreate(p map[string]any) (any, string) {
	if c := w.f.failed("tab.create"); c != "" {
		return nil, c
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	tab := w.next("t")
	ws := str(p, "workspace_id")
	w.tabs = append(w.tabs, map[string]any{"tab_id": tab, "workspace_id": ws, "label": str(p, "label")})
	pane := w.newPane(ws, tab, "", str(p, "cwd"), envOf(p))
	return map[string]any{"type": "tab_created", "tab": map[string]any{"tab_id": tab}, "root_pane": map[string]any{"pane_id": pane}}, ""
}

func (w *World) paneSplit(p map[string]any) (any, string) {
	if c := w.f.failed("pane.split"); c != "" {
		return nil, c
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	target := w.panes[str(p, "target_pane_id")]
	if target == nil {
		return nil, "pane_not_found"
	}
	pane := w.newPane(target.workspace, target.tab, "", str(p, "cwd"), envOf(p))
	return map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": pane}}, ""
}

func (w *World) paneRename(p map[string]any) (any, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	pane := w.panes[str(p, "pane_id")]
	if pane == nil {
		return nil, "pane_not_found"
	}
	pane.label = str(p, "label")
	return map[string]any{"type": "ok"}, ""
}

func (w *World) paneSendInput(p map[string]any) (any, string) {
	if c := w.f.failed("pane.send_input"); c != "" {
		return nil, c
	}
	id, text := str(p, "pane_id"), str(p, "text")
	w.mu.Lock()
	pane := w.panes[id]
	if pane == nil {
		w.mu.Unlock()
		return nil, "pane_not_found"
	}
	w.inputs[id] = append(w.inputs[id], text)
	hook, run, cwd := w.onInput, w.run, pane.cwd
	env := maps.Clone(pane.env)
	w.mu.Unlock()
	if hook != nil && !hook(id, text) {
		return map[string]any{"type": "ok"}, ""
	}
	// Only marked launch steps run. Everything else typed into a pane starts a
	// long-lived service, and under a test the service's executable is the test
	// binary itself, which would start the tests again.
	if run && strings.Contains(text, "herdsman-step") {
		cmd := exec.Command("/bin/sh", "-c", text)
		cmd.Dir = cwd
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		_ = cmd.Run() // a failing step records its status in its own marker
	}
	return map[string]any{"type": "ok"}, ""
}

func (w *World) paneProcessInfo(p map[string]any) (any, string) {
	if c := w.f.failed("pane.process_info"); c != "" {
		return nil, c
	}
	id := str(p, "pane_id")
	w.mu.Lock()
	defer w.mu.Unlock()
	info := w.process[id]
	if info == nil {
		return nil, "pane_not_found"
	}
	return map[string]any{"type": "pane_process_info", "process_info": info}, ""
}

func (w *World) agentStart(p map[string]any) (any, string) {
	if c := w.f.failed("agent.start"); c != "" {
		return nil, c
	}
	name, kind, id := str(p, "name"), str(p, "kind"), str(p, "pane_id")
	w.mu.Lock()
	pane := w.panes[id]
	if pane == nil {
		w.mu.Unlock()
		return nil, "pane_not_found"
	}
	workspace, tab := pane.workspace, pane.tab
	w.mu.Unlock()
	if w.f.named(name) {
		return nil, "agent_name_taken"
	}
	w.mu.Lock()
	w.starts[name]++
	w.mu.Unlock()
	rec := Agent(id, "session-"+name)
	rec["name"], rec["agent"], rec["workspace_id"], rec["tab_id"] = name, kind, workspace, tab
	rec["agent_session"] = map[string]any{"source": "hook", "agent": kind, "kind": "id", "value": "session-" + name}
	w.f.SetAgent(id, rec)
	return map[string]any{"type": "agent_info", "agent": rec}, ""
}

func (w *World) snapshot(map[string]any) (any, string) {
	if c := w.f.failed("session.snapshot"); c != "" {
		return nil, c
	}
	w.mu.Lock()
	panes := make([]map[string]any, 0, len(w.order))
	for _, id := range w.order {
		if p := w.panes[id]; p != nil {
			panes = append(panes, map[string]any{"pane_id": id, "workspace_id": p.workspace, "tab_id": p.tab, "label": p.label})
		}
	}
	snapshot := map[string]any{"version": "0.9.5", "protocol": 22, "workspaces": append([]map[string]any{}, w.workspaces...),
		"tabs": append([]map[string]any{}, w.tabs...), "panes": panes, "layouts": []any{}}
	w.mu.Unlock()
	w.f.mu.Lock()
	snapshot["agents"] = w.f.sorted()
	w.f.mu.Unlock()
	return map[string]any{"type": "session_snapshot", "snapshot": snapshot}, ""
}

func (w *World) paneClose(p map[string]any) (any, string) {
	if c := w.f.failed("pane.close"); c != "" {
		return nil, c
	}
	id := str(p, "pane_id")
	w.mu.Lock()
	_, ok := w.panes[id]
	w.mu.Unlock()
	if !ok {
		return nil, "pane_not_found"
	}
	w.Destroy(id)
	w.f.SetAgent(id, nil)
	return map[string]any{"type": "ok"}, ""
}

func (w *World) workspaceClose(p map[string]any) (any, string) {
	if c := w.f.failed("workspace.close"); c != "" {
		return nil, c
	}
	id := str(p, "workspace_id")
	w.mu.Lock()
	w.workspaces = slices.DeleteFunc(w.workspaces, func(m map[string]any) bool { return m["workspace_id"] == id })
	var closing []string
	for pane, data := range w.panes {
		if data.workspace == id {
			closing = append(closing, pane)
		}
	}
	w.mu.Unlock()
	for _, pane := range closing {
		w.Destroy(pane)
		w.f.SetAgent(pane, nil)
	}
	return map[string]any{"type": "ok"}, ""
}

// named reports whether a live agent already has the name.
func (f *Fixture) named(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.agents {
		if a["name"] == name {
			return true
		}
	}
	return false
}
