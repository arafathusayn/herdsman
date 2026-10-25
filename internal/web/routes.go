package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Creating a route from the web app: a tab per coordinator, the agent
// started through herdr's agent.start (so it can enroll in the message
// channel), the route command sent as its first prompt, and a checkpointer
// pane beside the orchestrator. Everything is checked before the first
// herdr call, and the calls run as one queued job, one at a time.

// Bounds of the checkpointer settings the form accepts.
const (
	CheckpointMinMinutes = 5
	CheckpointMaxMinutes = 240
	CheckpointMinTokens  = 50000
	CheckpointMaxTokens  = 2000000
)

const (
	maxRouteRequest = 8000 // runes
	agentStartMS    = 90000
	routeJobTimeout = 5 * time.Minute
)

var (
	routeName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	projectSafe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	paneIDRe    = regexp.MustCompile(`^[A-Za-z0-9:_-]+$`)
	inboxChars  = regexp.MustCompile(`^[A-Za-z0-9._/@+-]+$`)
)

// RouteCheckpointer is the checkpointer part of a new route.
type RouteCheckpointer struct {
	On      bool `json:"on"`
	Minutes int  `json:"minutes"`
	Limit   int  `json:"limit"`
}

// RouteRequest is the New route form as the browser sends it.
type RouteRequest struct {
	Role         string            `json:"role"` // "orchestrator" or "adjutant"
	WorkspaceID  string            `json:"workspace_id"`
	Folder       string            `json:"folder"`
	Kind         string            `json:"kind"`
	Model        string            `json:"model"`
	Effort       string            `json:"effort"`
	Name         string            `json:"name"`
	Request      string            `json:"request"`
	Checkpointer RouteCheckpointer `json:"checkpointer"`
	// The orchestrator an adjutant route starts beside the adjutant.
	OrchName   string `json:"orch_name,omitempty"`
	OrchKind   string `json:"orch_kind,omitempty"`
	OrchModel  string `json:"orch_model,omitempty"`
	OrchEffort string `json:"orch_effort,omitempty"`
	RouteName  string `json:"route_name,omitempty"`
	Inbox      string `json:"inbox,omitempty"`
}

// RouteStep is one herdr call of the job and how it went.
type RouteStep struct {
	Step   string `json:"step"`
	Method string `json:"method"`
	Status string `json:"status"` // "ok", "failed" or "skipped"
	Reason string `json:"reason,omitempty"`
	Code   string `json:"code,omitempty"`
	ID     string `json:"id,omitempty"`
}

// RouteResult is what the browser gets back.
type RouteResult struct {
	OK    bool        `json:"ok"`
	Code  string      `json:"code,omitempty"`
	Field string      `json:"field,omitempty"`
	Error string      `json:"error,omitempty"`
	Steps []RouteStep `json:"steps"`
	// The panes and tabs created so far; nothing is closed on a failure.
	Tabs             []string `json:"tabs"`
	OrchestratorPane string   `json:"orchestrator_pane,omitempty"`
	AdjutantPane     string   `json:"adjutant_pane,omitempty"`
	CheckpointerPane string   `json:"checkpointer_pane,omitempty"`
	// AttachCommand is what the human types into the adjutant pane.
	AttachCommand string `json:"attach_command,omitempty"`
}

// routeError is a refused field.
type routeError struct{ field, msg string }

func (e *routeError) Error() string { return e.msg }

func refuse(field, format string, a ...any) error {
	return &routeError{field: field, msg: fmt.Sprintf(format, a...)}
}

