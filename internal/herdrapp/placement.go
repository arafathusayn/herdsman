package herdrapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// Placement and ownership labels a launch writes on its core terminal, in
// terminal.run, all at once.
const (
	LabelOwner     = "herdsman.owner"
	LabelRoute     = "herdsman.route"
	LabelAgent     = "herdsman.agent"
	LabelExecution = "herdsman.execution"
	LabelRole      = "herdsman.role"
	LabelSpace     = "herdr.space"
	LabelTab       = "herdr.tab"
)

// presentationLabels belong to the app. Herdsman never writes them.
var presentationLabels = []string{"herdr.seen", "herdr.pinned", "where"}

// ErrPresentationLabel is returned for a label set that names one of the
// app's presentation labels.
var ErrPresentationLabel = errors.New("herdrapp: presentation labels belong to the app")

// CheckLabels refuses a label set that would write herdr.seen, herdr.pinned
// or where. Every label write on a managed terminal goes through it.
func CheckLabels(labels map[string]string) error {
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		if slices.Contains(presentationLabels, key) {
			return fmt.Errorf("%w: %s", ErrPresentationLabel, key)
		}
	}
	return nil
}

// PlacementIDs are the opaque space and tab ids of one placement.
type PlacementIDs struct {
	Space string `json:"space"`
	Tab   string `json:"tab"`
}

// idPrefix marks ids Herdsman allocated, for a person reading labels.
const idPrefix = "hs-"

// NewID returns a fresh opaque id with 128 random bits. A nil reader uses
// crypto/rand.
func NewID(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("herdrapp: allocate id: %w", err)
	}
	return idPrefix + hex.EncodeToString(b[:]), nil
}

// NewPlacementIDs allocates a space id and a tab id, both fresh.
func NewPlacementIDs(r io.Reader) (PlacementIDs, error) {
	space, err := NewID(r)
	if err != nil {
		return PlacementIDs{}, err
	}
	tab, err := NewID(r)
	if err != nil {
		return PlacementIDs{}, err
	}
	return PlacementIDs{Space: space, Tab: tab}, nil
}

func (p PlacementIDs) valid() error {
	for _, id := range []string{p.Space, p.Tab} {
		if id == "" || strings.Contains(id, "/") {
			return fmt.Errorf("herdrapp: placement id %q is not a local opaque id", id)
		}
	}
	if p.Space == p.Tab {
		return errors.New("herdrapp: space and tab ids must differ")
	}
	return nil
}

// LaunchLabels are the labels of one managed agent's terminal.
type LaunchLabels struct {
	Owner     string // installation id
	Route     string
	Agent     string // logical agent id
	Execution string
	Role      string
	Placement PlacementIDs
}

// Map returns the label set for terminal.run. It refuses an empty value and
// never contains a presentation label.
func (l LaunchLabels) Map() (map[string]string, error) {
	if err := l.Placement.valid(); err != nil {
		return nil, err
	}
	labels := map[string]string{
		LabelOwner:     l.Owner,
		LabelRoute:     l.Route,
		LabelAgent:     l.Agent,
		LabelExecution: l.Execution,
		LabelRole:      l.Role,
		LabelSpace:     l.Placement.Space,
		LabelTab:       l.Placement.Tab,
	}
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		if labels[key] == "" {
			return nil, fmt.Errorf("herdrapp: label %s is empty", key)
		}
	}
	return labels, CheckLabels(labels)
}

// Owned is a terminal whose launch labels were read back from the selected
// core. It is the only way to name a container or focus a terminal, so the
// adapter acts only on what Herdsman placed.
type Owned struct {
	Terminal  string
	Execution string
	Placement PlacementIDs
}

// ErrNotOwned is returned when core labels do not match the launch.
var ErrNotOwned = errors.New("herdrapp: terminal is not owned by this launch")

// OwnedFromCore checks the labels read back from core against the launch's
// labels. Every one must be there with the same value.
func OwnedFromCore(address string, read map[string]string, want LaunchLabels) (Owned, error) {
	labels, err := want.Map()
	if err != nil {
		return Owned{}, err
	}
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		if read[key] != labels[key] {
			return Owned{}, fmt.Errorf("%w: %s on %s reads %q, launch wrote %q", ErrNotOwned, key, address, read[key], labels[key])
		}
	}
	return Owned{Terminal: address, Execution: want.Execution, Placement: want.Placement}, nil
}

// ErrNotApplied means a command was acknowledged but the window does not
// show its effect.
var ErrNotApplied = errors.New("herdrapp: command not applied")

// Observation is what the open window shows of one owned terminal. "Pane
// present" and "agent listed" are separate facts: a starting harness has a
// pane before it reports any status.
type Observation struct {
	// AppClosed is true when no window answers. Nothing else is known then;
	// it is not a loss of placement.
	AppClosed bool
	// PanePresent: the window shows the terminal locally in a pane.
	PanePresent bool
	// AgentListed: that pane's program reports a status, so the app lists
	// it as an agent.
	AgentListed bool
	// Where is the observed location when PanePresent.
	Where Location
	// Rearranged: shown, but not in the intended space and tab. A person
	// moved it; that does not change the execution, so the observed place
	// is reported rather than fought.
	Rearranged bool
}

