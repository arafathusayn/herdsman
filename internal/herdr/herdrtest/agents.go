package herdrtest

import (
	"maps"
	"sort"
	"sync"
)

// Fixture serves the calls the herdsman commands make, from values a test
// can change between calls: agent records (agent.get by pane id or name,
// agent.list, pane.list), pane text (pane.read), and the answers of
// agent.prompt, tab.focus, pane.report_metadata and notification.show.
// Every call is recorded by the Server, as any other request.
type Fixture struct {
	srv *Server

	mu     sync.Mutex
	agents map[string]map[string]any // by pane id
	screen map[string]string         // pane id to text
	fail   map[string]string         // method to error code
	shown  bool
}

// Serve installs a Fixture's handlers on s.
func Serve(s *Server) *Fixture {
	f := &Fixture{srv: s, agents: map[string]map[string]any{}, screen: map[string]string{}, fail: map[string]string{}, shown: true}
	s.Handle("agent.get", f.agentGet)
	s.Handle("agent.list", f.agentList)
	s.Handle("pane.list", f.paneList)
	s.Handle("pane.read", f.paneRead)
	for _, m := range []string{"agent.prompt", "tab.focus", "pane.report_metadata"} {
		s.Handle(m, f.ok(m))
	}
	s.Handle("notification.show", f.notify)
	return f
}

// Agent returns a complete agent record, as herdr gives one, for a pane
// with a session id.
func Agent(pane, session string) map[string]any {
	return map[string]any{
		"pane_id": pane, "terminal_id": "term-" + pane, "workspace_id": "fixture-workspace", "tab_id": "fixture-tab",
		"focused": false, "revision": 1, "agent": "claude", "interactive_ready": true, "launch_pending": false,
		"agent_status":  "idle",
		"agent_session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": session},
	}
}

// SetAgent sets (or with nil, removes) the agent of a pane.
func (f *Fixture) SetAgent(pane string, a map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a == nil {
		delete(f.agents, pane)
		return
	}
	f.agents[pane] = maps.Clone(a)
}

// Edit changes one field of a pane's agent record.
func (f *Fixture) Edit(pane, key string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a := f.agents[pane]; a != nil {
		a[key] = v
	}
}

// SetScreen sets a pane's text.
func (f *Fixture) SetScreen(pane, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.screen[pane] = text
}

// Fail makes a method answer with an error code ("" clears it).
func (f *Fixture) Fail(method, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if code == "" {
		delete(f.fail, method)
		return
	}
	f.fail[method] = code
}

// Shown sets whether notification.show reports the notification as shown.
func (f *Fixture) Shown(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shown = v
}

func (f *Fixture) failed(method string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail[method]
}

func (f *Fixture) agentGet(p map[string]any) (any, string) {
	if c := f.failed("agent.get"); c != "" {
		return nil, c
	}
	target, _ := p["target"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[target]; ok {
		return map[string]any{"type": "agent_info", "agent": maps.Clone(a)}, ""
	}
	for _, a := range f.agents {
		if a["name"] == target {
			return map[string]any{"type": "agent_info", "agent": maps.Clone(a)}, ""
		}
	}
	return nil, "agent_not_found"
}

func (f *Fixture) sorted() []map[string]any {
	keys := make([]string, 0, len(f.agents))
	for k := range f.agents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, maps.Clone(f.agents[k]))
	}
	return out
}

func (f *Fixture) agentList(map[string]any) (any, string) {
	if c := f.failed("agent.list"); c != "" {
		return nil, c
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"type": "agent_list", "agents": f.sorted()}, ""
}

func (f *Fixture) paneList(map[string]any) (any, string) {
	if c := f.failed("pane.list"); c != "" {
		return nil, c
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var panes []map[string]any
	for _, a := range f.sorted() {
		panes = append(panes, map[string]any{"pane_id": a["pane_id"], "tab_id": a["tab_id"], "workspace_id": a["workspace_id"], "focused": true})
	}
	return map[string]any{"type": "pane_list", "panes": panes}, ""
}

func (f *Fixture) paneRead(p map[string]any) (any, string) {
	if c := f.failed("pane.read"); c != "" {
		return nil, c
	}
	pane, _ := p["pane_id"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": pane, "text": f.screen[pane]}}, ""
}

func (f *Fixture) ok(method string) Handler {
	return func(map[string]any) (any, string) {
		if c := f.failed(method); c != "" {
			return nil, c
		}
		return map[string]any{"type": "ok"}, ""
	}
}

func (f *Fixture) notify(map[string]any) (any, string) {
	if c := f.failed("notification.show"); c != "" {
		return nil, c
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"type": "notification_show", "shown": f.shown}, ""
}