// projectLabel is the folder's base name cut to a safe label.
func projectLabel(folder string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(filepath.Base(folder)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 24 {
		s = strings.TrimRight(s[:24], "-")
	}
	if !projectSafe.MatchString(s) {
		return ""
	}
	return s
}

// quoteArg puts the route request in double quotes for the harness's prompt
// line: a backslash and a double quote get a backslash. Newlines and tabs
// pass through inside the quotes; agent.prompt pastes the text as one prompt.
func quoteArg(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// checkRequestText refuses control characters other than newline and tab.
func checkRequestText(s string) error {
	if !utf8.ValidString(s) {
		return refuse("request", "the request is not valid text")
	}
	if utf8.RuneCountInString(s) > maxRouteRequest {
		return refuse("request", "the request is longer than %d characters", maxRouteRequest)
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return refuse("request", "the request holds a control character (U+%04X): only newlines and tabs are allowed", r)
		}
	}
	return nil
}

// expandInbox resolves the inbox path and keeps it inside the home folder.
func expandInbox(p, home string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) || !inboxChars.MatchString(p) || filepath.Clean(p) != p {
		return "", refuse("inbox", "the inbox must be a plain absolute path (letters, digits, . _ / @ + -)")
	}
	rh := realPath(home)
	rp := realPath(p)
	if rel, err := filepath.Rel(rh, rp); home == "" || err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", refuse("inbox", "the inbox must be inside your home folder")
	}
	return p, nil
}

// launch is one coordinator to start.
type launch struct {
	role, name, kind, label string
	args                    []string
}

// routePlan is a checked request, ready to run.
type routePlan struct {
	req          RouteRequest
	folder       string
	project      string
	coordinators []launch // the adjutant first, then the orchestrator
	prompt       string   // sent to the orchestrator
	checkpointer string   // the shell line, or "" when off
	attach       string
}

