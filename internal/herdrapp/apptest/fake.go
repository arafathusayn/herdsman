// Package apptest is a fake herdr-app for tests. It answers ls, do,
// commands and --version the way the installed app does, adopts labeled
// terminals of the core it is attached to, and can be quit, parked, pointed
// at another core or uninstalled, so tests never reach the person's window.
package apptest

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
)

// DefaultCommands are the command names the installed app lists that the
// adapter cares about, plus two it must never send.
var DefaultCommands = []string{
	"space.new", "space.close", "space.rename", "tab.new", "tab.close", "tab.rename",
	"pane.focus", "agent.focus", "terminal.park", "terminal.pin",
}

// Fake is one app installation with one home.
type Fake struct {
	mu        sync.Mutex
	home      string
	version   string
	commands  []string
	installed bool
	running   bool
	connected *Core
	remotes   map[string]*Core // machine name -> core the window also shows
	names     map[string]string
	active    struct{ space, tab string }
	pending   bool
	calls     []herdrapp.Invocation
}

// New returns an installed app at version 0.1.15 with its window closed.
func New(home string) *Fake {
	return &Fake{
		home: filepath.Clean(home), version: "0.1.15",
		commands: slices.Clone(DefaultCommands), installed: true,
		remotes: map[string]*Core{}, names: map[string]string{},
	}
}

// Open opens the window, attached to core.
func (f *Fake) Open(core *Core) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running, f.connected = true, core
}

// Quit closes the window. Core terminals keep running.
func (f *Fake) Quit() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
}

// Uninstall makes every run fail as a missing binary.
func (f *Fake) Uninstall() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = false
}

// SetVersion sets what --version prints.
func (f *Fake) SetVersion(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version = v
}

// SetCommands replaces the command list.
func (f *Fake) SetCommands(names []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = slices.Clone(names)
}

// AddMachine makes the window also show core as a remote machine.
func (f *Fake) AddMachine(name string, core *Core) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remotes[name] = core
}

// PendingOnly makes every do reply {"pending":true} and apply nothing, as a
// command whose core change has not landed.
func (f *Fake) PendingOnly(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = on
}

// Park does what terminal.park does: removes the terminal's pane and its
// placement labels on the attached core; the process keeps running.
func (f *Fake) Park(address string) {
	f.mu.Lock()
	core := f.connected
	f.mu.Unlock()
	if core != nil {
		core.appRemove(address, "herdr.space", "herdr.tab")
	}
}

// Calls returns every invocation so far.
func (f *Fake) Calls() []herdrapp.Invocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Run implements herdrapp.Runner.
func (f *Fake) Run(_ context.Context, inv herdrapp.Invocation) (herdrapp.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, herdrapp.Invocation{Home: inv.Home, Args: slices.Clone(inv.Args)})
	if !f.installed {
		return herdrapp.Output{Exit: -1}, &exec.Error{Name: "herdr-app", Err: exec.ErrNotFound}
	}
	if len(inv.Args) == 0 {
		return herdrapp.Output{Exit: -1}, fmt.Errorf("apptest: bare herdr-app would open the window")
	}
	switch inv.Args[0] {
	case "--version":
		return ok("herdr-app " + f.version + "\n"), nil
	case "commands":
		list := make([]map[string]any, 0, len(f.commands))
		for _, name := range f.commands {
			list = append(list, map[string]any{"name": name, "args": []any{}, "about": ""})
		}
		return okJSON(list), nil
	}
	if !f.running || filepath.Clean(inv.Home) != f.home {
		return herdrapp.Output{
			Exit:   2,
			Stderr: fmt.Appendf(nil, "herdr-app: no Herdr window at %s/herdr-app.sock: No such file or directory (os error 2)\n", inv.Home),
		}, nil
	}
	switch inv.Args[0] {
	case "ls":
		return okJSON(f.layout()), nil
	case "do":
		return f.do(inv.Args[1:]), nil
	}
	return herdrapp.Output{Exit: 2, Stderr: []byte("usage: herdr-app ls | ...\n")}, nil
}

func ok(s string) herdrapp.Output { return herdrapp.Output{Stdout: []byte(s)} }

func okJSON(v any) herdrapp.Output {
	b, _ := json.MarshalIndent(v, "", "  ")
	return herdrapp.Output{Stdout: append(b, '\n')}
}

func fail(message string) herdrapp.Output {
	b, _ := json.Marshal(map[string]string{"error": message})
	return herdrapp.Output{Stdout: append(b, '\n'), Exit: 1}
}

func (f *Fake) do(args []string) herdrapp.Output {
	if len(args) != 2 {
		return fail("do takes a command and one JSON object here")
	}
	command := args[0]
	if !slices.Contains(f.commands, command) {
		return fail("no such command: " + command)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(args[1]), &params); err != nil {
		return fail("arguments must be one JSON object of strings")
	}
	layout := f.layout()
	switch command {
	case "space.rename":
		if _, found := findSpace(layout, params["id"]); !found {
			return fail("no such space")
		}
	case "tab.rename":
		if _, _, found := findTab(layout, params["id"]); !found {
			return fail("no such tab")
		}
	case "agent.focus", "pane.focus":
		if len(layout.Locate(params["terminal"])) == 0 {
			return fail("no such running terminal")
		}
	default:
		return fail("apptest: " + command + " is not simulated")
	}
	if f.pending {
		return okJSON(map[string]bool{"pending": true})
	}
	switch command {
	case "space.rename", "tab.rename":
		f.names[params["id"]] = params["name"]
	case "agent.focus", "pane.focus":
		loc := layout.Locate(params["terminal"])[0]
		f.active.space, f.active.tab = loc.Space, loc.Tab
	}
	return okJSON(map[string]any{})
}

