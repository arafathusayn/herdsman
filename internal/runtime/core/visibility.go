package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Bindings keeps verified app-to-core bindings, one per app home and core.
type Bindings interface {
	Load(appHome, coreID string) (herdrapp.Binding, bool, error)
	Save(b herdrapp.Binding) error
}

// FileBindings keeps bindings in one private JSON file.
type FileBindings struct {
	Path string
	mu   sync.Mutex
}

func bindingKey(appHome, coreID string) string { return filepath.Clean(appHome) + "\x00" + coreID }

func (f *FileBindings) read() (map[string]herdrapp.Binding, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]herdrapp.Binding{}, nil
	}
	if err != nil {
		return nil, err
	}
	var list []herdrapp.Binding
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("core: app bindings: %w", err)
	}
	out := map[string]herdrapp.Binding{}
	for _, x := range list {
		out[bindingKey(x.AppHome, x.CoreID)] = x
	}
	return out, nil
}

// Load implements Bindings.
func (f *FileBindings) Load(appHome, coreID string) (herdrapp.Binding, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.read()
	if err != nil {
		return herdrapp.Binding{}, false, err
	}
	b, ok := all[bindingKey(appHome, coreID)]
	return b, ok, nil
}

// Save implements Bindings.
func (f *FileBindings) Save(b herdrapp.Binding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.read()
	if err != nil {
		return err
	}
	all[bindingKey(b.AppHome, b.CoreID)] = b
	list := make([]herdrapp.Binding, 0, len(all))
	for _, x := range all {
		list = append(list, x)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// coreLabels adapts a terminal reader to herdrapp.CoreLabels.
type coreLabels struct{ c TerminalReader }

func (l coreLabels) TerminalLabels(ctx context.Context, address string) (map[string]string, error) {
	t, err := l.c.Get(ctx, address)
	if err != nil {
		return nil, err
	}
	return t.Labels, nil
}

// VisibilityInput is one execution's visibility check.
type VisibilityInput struct {
	// App is the configured app; nil when none is installed, which reads as
	// a closed window.
	App      *herdrapp.App
	Bindings Bindings
	Core     herdrapp.Core
	Launch   Launch
	Address  string
	Previous runtime.Visibility
	// Timeout is visibility_timeout: how long the window may take to show a
	// placement before it is missing (default 15 s).
	Timeout time.Duration
	Poll    time.Duration
	Now     func() time.Time
	// SpaceName and TabName name the owned containers once shown.
	SpaceName, TabName string
}

// VisibilityResult is the execution's visibility after the check.
type VisibilityResult struct {
	State     herdrapp.Visibility
	AppClosed bool
	// Owned is set once the launch labels were read back from core.
	Owned   *herdrapp.Owned
	Where   herdrapp.Location
	Binding *herdrapp.Binding
	// Note says what a person may want to know: a naming that did not
	// apply, or why the window could not be read.
	Note string
}

// AppBinding is the result as the execution record keeps it.
func (r VisibilityResult) AppBinding(coreID string) *runtime.AppBinding {
	b := &runtime.AppBinding{CoreID: coreID, Visibility: runtime.Visibility(r.State)}
	if r.Owned != nil {
		b.SpaceID, b.TabID = r.Owned.Placement.Space, r.Owned.Placement.Tab
	}
	if r.Where.Pane != "" {
		b.SpaceID, b.TabID, b.PaneID = r.Where.Space, r.Where.Tab, r.Where.Pane
	}
	return b
}

func (in *VisibilityInput) defaults() {
	if in.Timeout == 0 {
		in.Timeout = 15 * time.Second
	}
	if in.Poll == 0 {
		in.Poll = 250 * time.Millisecond
	}
	if in.Now == nil {
		in.Now = time.Now
	}
}

// EstablishVisibility runs section 7 for one execution: the labels read back
// from core, the binding checked or verified with this route's own placement
// as the challenge, the window observed, and the owned containers named.
// Writing labels or a live terminal reach no state; only read-back facts do.
// It returns unverified when no binding was ever verified and the window
// cannot verify one now: the launch then waits for visibility.
func EstablishVisibility(ctx context.Context, c TerminalReader, in VisibilityInput) (VisibilityResult, error) {
	in.defaults()
	labels := coreLabels{c}
	read, err := labels.TerminalLabels(ctx, in.Address)
	if err != nil {
		return VisibilityResult{State: herdrapp.Unverified}, fmt.Errorf("read the labels of %s back from core: %w", in.Address, err)
	}
	owned, err := herdrapp.OwnedFromCore(in.Address, read, in.Launch.Labels())
	parked := herdrapp.Parked(read, in.Launch.Placement)
	if err != nil && !parked {
		return VisibilityResult{State: herdrapp.Unverified}, err
	}
	var out VisibilityResult
	if err == nil {
		out.Owned = &owned
	} else {
		owned = herdrapp.Owned{Terminal: in.Address, Execution: in.Launch.Execution, Placement: in.Launch.Placement}
	}
	bindingValid := false
	if in.Bindings != nil && in.App != nil {
		if b, ok, err := in.Bindings.Load(in.App.Home, in.Core.ID); err == nil && ok && b.Valid(in.Core, in.App.Home) == nil {
			bindingValid, out.Binding = true, &b
		}
	}
	deadline := in.Now().Add(in.Timeout)
	for {
		var obs herdrapp.Observation
		if in.App == nil {
			obs.AppClosed = true
		} else if obs, err = in.App.Observe(ctx, owned); err != nil {
			// An app that cannot run is no window: nothing about placement
			// is known, and nothing is lost.
			obs, out.Note = herdrapp.Observation{AppClosed: true}, err.Error()
		}
		if !bindingValid && obs.PanePresent && !parked && out.Owned != nil {
			b, verr := herdrapp.Verify(ctx, in.App, in.Core, labels, in.Launch.Placement, in.Address, in.Now())
			switch {
			case verr == nil:
				if in.Bindings != nil {
					if err := in.Bindings.Save(b); err != nil {
						return out, err
					}
				}
				bindingValid, out.Binding = true, &b
			case errors.Is(verr, herdrapp.ErrEndpointMismatch):
				return VisibilityResult{State: herdrapp.Unverified}, refuse(CodeEndpointMismatch, "the app at %s is not bound to core %s: %v", in.App.Home, in.Core.ID, verr)
			}
		}
		timedOut := !in.Now().Before(deadline)
		rep := herdrapp.Next(herdrapp.Visibility(in.Previous), herdrapp.Facts{
			BindingValid: bindingValid, LabelsReadBack: out.Owned != nil || parked, Observation: obs, Parked: parked, TimedOut: timedOut,
		})
		out.State, out.AppClosed, out.Where = rep.State, rep.AppClosed, obs.Where
		// An open window gets until the timeout to adopt the placement; a
		// closed one is not waited for.
		if rep.State == herdrapp.Verified || rep.State == herdrapp.Missing || obs.AppClosed || timedOut {
			break
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(in.Poll):
		}
	}
	if out.State == herdrapp.Verified && out.Owned != nil {
		if in.SpaceName != "" {
			if err := in.App.NameSpace(ctx, owned, in.SpaceName); err != nil {
				out.Note = "the space was not named: " + err.Error()
			}
		}
		if in.TabName != "" {
			if err := in.App.NameTab(ctx, owned, in.TabName); err != nil {
				out.Note = "the tab was not named: " + err.Error()
			}
		}
	}
	return out, nil
}