// planRoute checks a request against the snapshot and the catalog and
// builds every value the herdr calls carry. It makes no herdr call.
func planRoute(req RouteRequest, st State, cat Catalog, home, skillDir string) (*routePlan, error) {
	p := &routePlan{req: req}
	if req.Role != "orchestrator" && req.Role != "adjutant" {
		return nil, refuse("role", "role must be orchestrator or adjutant")
	}
	if !wsKnown(st, req.WorkspaceID) {
		return nil, refuse("workspace_id", "unknown workspace %q", req.WorkspaceID)
	}
	if !filepath.IsAbs(req.Folder) {
		return nil, refuse("folder", "the folder must be an absolute path")
	}
	p.folder = realPath(req.Folder)
	if fi, err := os.Stat(p.folder); err != nil {
		return nil, refuse("folder", "folder does not exist: %s", req.Folder)
	} else if !fi.IsDir() {
		return nil, refuse("folder", "not a folder: %s", req.Folder)
	}
	if p.project = projectLabel(p.folder); p.project == "" {
		return nil, refuse("folder", "the folder name gives no label: use a folder whose name has letters or digits")
	}
	if err := checkRequestText(req.Request); err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, a := range st.Snapshot.Agents {
		if a.Name != nil {
			live[*a.Name] = true
		}
	}
	checkName := func(field, n string) error {
		if !agentName.MatchString(n) {
			return refuse(field, "the agent name must match %s", agentName)
		}
		if live[n] {
			return refuse(field, "an agent named %s is already running", n)
		}
		return nil
	}
	coord := launch{role: "orchestrator", name: req.Name, kind: req.Kind, label: p.project + "-orchestrator"}
	model, effort, nameField := req.Model, req.Effort, "name"
	if req.Role == "adjutant" {
		if err := checkName("name", req.Name); err != nil {
			return nil, err
		}
		adjArgs, err := launchArgs(cat.kind(req.Kind), req.Model, req.Effort)
		if err != nil {
			return nil, refuse("model", "%s", err)
		}
		p.coordinators = append(p.coordinators, launch{role: "adjutant", name: req.Name, kind: req.Kind, label: p.project + "-adjutant", args: adjArgs})
		if req.OrchName == req.Name {
			return nil, refuse("orch_name", "the orchestrator needs a name of its own")
		}
		coord = launch{role: "orchestrator", name: req.OrchName, kind: req.OrchKind, label: p.project + "-orchestrator"}
		model, effort, nameField = req.OrchModel, req.OrchEffort, "orch_name"
		if !routeName.MatchString(req.RouteName) {
			return nil, refuse("route_name", "the route name must match %s", routeName)
		}
		inbox := req.Inbox
		if inbox == "" {
			inbox = "~/.herdsman-private/" + p.project + "-" + req.RouteName
		}
		var ierr error
		if p.req.Inbox, ierr = expandInbox(inbox, home); ierr != nil {
			return nil, ierr
		}
	} else if req.OrchName != "" || req.OrchKind != "" || req.RouteName != "" || req.Inbox != "" {
		return nil, refuse("role", "adjutant fields are set on an orchestrator route")
	}
	if err := checkName(nameField, coord.name); err != nil {
		return nil, err
	}
	ck := cat.kind(coord.kind)
	args, err := launchArgs(ck, model, effort)
	if err != nil {
		f := "model"
		if req.Role == "adjutant" {
			f = "orch_model"
		}
		return nil, refuse(f, "%s", err)
	}
	coord.args = args
	p.coordinators = append(p.coordinators, coord)

	prefix := ck.SkillPrefix + "herdsman"
	if req.Role == "adjutant" {
		p.prompt = prefix + " orchestrator --adjutant " + req.Name + " --name " + coord.name + " --inbox " + p.req.Inbox
		p.attach = cat.kind(req.Kind).SkillPrefix + "herdsman adjutant --name " + req.Name + " --orchestrator <orchestrator pane> --inbox " + p.req.Inbox
	} else {
		p.prompt = prefix + " run"
	}
	if req.Request != "" {
		p.prompt += " " + quoteArg(req.Request)
	}

	c := req.Checkpointer
	if c.On {
		if !ck.Checkpointer {
			return nil, refuse("checkpointer", "the checkpointer reads Claude Code and Codex transcripts only")
		}
		if c.Minutes < CheckpointMinMinutes || c.Minutes > CheckpointMaxMinutes {
			return nil, refuse("minutes", "the interval must be %d to %d minutes", CheckpointMinMinutes, CheckpointMaxMinutes)
		}
		if c.Limit < CheckpointMinTokens || c.Limit > CheckpointMaxTokens {
			return nil, refuse("limit", "the limit must be %d to %d tokens", CheckpointMinTokens, CheckpointMaxTokens)
		}
		script, why := checkpointerScript(home, skillDir)
		if script == "" {
			return nil, refuse("checkpointer", "%s", why)
		}
		// TARGET is filled in once the orchestrator's pane exists.
		line := "INTERVAL=" + strconv.Itoa(c.Minutes*60) + " LIMIT=" + strconv.Itoa(c.Limit) + " /bin/bash '" + script + "'"
		if req.Role == "adjutant" {
			line = "HERDSMAN_ORCHESTRATOR=" + coord.name + " HERDSMAN_INBOX=" + p.req.Inbox + " HERDSMAN_PREFLIGHT=" + p.req.Inbox + "/setup.json " + line
		}
		p.checkpointer = line
	}
	return p, nil
}

func wsKnown(st State, id string) bool {
	for _, w := range st.Snapshot.Workspaces {
		if w.ID == id {
			return true
		}
	}
	return false
}

// checkpointerLine is the fixed command line typed into the checkpointer
// pane: every value in it is a checked pane id, name, number or path.
func (p *routePlan) checkpointerLine(target string) string {
	return "TARGET=" + target + " " + p.checkpointer
}

type routeStep struct {
	step, method string
	run          func(ctx context.Context) (string, error)
}