// layout builds what ls prints: the attached core's adopted terminals as
// local spaces, then each remote machine's with prefixed ids.
func (f *Fake) layout() herdrapp.Layout {
	layout := herdrapp.Layout{Spaces: []herdrapp.Space{}, Recent: []herdrapp.Recent{}}
	if f.connected != nil {
		layout.Spaces = append(layout.Spaces, f.connected.adopt("", f.names)...)
	}
	for _, machine := range slices.Sorted(maps.Keys(f.remotes)) {
		layout.Spaces = append(layout.Spaces, f.remotes[machine].adopt(machine, f.names)...)
	}
	for i := range layout.Spaces {
		s := &layout.Spaces[i]
		s.Active = s.ID == f.active.space || (f.active.space == "" && i == 0)
		for j := range s.Tabs {
			t := &s.Tabs[j]
			t.Active = t.ID == f.active.tab || (f.active.tab == "" && j == 0)
		}
	}
	return layout
}

func findSpace(l herdrapp.Layout, id string) (herdrapp.Space, bool) {
	for _, s := range l.Spaces {
		if s.ID == id {
			return s, true
		}
	}
	return herdrapp.Space{}, false
}

func findTab(l herdrapp.Layout, id string) (herdrapp.Space, herdrapp.Tab, bool) {
	for _, s := range l.Spaces {
		for _, t := range s.Tabs {
			if t.ID == id {
				return s, t, true
			}
		}
	}
	return herdrapp.Space{}, herdrapp.Tab{}, false
}

// Core is a fake herdr-core: terminals with labels. Writes through Run and
// SetLabels are the caller's (Herdsman's) and are logged; the app's own
// presentation labels are written separately on adoption.
type Core struct {
	mu     sync.Mutex
	ID     string
	order  []string
	terms  map[string]*terminal
	writes []map[string]string
}

type terminal struct {
	name   string
	labels map[string]string
	status string
}

// NewCore returns an empty core.
func NewCore(id string) *Core { return &Core{ID: id, terms: map[string]*terminal{}} }

// Run starts a terminal with labels, as terminal.run does.
func (c *Core) Run(address, name string, labels map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.terms[address]; !exists {
		c.order = append(c.order, address)
	}
	c.terms[address] = &terminal{name: name, labels: maps.Clone(labels)}
	if c.terms[address].labels == nil {
		c.terms[address].labels = map[string]string{}
	}
	c.writes = append(c.writes, maps.Clone(labels))
}

// SetLabels adds or replaces labels, as terminal.label does.
func (c *Core) SetLabels(address string, labels map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.terms[address]; t != nil {
		maps.Copy(t.labels, labels)
		c.writes = append(c.writes, maps.Clone(labels))
	}
}

// SetStatus sets what the terminal's program reports (OSC 7501); empty
// means it reports nothing.
func (c *Core) SetStatus(address, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.terms[address]; t != nil {
		t.status = status
	}
}

// Writes returns every label set the caller wrote.
func (c *Core) Writes() []map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.writes)
}

// TerminalLabels implements herdrapp.CoreLabels.
func (c *Core) TerminalLabels(_ context.Context, address string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.terms[address]
	if t == nil {
		return nil, fmt.Errorf("apptest: core %s has no terminal %s", c.ID, address)
	}
	return maps.Clone(t.labels), nil
}

func (c *Core) appRemove(address string, keys ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.terms[address]; t != nil {
		for _, k := range keys {
			delete(t.labels, k)
		}
	}
}

// adopt groups labeled terminals into spaces and tabs in first-seen order,
// and writes the app's own presentation labels, as the app does.
func (c *Core) adopt(machine string, names map[string]string) []herdrapp.Space {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := ""
	var machinePtr *string
	if machine != "" {
		prefix = machine + "/"
		machinePtr = &machine
	}
	var spaces []herdrapp.Space
	index := map[string]int{}
	for _, address := range c.order {
		t := c.terms[address]
		space, tab := t.labels["herdr.space"], t.labels["herdr.tab"]
		if space == "" {
			continue
		}
		if tab == "" {
			tab = space + "-tab"
		}
		spaceID, tabID := prefix+space, prefix+tab
		si, seen := index[spaceID]
		if !seen {
			si = len(spaces)
			index[spaceID] = si
			name := names[spaceID]
			if name == "" {
				name = "~"
			}
			spaces = append(spaces, herdrapp.Space{ID: spaceID, Name: name, Machine: machinePtr})
		}
		s := &spaces[si]
		ti := slices.IndexFunc(s.Tabs, func(x herdrapp.Tab) bool { return x.ID == tabID })
		if ti < 0 {
			name := names[tabID]
			if name == "" {
				name = fmt.Sprint(len(s.Tabs) + 1)
			}
			s.Tabs = append(s.Tabs, herdrapp.Tab{ID: tabID, Name: name})
			ti = len(s.Tabs) - 1
		}
		entry := herdrapp.Terminal{Terminal: prefix + address, Pane: "p-" + prefix + address, Name: t.name, SubAgents: []herdrapp.Terminal{}}
		if t.status != "" {
			status := t.status
			entry.Status = &status
		}
		s.Tabs[ti].Terminals = append(s.Tabs[ti].Terminals, entry)
		t.labels["where"] = s.Name + " / " + s.Tabs[ti].Name
		t.labels["herdr.seen"] = "1"
	}
	return spaces
}