// Observe reads the window once for an owned terminal.
func (a *App) Observe(ctx context.Context, owned Owned) (Observation, error) {
	layout, err := a.List(ctx)
	if errors.Is(err, ErrAppClosed) {
		return Observation{AppClosed: true}, nil
	}
	if err != nil {
		return Observation{}, err
	}
	return observe(layout, owned), nil
}

func observe(layout Layout, owned Owned) Observation {
	var local []Location
	for _, loc := range layout.Locate(owned.Terminal) {
		if loc.Local {
			local = append(local, loc)
		}
	}
	if len(local) == 0 {
		return Observation{}
	}
	where := local[0]
	for _, loc := range local {
		if loc.Space == owned.Placement.Space && loc.Tab == owned.Placement.Tab {
			where = loc
			break
		}
	}
	return Observation{
		PanePresent: true,
		AgentListed: where.Status != "",
		Where:       where,
		Rearranged:  where.Space != owned.Placement.Space || where.Tab != owned.Placement.Tab,
	}
}

// NameSpace names the owned terminal's space and reads the name back.
func (a *App) NameSpace(ctx context.Context, owned Owned, name string) error {
	if name == "" {
		return errors.New("herdrapp: a space name is required")
	}
	if err := a.requireOwnedContainer(ctx, owned); err != nil {
		return err
	}
	if _, err := a.do(ctx, "space.rename", map[string]string{"id": owned.Placement.Space, "name": name}); err != nil {
		return err
	}
	layout, err := a.List(ctx)
	if err != nil {
		return err
	}
	space, ok := layout.space(owned.Placement.Space)
	if !ok || space.Name != name {
		return fmt.Errorf("%w: space %s reads %q, want %q", ErrNotApplied, owned.Placement.Space, space.Name, name)
	}
	return nil
}

// NameTab names the owned terminal's tab and reads the name back.
func (a *App) NameTab(ctx context.Context, owned Owned, name string) error {
	if name == "" {
		return errors.New("herdrapp: a tab name is required")
	}
	if err := a.requireOwnedContainer(ctx, owned); err != nil {
		return err
	}
	if _, err := a.do(ctx, "tab.rename", map[string]string{"id": owned.Placement.Tab, "name": name}); err != nil {
		return err
	}
	layout, err := a.List(ctx)
	if err != nil {
		return err
	}
	_, tab, ok := layout.tab(owned.Placement.Tab)
	if !ok || tab.Name != name {
		return fmt.Errorf("%w: tab %s reads %q, want %q", ErrNotApplied, owned.Placement.Tab, tab.Name, name)
	}
	return nil
}

// requireOwnedContainer confirms the window shows the owned space, locally,
// with the owned tab in it, before a command names either.
func (a *App) requireOwnedContainer(ctx context.Context, owned Owned) error {
	if err := owned.Placement.valid(); err != nil {
		return err
	}
	layout, err := a.List(ctx)
	if err != nil {
		return err
	}
	space, ok := layout.space(owned.Placement.Space)
	if !ok {
		return fmt.Errorf("%w: the window shows no space %s", ErrNotOwned, owned.Placement.Space)
	}
	if !space.Local() {
		return fmt.Errorf("%w: space %s is on another machine", ErrNotOwned, owned.Placement.Space)
	}
	if !slices.ContainsFunc(space.Tabs, func(t Tab) bool { return t.ID == owned.Placement.Tab }) {
		return fmt.Errorf("%w: space %s has no tab %s", ErrNotOwned, owned.Placement.Space, owned.Placement.Tab)
	}
	return nil
}

// Show brings an owned terminal to the front: agent.focus for a listed
// agent, pane.focus for a pane whose program reports no status yet. It reads
// back that the terminal's space and tab are the active ones.
func (a *App) Show(ctx context.Context, owned Owned) (Observation, error) {
	before, err := a.Observe(ctx, owned)
	if err != nil {
		return before, err
	}
	if before.AppClosed {
		return before, fmt.Errorf("%w for app home %s", ErrAppClosed, a.Home)
	}
	if !before.PanePresent {
		return before, fmt.Errorf("%w: the window shows no pane for %s", ErrNotApplied, owned.Terminal)
	}
	command := "pane.focus"
	if before.AgentListed {
		command = "agent.focus"
	}
	if _, err := a.do(ctx, command, map[string]string{"terminal": owned.Terminal}); err != nil {
		return before, err
	}
	layout, err := a.List(ctx)
	if err != nil {
		return before, err
	}
	after := observe(layout, owned)
	if !after.PanePresent {
		return after, fmt.Errorf("%w: %s left the window", ErrNotApplied, owned.Terminal)
	}
	space, tab, ok := layout.tab(after.Where.Tab)
	if !ok || !space.Active || !tab.Active {
		return after, fmt.Errorf("%w: %s did not bring %s to the front", ErrNotApplied, command, owned.Terminal)
	}
	return after, nil
}

// Parked reports whether labels read back from core have lost the
// placement while the terminal is still there: what terminal.park leaves
// behind. A parked execution is missing; it is not re-adopted
// automatically.
func Parked(read map[string]string, placement PlacementIDs) bool {
	return read[LabelSpace] != placement.Space || read[LabelTab] != placement.Tab
}