// steps lists every herdr call of the plan in order. Later steps read the
// ids earlier steps found, through res.
func (p *routePlan) steps(c *herdr.Client, res *RouteResult) []routeStep {
	var out []routeStep
	for _, l := range p.coordinators {
		pane := new(string)
		out = append(out,
			routeStep{"create tab " + l.label, "tab.create", func(ctx context.Context) (string, error) {
				tab, id, err := c.TabCreate(ctx, p.req.WorkspaceID, p.folder, l.label)
				if tab != "" {
					res.Tabs = append(res.Tabs, tab)
				}
				if err == nil && !paneIDRe.MatchString(id) {
					err = fmt.Errorf("herdr answered an unexpected pane id %q", id)
				}
				*pane = id
				if l.role == "adjutant" {
					res.AdjutantPane = id
				} else {
					res.OrchestratorPane = id
				}
				return tab + " " + id, err
			}},
			routeStep{"start " + l.role + " " + l.name + " (" + l.kind + ")", "agent.start", func(ctx context.Context) (string, error) {
				_, err := c.AgentStart(ctx, l.name, l.kind, *pane, l.args, agentStartMS)
				return *pane, err
			}},
		)
	}
	orch := p.coordinators[len(p.coordinators)-1]
	out = append(out, routeStep{"send the route command to " + orch.name, "agent.prompt", func(ctx context.Context) (string, error) {
		return res.OrchestratorPane, promptWhenReady(ctx, c, res.OrchestratorPane, p.prompt)
	}})
	if p.checkpointer == "" {
		return out
	}
	out = append(out,
		routeStep{"split a checkpointer pane", "pane.split", func(ctx context.Context) (string, error) {
			id, err := c.PaneSplit(ctx, res.OrchestratorPane, "right", p.folder)
			if err == nil && !paneIDRe.MatchString(id) {
				err = fmt.Errorf("herdr answered an unexpected pane id %q", id)
			}
			res.CheckpointerPane = id
			return id, err
		}},
		routeStep{"label it checkpointer", "pane.rename", func(ctx context.Context) (string, error) {
			return res.CheckpointerPane, c.Call(ctx, "pane.rename", map[string]any{"pane_id": res.CheckpointerPane, "label": "checkpointer"}, nil)
		}},
		routeStep{"type the checkpointer command", "pane.send_text", func(ctx context.Context) (string, error) {
			return res.CheckpointerPane, c.Call(ctx, "pane.send_text", map[string]any{"pane_id": res.CheckpointerPane, "text": p.checkpointerLine(res.OrchestratorPane)}, nil)
		}},
		routeStep{"run it", "pane.send_keys", func(ctx context.Context) (string, error) {
			return res.CheckpointerPane, c.Call(ctx, "pane.send_keys", map[string]any{"pane_id": res.CheckpointerPane, "keys": []string{"enter"}}, nil)
		}},
	)
	return out
}

