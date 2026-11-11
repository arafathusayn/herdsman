package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/checkpoint/oncore"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// coreServicePoll is how often a core service looks again.
const coreServicePoll = 30 * time.Second

// coreServices owns the supporting services of routes on herdr-core (spec
// section 13): one owner per service, the daemon, with no pane copy beside
// it. Each service acts on a typed execution reference, the agent resolved to
// its current generation at each pass, never a pane. The registrations are
// kept in the daemon's folder, so a restarted daemon takes them up again.
type coreServices struct {
	c *coreRuntime
	// poll is the pass interval (default coreServicePoll).
	poll time.Duration

	mu      sync.Mutex
	running map[string]*coreService
}

type coreService struct {
	spec   route.ServiceSpec
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *coreServices) path() string { return filepath.Join(s.c.d.daemonDir(), "core-services.json") }

// load reads the registrations ({} when none).
func (s *coreServices) load() (map[string]route.ServiceSpec, error) {
	out := map[string]route.ServiceSpec{}
	b, err := readSmall(s.path(), 1<<20)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var list []route.ServiceSpec
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path(), err)
	}
	for _, sp := range list {
		out[sp.Key()] = sp
	}
	return out, nil
}

func (s *coreServices) save(all map[string]route.ServiceSpec) error {
	list := make([]route.ServiceSpec, 0, len(all))
	for _, sp := range all {
		list = append(list, sp)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeDurable(s.path(), append(b, '\n'), 0o600)
}

// Start implements route.Services: it records the service and runs it once.
func (s *coreServices) Start(_ context.Context, sp route.ServiceSpec) error {
	switch sp.Name {
	case route.ServiceCheckpoint, route.ServiceCardWatch, route.ServiceCourier:
	default:
		return fmt.Errorf("no core service is named %q", sp.Name)
	}
	if sp.Name == route.ServiceCourier {
		// The courier is the route's daemon channel: its durable outbox
		// moves the pair's messages, so nothing else runs for it.
		if err := s.c.channelOpen(sp.Route, sp.Inbox); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	if prev, ok := all[sp.Key()]; !ok || !sameSpec(prev, sp) {
		all[sp.Key()] = sp
		if err := s.save(all); err != nil {
			return err
		}
	}
	s.runLocked(sp)
	return nil
}

func sameSpec(a, b route.ServiceSpec) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Stop implements route.Services.
func (s *coreServices) Stop(_ context.Context, repo, name, service string) error {
	key := route.ServiceSpec{Repo: repo, Route: name, Name: service}.Key()
	s.mu.Lock()
	run := s.running[key]
	delete(s.running, key)
	all, err := s.load()
	if err == nil {
		if _, ok := all[key]; ok {
			delete(all, key)
			err = s.save(all)
		}
	}
	s.mu.Unlock()
	if run != nil {
		run.cancel()
		<-run.done
	}
	return err
}

// Running implements route.Services.
func (s *coreServices) Running(repo, name, service string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.running[route.ServiceSpec{Repo: repo, Route: name, Name: service}.Key()]
	return ok
}

// resume takes up the recorded services after a daemon start.
func (s *coreServices) resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		s.c.d.logf("core services: %v", err)
		return
	}
	for _, sp := range all {
		s.runLocked(sp)
	}
}

