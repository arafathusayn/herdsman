package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/route"
)

// Action is one browser request to change something in herdr.
type Action struct {
	Type      string   `json:"type"`
	Pane      string   `json:"pane"`
	Text      string   `json:"text,omitempty"`
	Keys      []string `json:"keys,omitempty"`
	Name      string   `json:"name,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Args      []string `json:"args,omitempty"`
	Label     string   `json:"label,omitempty"`
	Direction string   `json:"direction,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	Force     bool     `json:"force,omitempty"`
	// Lines, Col and Row are for scroll: lines to move (positive scrolls
	// back into history) and the 1-based cell under the pointer.
	Lines int `json:"lines,omitempty"`
	Col   int `json:"col,omitempty"`
	Row   int `json:"row,omitempty"`
}

// Result is what the browser gets back for an action.
type Result struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Code    string          `json:"code,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Summary string          `json:"summary,omitempty"`
}

var agentName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// errNeedsForce marks an action the browser must confirm with force.
var errNeedsForce = errors.New("needs_force")

type job struct {
	a Action
	// fn, when set, runs instead of a: a job of several herdr calls that
	// no other queued mutation may interleave with.
	fn   func(context.Context) Result
	done chan Result
}

// Queue runs herdr mutations one at a time, so two browser tabs never race
// each other in the same session, and puts the user's keyboard focus back
// after every action that can move it.
type Queue struct {
	client *herdr.Client
	hub    *Hub
	jobs   chan job
	now    func() time.Time
	// scrolled marks panes this queue scrolled back into herdr's
	// scrollback. Only the Run goroutine touches it.
	scrolled map[string]bool
}

func NewQueue(c *herdr.Client, h *Hub) *Queue {
	return &Queue{client: c, hub: h, jobs: make(chan job), now: time.Now, scrolled: map[string]bool{}}
}

func (q *Queue) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-q.jobs:
			if j.fn != nil {
				j.done <- safeJob(ctx, j.fn)
			} else {
				j.done <- q.safeRun(ctx, j.a)
			}
		}
	}
}

// safeRun runs one action and turns a panic into its result, so one bad
// action never stops the queue that every browser shares.
func (q *Queue) safeRun(ctx context.Context, a Action) (r Result) {
	defer func() {
		if p := recover(); p != nil {
			r = Result{Code: "internal_error", Error: fmt.Sprintf("the action failed: %v", p)}
		}
	}()
	return q.run(ctx, a)
}

// safeJob runs a multi-step job and turns a panic into its result.
func safeJob(ctx context.Context, fn func(context.Context) Result) (r Result) {
	defer func() {
		if p := recover(); p != nil {
			r = Result{Code: "internal_error", Error: fmt.Sprintf("the job failed: %v", p)}
		}
	}()
	return fn(ctx)
}

// DoJob queues a job of several herdr calls and waits for its result. The
// queue runs nothing else until it returns.
func (q *Queue) DoJob(ctx context.Context, fn func(context.Context) Result) Result {
	return q.submit(ctx, job{fn: fn, done: make(chan Result, 1)})
}

// Do queues an action and waits for its result.
func (q *Queue) Do(ctx context.Context, a Action) Result {
	j := job{a: a, done: make(chan Result, 1)}
	return q.submit(ctx, j)
}

func (q *Queue) submit(ctx context.Context, j job) Result {
	select {
	case q.jobs <- j:
	case <-ctx.Done():
		return Result{Error: ctx.Err().Error()}
	}
	select {
	case r := <-j.done:
		return r
	case <-ctx.Done():
		return Result{Error: ctx.Err().Error()}
	}
}

// movesFocus lists the actions that can move the user's keyboard focus into
// the target pane.
var movesFocus = map[string]bool{"prompt": true, "keys": true, "text": true, "start": true, "input": true}

// quiet lists the actions of the terminal mode. They come one keystroke or
// wheel notch at a time and can carry passwords, so they never reach the
// activity stream or the route log.
var quiet = map[string]bool{"input": true}

func (q *Queue) run(ctx context.Context, a Action) Result {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if a.Type == "scroll" {
		return q.scroll(ctx, a)
	}
	st := q.hub.Current()
	method, params, summary, err := plan(a, st)
	if err != nil {
		if errors.Is(err, errNeedsForce) {
			return Result{Code: "needs_force", Error: summary}
		}
		return Result{Code: "invalid_action", Error: err.Error()}
	}

	// Typing into a pane scrolled back jumps to the bottom, as in a terminal.
	if a.Type == "input" && (q.scrolled[a.Pane] || scrolledBack(st, a.Pane)) {
		if err := q.client.Call(ctx, "pane.scroll", map[string]any{"pane_id": a.Pane, "offset_from_bottom": 0}, nil); err == nil {
			delete(q.scrolled, a.Pane)
		}
	}
	var raw json.RawMessage
	callErr := q.keepFocus(ctx, movesFocus[a.Type], func() error {
		return q.client.Call(ctx, method, params, &raw)
	})
	res := result(raw, summary, callErr)
	if quiet[a.Type] {
		return res
	}
	q.hub.Log(Entry{At: q.now(), Source: "web", Kind: a.Type, PaneID: a.Pane, Text: outcome(summary, callErr)})
	if r := routeOf(st, a.Pane); r != nil {
		if err := route.AppendLog(r.Dir, "web: "+outcome(summary, callErr), q.now()); err != nil {
			res.Error = strings.TrimSpace(res.Error + "; route log: " + err.Error())
		}
	}
	q.hub.Refresh()
	return res
}

// keepFocus runs call and, when it may move herdr's focus, puts the user's
// focused tab back afterwards.
func (q *Queue) keepFocus(ctx context.Context, moves bool, call func() error) error {
	var focusedTab string
	if moves {
		if snap, err := q.client.Snapshot(ctx); err == nil && snap.FocusedTabID != nil {
			focusedTab = *snap.FocusedTabID
		}
	}
	err := call()
	if focusedTab != "" {
		if snap, err := q.client.Snapshot(ctx); err == nil && snap.FocusedTabID != nil && *snap.FocusedTabID != focusedTab {
			_ = q.client.Call(ctx, "tab.focus", map[string]any{"tab_id": focusedTab}, nil)
		}
	}
	return err
}

func result(raw json.RawMessage, summary string, err error) Result {
	res := Result{OK: err == nil, Result: raw, Summary: summary}
	if err != nil {
		res.Error = err.Error()
		if he, ok := errors.AsType[*herdr.Error](err); ok {
			res.Code = he.Code
		}
	}
	return res
}

// maxWheel caps the wheel events one scroll action sends.
const maxWheel = 10

// maxScroll bounds one scroll; it is far past any real scrollback step.
const maxScroll = 100000

// scroll moves a pane's view the way a mouse wheel over a terminal would.
// A pane with herdr scrollback scrolls there. An agent's full-screen UI has
// no scrollback, so it gets SGR mouse wheel events at the pointer: agents
// that read the mouse scroll, the others ignore them. Any other pane is
// left alone, because a shell would take wheel bytes as typed text.
func (q *Queue) scroll(ctx context.Context, a Action) Result {
	if a.Pane == "" || a.Lines == 0 {
		return Result{Code: "invalid_action", Error: "pane and lines are required"}
	}
	if a.Lines < -maxScroll || a.Lines > maxScroll {
		return Result{Code: "invalid_action", Error: fmt.Sprintf("lines must be between -%d and %d", maxScroll, maxScroll)}
	}
	p, err := q.client.Pane(ctx, a.Pane)
	if err != nil {
		return result(nil, "", err)
	}
	switch {
	case p.Scroll != nil && p.Scroll.Max > 0:
		off := max(0, min(int64(p.Scroll.Offset)+int64(a.Lines), int64(p.Scroll.Max)))
		err := q.client.Call(ctx, "pane.scroll", map[string]any{"pane_id": a.Pane, "offset_from_bottom": off}, nil)
		if err == nil {
			markScrolled(q.scrolled, a.Pane, off > 0, q.hub.Current())
		}
		raw, _ := json.Marshal(herdr.Scroll{Offset: uint64(off), Max: p.Scroll.Max, Rows: p.Scroll.Rows})
		return result(raw, "scrolled", err)
	case p.Agent != nil && *p.Agent != "":
		button, n := 64, a.Lines // 64 is wheel up, 65 wheel down
		if n < 0 {
			button, n = 65, -n
		}
		col, row := max(1, min(a.Col, 1000)), max(1, min(a.Row, 1000))
		text := strings.Repeat(fmt.Sprintf("\x1b[<%d;%d;%dM", button, col, row), min(n, maxWheel))
		err := q.keepFocus(ctx, true, func() error {
			return q.client.Call(ctx, "pane.send_text", map[string]any{"pane_id": a.Pane, "text": text}, nil)
		})
		return result(nil, "sent the wheel", err)
	}
	return Result{OK: true, Summary: "nothing to scroll"}
}

// markScrolled records whether pane is scrolled back, keeping marks only
// for scrolled-back panes the last snapshot still lists (A24: pane churn
// never grows the map).
func markScrolled(m map[string]bool, pane string, back bool, st State) {
	if back {
		m[pane] = true
	} else {
		delete(m, pane)
	}
	if len(st.Snapshot.Panes) == 0 {
		return
	}
	alive := make(map[string]bool, len(st.Snapshot.Panes))
	for _, p := range st.Snapshot.Panes {
		alive[p.ID] = true
	}
	for id := range m {
		if !alive[id] && id != pane {
			delete(m, id)
		}
	}
}

// scrolledBack reports whether the last snapshot shows the pane scrolled
// back, for scrolls made in herdr itself.
func scrolledBack(st State, pane string) bool {
	for _, p := range st.Snapshot.Panes {
		if p.ID == pane {
			return p.Scroll != nil && p.Scroll.Offset > 0
		}
	}
	return false
}

func outcome(summary string, err error) string {
	if err != nil {
		return summary + " (failed: " + err.Error() + ")"
	}
	return summary
}

// plan turns an action into one herdr call and a one-line summary for the
// route log and the activity stream.
func plan(a Action, st State) (string, any, string, error) {
	if a.Pane == "" {
		return "", nil, "", fmt.Errorf("pane is required")
	}
	who := describe(st, a.Pane)
	coordinator := coordinatorRole(st, a.Pane)
	switch a.Type {
	case "prompt":
		if strings.TrimSpace(a.Text) == "" {
			return "", nil, "", fmt.Errorf("text is required")
		}
		return "agent.prompt", map[string]any{"target": a.Pane, "text": a.Text},
			fmt.Sprintf("prompted %s: %q", who, clip(a.Text, 120)), nil
	case "keys":
		if len(a.Keys) == 0 {
			return "", nil, "", fmt.Errorf("keys are required")
		}
		return "pane.send_keys", map[string]any{"pane_id": a.Pane, "keys": a.Keys},
			fmt.Sprintf("sent keys %s to %s", strings.Join(a.Keys, " "), who), nil
	case "text":
		if a.Text == "" {
			return "", nil, "", fmt.Errorf("text is required")
		}
		return "pane.send_text", map[string]any{"pane_id": a.Pane, "text": a.Text},
			fmt.Sprintf("typed into %s: %q", who, clip(a.Text, 120)), nil
	case "start":
		if !agentName.MatchString(a.Name) {
			return "", nil, "", fmt.Errorf("agent name must match %s", agentName)
		}
		if strings.TrimSpace(a.Kind) == "" {
			return "", nil, "", fmt.Errorf("kind is required")
		}
		p := map[string]any{"pane_id": a.Pane, "name": a.Name, "kind": a.Kind, "args": nonNil(a.Args), "timeout_ms": 90000}
		return "agent.start", p, fmt.Sprintf("started %s (%s %s) in %s", a.Name, a.Kind, strings.Join(a.Args, " "), a.Pane), nil
	case "rename_agent":
		if !agentName.MatchString(a.Name) {
			return "", nil, "", fmt.Errorf("agent name must match %s", agentName)
		}
		return "agent.rename", map[string]any{"target": a.Pane, "name": a.Name},
			fmt.Sprintf("renamed agent in %s to %s", a.Pane, a.Name), nil
	case "rename_pane":
		return "pane.rename", map[string]any{"pane_id": a.Pane, "label": a.Label},
			fmt.Sprintf("labelled %s %q", who, a.Label), nil
	case "split":
		dir := a.Direction
		if dir != "right" && dir != "down" {
			return "", nil, "", fmt.Errorf("direction must be right or down")
		}
		p := map[string]any{"target_pane_id": a.Pane, "direction": dir, "focus": false}
		if a.Cwd != "" {
			p["cwd"] = a.Cwd
		}
		return "pane.split", p, fmt.Sprintf("split %s %s", who, dir), nil
	case "close": // herdr's pane.close; the app calls it deleting a pane
		s := fmt.Sprintf("deleted pane %s", who)
		if !a.Force {
			reason := "deleting a pane ends what runs in it"
			if coordinator != "" {
				reason = "this is the " + coordinator + "'s pane"
			}
			return "", nil, s + ": " + reason + "; confirm to delete it", errNeedsForce
		}
		return "pane.close", map[string]any{"pane_id": a.Pane}, s, nil
	case "focus":
		return "pane.focus", map[string]any{"pane_id": a.Pane}, fmt.Sprintf("focused %s in herdr", who), nil
	case "input": // terminal mode: one batch of typing, keys or text, never logged
		switch {
		case len(a.Keys) > 0 && a.Text == "":
			return "pane.send_keys", map[string]any{"pane_id": a.Pane, "keys": a.Keys}, "input", nil
		case a.Text != "" && len(a.Keys) == 0:
			return "pane.send_text", map[string]any{"pane_id": a.Pane, "text": a.Text}, "input", nil
		}
		return "", nil, "", fmt.Errorf("input needs keys or text, not both")
	}
	return "", nil, "", fmt.Errorf("unknown action %q", a.Type)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// describe names a pane by its agent name or label, with its id.
func describe(st State, pane string) string {
	for _, a := range st.Snapshot.Agents {
		if a.PaneID == pane && a.Name != nil && *a.Name != "" {
			return *a.Name + " (" + pane + ")"
		}
	}
	for _, p := range st.Snapshot.Panes {
		if p.ID == pane && p.Label != nil && *p.Label != "" {
			return *p.Label + " (" + pane + ")"
		}
	}
	return pane
}

// coordinatorRole names the role of a live route's orchestrator or adjutant
// pane, or "" for any other pane.
func coordinatorRole(st State, pane string) string {
	for _, r := range st.Routes {
		switch {
		case !r.Live:
		case r.OrchestratorPane == pane:
			return "orchestrator"
		case r.AdjutantPane != "" && r.AdjutantPane == pane:
			return "adjutant"
		}
	}
	return ""
}

// routeOf finds the live route whose task table or orchestrator line names
// the pane, so the action lands in that route's run state.
func routeOf(st State, pane string) *route.Route {
	for _, r := range st.Routes {
		if !r.Live {
			continue
		}
		if r.OrchestratorPane == pane || (r.AdjutantPane != "" && r.AdjutantPane == pane) {
			return r.Route
		}
		for _, t := range r.Tasks {
			if t.Pane == pane {
				return r.Route
			}
		}
	}
	return nil
}