// runRoute runs the steps in order and stops at the first failure. It
// never closes what it created: those are the user's panes now.
func (q *Queue) runRoute(ctx context.Context, p *routePlan) RouteResult {
	ctx, cancel := context.WithTimeout(ctx, routeJobTimeout)
	defer cancel()
	res := RouteResult{Steps: []RouteStep{}, Tabs: []string{}}
	if err := q.recheckNames(ctx, p); err != nil {
		res.Error = err.Error()
		if re, ok := errors.AsType[*routeError](err); ok {
			res.Code, res.Field = "invalid_route", re.field
		} else if he, ok := errors.AsType[*herdr.Error](err); ok {
			res.Code = he.Code
		} else {
			res.Code = "herdr_error"
		}
		return res
	}
	steps := p.steps(q.client, &res)
	q.hub.Log(Entry{At: q.now(), Source: "web", Kind: "route", Text: fmt.Sprintf("new %s route in %s (request of %d characters)", p.req.Role, p.folder, utf8.RuneCountInString(p.req.Request))})
	var failed error
	_ = q.keepFocus(ctx, true, func() error {
		for _, s := range steps {
			if failed != nil {
				res.Steps = append(res.Steps, RouteStep{Step: s.step, Method: s.method, Status: "skipped", Reason: "an earlier step failed"})
				continue
			}
			id, err := s.run(ctx)
			rs := RouteStep{Step: s.step, Method: s.method, Status: "ok", ID: strings.TrimSpace(id)}
			if err != nil {
				failed = err
				rs.Status, rs.Reason = "failed", err.Error()
				if he, ok := errors.AsType[*herdr.Error](err); ok {
					rs.Code = he.Code
				}
				res.Code, res.Error = rs.Code, s.step+": "+err.Error()
			}
			res.Steps = append(res.Steps, rs)
			q.hub.Log(Entry{At: q.now(), Source: "web", Kind: "route", PaneID: rs.ID, Text: outcome("route: "+s.step, err)})
		}
		return failed
	})
	res.OK = failed == nil
	if res.Code == "" && failed != nil {
		res.Code = "herdr_error"
	}
	if p.attach != "" {
		res.AttachCommand = strings.Replace(p.attach, "<orchestrator pane>", orDefault(res.OrchestratorPane, "<orchestrator pane>"), 1)
	}
	q.hub.Refresh()
	return res
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// createRoute serves POST /api/routes.
func (s *Server) createRoute(w http.ResponseWriter, r *http.Request) {
	var req RouteRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, RouteResult{Code: "bad_request", Error: err.Error(), Steps: []RouteStep{}, Tabs: []string{}})
		return
	}
	home, _ := os.UserHomeDir()
	p, err := planRoute(req, s.hub.Current(), s.catalog.get(r.Context()), home, s.cfg.SkillDir)
	if err != nil {
		re, _ := errors.AsType[*routeError](err)
		field := ""
		if re != nil {
			field = re.field
		}
		writeJSON(w, http.StatusBadRequest, RouteResult{Code: "invalid_route", Field: field, Error: err.Error(), Steps: []RouteStep{}, Tabs: []string{}})
		return
	}
	// The result travels in the job's completion message: once the browser
	// leaves, the handler never reads what the queue's worker writes.
	done := s.queue.DoJob(r.Context(), func(ctx context.Context) Result {
		res := s.queue.runRoute(ctx, p)
		raw, err := json.Marshal(res)
		if err != nil {
			return Result{Code: "internal_error", Error: err.Error()}
		}
		return Result{OK: res.OK, Result: raw}
	})
	var res RouteResult
	if done.Result == nil || json.Unmarshal(done.Result, &res) != nil {
		// The browser left (or the job failed before it ran a step): the job
		// may be queued or running; it is never cancelled half way.
		writeJSON(w, http.StatusServiceUnavailable, RouteResult{Code: orDefault(done.Code, "not_waited"),
			Error: "the request ended before the route finished; it may still be running: check the session (" + done.Error + ")",
			Steps: []RouteStep{}, Tabs: []string{}})
		return
	}
	status := http.StatusOK
	switch {
	case res.Code == "invalid_route":
		status = http.StatusBadRequest
	case !res.OK:
		status = http.StatusBadGateway
	}
	writeJSON(w, status, res)
}

// InboxState is what the checklist can read from a route's private inbox,
// without a lock or a write.
type InboxState struct {
	Path     string   `json:"path"`
	Exists   bool     `json:"exists"`
	Private  bool     `json:"private"` // a folder of mode 700, not a link
	Setup    bool     `json:"setup"`   // setup.json is there
	Proposed []string `json:"proposed"`
	Approved []string `json:"approved"`
	// PairProposed: the pending registry holds exactly this route's
	// adjutant and orchestrator. PairApproved: the approved copy is that
	// same registry, byte for byte, as the channel itself requires; an
	// approval of an older proposal is stale and does not count.
	PairProposed bool   `json:"pair_proposed"`
	PairApproved bool   `json:"pair_approved"`
	Error        string `json:"error,omitempty"`
}