// runLocked starts the service's loop once; the caller holds mu.
func (s *coreServices) runLocked(sp route.ServiceSpec) {
	if _, ok := s.running[sp.Key()]; ok || sp.Name == route.ServiceCourier {
		if sp.Name == route.ServiceCourier {
			s.running[sp.Key()] = &coreService{spec: sp, cancel: func() {}, done: closedChan()}
		}
		return
	}
	ctx, cancel := context.WithCancel(s.c.ctx)
	run := &coreService{spec: sp, cancel: cancel, done: make(chan struct{})}
	s.running[sp.Key()] = run
	var pass func(context.Context) error
	switch sp.Name {
	case route.ServiceCheckpoint:
		pass = s.checkpointer(sp)
	case route.ServiceCardWatch:
		pass = s.cardWatch(sp)
	}
	poll := s.poll
	if poll <= 0 {
		poll = coreServicePoll
	}
	s.c.d.bg.Add(1)
	go func() {
		defer s.c.d.bg.Done()
		defer close(run.done)
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			if err := pass(ctx); err != nil && ctx.Err() == nil {
				s.c.d.logf("core service %s of %s: %v", sp.Name, sp.Route, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// checkpointSource is the checkpointer's view of one core route: the target
// agent's current execution, and the daemon's notices.
type checkpointSource struct {
	c  *coreRuntime
	sp route.ServiceSpec
}

func (src checkpointSource) Current(ctx context.Context) (runtime.Execution, bool, error) {
	return currentExecution(ctx, src.c, src.sp.Repo, src.sp.Route, src.sp.Agent)
}

func (src checkpointSource) Issue(ctx context.Context, key string, e runtime.Execution, text string) (notice.Notice, error) {
	svc := src.c.d.noticeSvc()
	if svc == nil {
		return notice.Notice{}, errors.New("this daemon serves no notices")
	}
	return svc.Issue(ctx, notice.IssueRequest{Key: key, Kind: notice.KindCheckpoint, Execution: e.ID, Content: text, Issuer: oncore.Issuer})
}

func (src checkpointSource) ByKey(key string) (notice.Notice, bool, error) {
	svc := src.c.d.noticeSvc()
	if svc == nil {
		return notice.Notice{}, false, nil
	}
	return svc.ByKey(key)
}

func (src checkpointSource) Emit(ev notice.Event) { src.c.d.emitNotice(ev) }

// currentExecution is an agent's running core execution of the latest
// generation, registered by its process.
func currentExecution(ctx context.Context, c *coreRuntime, repo, name, agent string) (runtime.Execution, bool, error) {
	all, err := c.records.List(ctx, repo, name)
	if err != nil {
		return runtime.Execution{}, false, err
	}
	var best runtime.Execution
	found := false
	for _, e := range all {
		if e.Backend != runtime.BackendCore || e.Agent != agent || e.Desired != runtime.DesiredRunning || e.Evidence.PID <= 0 {
			continue
		}
		if !found || e.Generation > best.Generation || (e.Generation == best.Generation && e.Created.After(best.Created)) {
			best, found = e, true
		}
	}
	return best, found, nil
}

// checkpointer is the checkpoint service's pass.
func (s *coreServices) checkpointer(sp route.ServiceSpec) func(context.Context) error {
	home, _ := os.UserHomeDir()
	if v := s.c.loginValue("HOME"); v != "" {
		home = v
	}
	cp := &oncore.Checkpointer{
		Config: oncore.Config{Interval: time.Duration(sp.IntervalMS) * time.Millisecond, Limit: sp.Limit, Kind: sp.Kind, Args: sp.Args,
			Home: home, CodexHome: s.c.loginValue("CODEX_HOME")},
		Source: checkpointSource{c: s.c, sp: sp},
		Now:    s.c.d.now,
	}
	if cp.Config.Interval <= 0 {
		cp.Config.Interval = 30 * time.Minute
	}
	last := ""
	return func(ctx context.Context) error {
		p, err := cp.Step(ctx)
		if err != nil {
			return err
		}
		// Say each new finding once, not every pass.
		if line := strings.Join(p.Outcomes, ",") + " " + p.Detail; p.Detail != "" && line != last {
			last = line
			s.c.d.logf("checkpointer of %s: %s", sp.Route, line)
		}
		return nil
	}
}

// loginValue is one login value of the daemon's launches.
func (c *coreRuntime) loginValue(k string) string {
	environ := c.cfg.Environ
	if environ == nil {
		environ = os.Environ()
	}
	for _, kv := range environ {
		if key, v, ok := strings.Cut(kv, "="); ok && key == k {
			return v
		}
	}
	return ""
}

// dialogRe is the screen of a dialog that only a person ends: a permission or
// trust question, a password, a usage limit. It adds a blocker; it never
// removes one the status reports, and it never confers identity.
var dialogRe = regexp.MustCompile(`(?im)^[ \t]*(([>]?[ \t]*[0-9]+[.)]?[ \t]*)?)(Do you want to proceed|Do you trust|Allow reads outside|Allow once|Allow always|Password for|Permission required|approval required|Yes, proceed|Yes, and don|escalated execution requires|Type your answer|Usage limit|Selected model is at capacity)`)

// cardWatch is the card watch's pass on core: each running member whose
// program reports blocked, or whose screen shows a dialog, raises
// needs_attention once per dialog. It types nothing and answers nothing.
func (s *coreServices) cardWatch(sp route.ServiceSpec) func(context.Context) error {
	seen := map[string]string{}
	return func(ctx context.Context) error {
		all, err := s.c.records.List(ctx, sp.Repo, sp.Route)
		if err != nil {
			return err
		}
		p := s.c.provider("")
		live := map[string]bool{}
		for _, e := range all {
			if e.Backend != runtime.BackendCore || e.Desired != runtime.DesiredRunning || e.Evidence.PID <= 0 {
				continue
			}
			live[e.ID] = true
			t := runtime.Target{ExecutionID: e.ID, Generation: e.Generation, TerminalAddress: e.TerminalAddress}
			text, err := p.Read(ctx, t, runtime.ReadRequest{Lines: 40, MaxBytes: 16 << 10})
			if err != nil {
				continue
			}
			reason := ""
			if m := dialogRe.FindStringSubmatch(text.Text); m != nil {
				reason = strings.TrimSpace(m[3])
			} else if e.Observation.Program == runtime.ProgramBlocked {
				reason = "the program reports blocked"
			}
			if reason == "" {
				delete(seen, e.ID)
				continue
			}
			if seen[e.ID] == reason {
				continue
			}
			seen[e.ID] = reason
			s.c.d.emitNotice(notice.Event{Kind: notice.EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Reason: "dialog",
				Detail: fmt.Sprintf("%s shows a dialog only a person ends (%s); nothing is typed into it", e.Agent, reason)})
		}
		for id := range seen {
			if !live[id] {
				delete(seen, id)
			}
		}
		return nil
	}
}

// daemonServices is the route package's view of the core services: resolved
// when a step runs, since the core runtime starts after the effects are made.
type daemonServices struct{ d *Daemon }

func (s daemonServices) owner() (*coreServices, error) {
	c := s.d.coreRT()
	if c == nil {
		return nil, runtime.Unavailable(runtime.BackendCore, "in this daemon: it runs no herdr-core services")
	}
	return c.services, nil
}

// Start implements route.Services.
func (s daemonServices) Start(ctx context.Context, sp route.ServiceSpec) error {
	o, err := s.owner()
	if err != nil {
		return err
	}
	return o.Start(ctx, sp)
}

// Stop implements route.Services.
func (s daemonServices) Stop(ctx context.Context, repo, name, service string) error {
	o, err := s.owner()
	if err != nil {
		return err
	}
	return o.Stop(ctx, repo, name, service)
}

// Running implements route.Services.
func (s daemonServices) Running(repo, name, service string) bool {
	o, err := s.owner()
	return err == nil && o.Running(repo, name, service)
}

// channelOpen checks that a route's pair channel is the daemon's: its courier
// then needs nothing more.
func (c *coreRuntime) channelOpen(name, inbox string) error {
	e, ok := c.d.routeEntry(name)
	if !ok || (inbox != "" && e.Inbox != inbox) {
		return fmt.Errorf("route %s has no daemon channel at %s; the courier runs only as the daemon's channel on herdr-core", name, inbox)
	}
	return nil
}