// inboxState serves GET /api/routes/inbox?path=...: the state of each
// channel setup step the checklist shows. It reads only.
func (s *Server) inboxState(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	p, err := expandInbox(r.URL.Query().Get("path"), home)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, InboxState{Proposed: []string{}, Approved: []string{}, Error: err.Error()})
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, readInboxState(p, q.Get("adjutant"), q.Get("orchestrator")))
}

// isPair reports whether a registry is exactly this adjutant and orchestrator.
func isPair(reg []ChannelPeer, adjutant, orchestrator string) bool {
	if len(reg) != 2 || adjutant == "" || orchestrator == "" {
		return false
	}
	want := map[string]string{adjutant: "adjutant", orchestrator: "orchestrator"}
	for _, p := range reg {
		if want[p.Name] != p.Role {
			return false
		}
	}
	return true
}

func readInboxState(p, adjutant, orchestrator string) InboxState {
	st := InboxState{Path: p, Proposed: []string{}, Approved: []string{}}
	fi, err := os.Lstat(p)
	if err != nil {
		return st
	}
	st.Exists = true
	st.Private = fi.IsDir() && fi.Mode().Perm() == 0o700
	if !fi.IsDir() {
		st.Error = "not a folder"
		return st
	}
	st.Setup = inbox.RegularFile(filepath.Join(p, "setup.json"))
	names := func(file string) []string {
		out := []string{}
		if !inbox.RegularFile(filepath.Join(p, file)) {
			return out
		}
		peers, ok := webRegistry(inbox.ReadBounded(filepath.Join(p, file), envelopeMax))
		if !ok {
			return out
		}
		for _, x := range peers {
			out = append(out, x.Name+" ("+x.Role+")")
		}
		slices.Sort(out)
		return out
	}
	st.Proposed, st.Approved = names("peers"), names("peers.approved")
	peers, perr := inbox.ReadBounded(filepath.Join(p, "peers"), envelopeMax)
	approved, aerr := inbox.ReadBounded(filepath.Join(p, "peers.approved"), envelopeMax)
	preg, pok := webRegistry(peers, perr)
	st.PairProposed = pok && inbox.RegularFile(filepath.Join(p, "peers")) && isPair(preg, adjutant, orchestrator)
	_, aok := webRegistry(approved, aerr)
	st.PairApproved = st.PairProposed && aok && inbox.RegularFile(filepath.Join(p, "peers.approved")) && bytes.Equal(peers, approved)
	return st
}

// promptReadyWait bounds how long the route command waits for herdr to
// register the agent it just started: right after agent.start, herdr can
// still answer agent_not_ready for a moment.
var (
	promptReadyWait  = 20 * time.Second
	promptReadyRetry = 500 * time.Millisecond
)

// promptWhenReady sends the route command to the agent in pane, retrying
// while herdr answers agent_not_ready, up to promptReadyWait.
func promptWhenReady(ctx context.Context, c *herdr.Client, pane, text string) error {
	deadline := time.Now().Add(promptReadyWait)
	for {
		err := c.Call(ctx, "agent.prompt", map[string]any{"target": pane, "text": text}, nil)
		he, ok := errors.AsType[*herdr.Error](err)
		if err == nil || !ok || he.Code != "agent_not_ready" || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(promptReadyRetry):
		}
	}
}

// recheckNames reads a fresh snapshot inside the queued job, before any tab
// is made: the hub's state can be older than a route another browser just
// started, and agent.start would refuse a taken name only after its tab
// exists.
func (q *Queue) recheckNames(ctx context.Context, p *routePlan) error {
	snap, err := q.client.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, l := range p.coordinators {
		for _, a := range snap.Agents {
			if a.Name == nil || *a.Name != l.name {
				continue
			}
			field := "name"
			if l.role == "orchestrator" && p.req.Role == "adjutant" {
				field = "orch_name"
			}
			return refuse(field, "an agent named %s is already running", l.name)
		}
	}
	return nil
}
