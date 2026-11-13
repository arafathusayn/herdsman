package herdrapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Core is the selected herdr-core: its hello.core.id and the canonical
// endpoint (socket and session) it was reached at.
type Core struct {
	ID       string
	Endpoint string
}

// CoreLabels reads one terminal's labels back from the selected core. The
// caller supplies it, so this package needs no core client.
type CoreLabels interface {
	TerminalLabels(ctx context.Context, address string) (map[string]string, error)
}

// Binding is a verified link between one app home and one core. It is
// persisted and reused until Valid refuses it.
type Binding struct {
	CoreID     string    `json:"core_id"`
	Endpoint   string    `json:"endpoint"`
	AppHome    string    `json:"app_home"`
	AppHomeID  string    `json:"app_home_id"`
	AppVersion string    `json:"app_version"`
	VerifiedAt time.Time `json:"verified_at"`
}

var (
	// ErrChallengeNotOnCore means the selected core does not carry the
	// challenge labels on the challenge terminal.
	ErrChallengeNotOnCore = errors.New("herdrapp: challenge labels are not on the selected core")
	// ErrChallengeNotShown means the window does not show the challenge
	// placement, yet. Adoption is asynchronous, so a caller may retry until
	// its visibility timeout.
	ErrChallengeNotShown = errors.New("herdrapp: the app does not show the challenge placement")
	// ErrEndpointMismatch means the window shows the challenge, but not as
	// the challenge terminal on this computer's core.
	ErrEndpointMismatch = errors.New("herdrapp: the app is not bound to the selected core")
	// ErrBindingStale means a persisted binding no longer describes the
	// configured app and core.
	ErrBindingStale = errors.New("herdrapp: app binding must be verified again")
)

// Verify runs the binding challenge. The caller allocated challenge with
// NewPlacementIDs, wrote it as herdr.space and herdr.tab on the terminal at
// address on the selected core, and on no other core. Verify then requires
// the core to read those labels back and the configured app's window to
// show exactly that placement holding exactly that terminal, as local
// (machine:null). A matching address or display name alone never verifies:
// only the fresh random ids do.
func Verify(ctx context.Context, app *App, core Core, labels CoreLabels, challenge PlacementIDs, address string, now time.Time) (Binding, error) {
	if core.ID == "" || core.Endpoint == "" {
		return Binding{}, errors.New("herdrapp: the core id and endpoint are required")
	}
	if err := challenge.valid(); err != nil {
		return Binding{}, err
	}
	version, err := app.Check(ctx)
	if err != nil {
		return Binding{}, err
	}
	read, err := labels.TerminalLabels(ctx, address)
	if err != nil {
		return Binding{}, fmt.Errorf("herdrapp: read challenge labels from core %s: %w", core.ID, err)
	}
	if read[LabelSpace] != challenge.Space || read[LabelTab] != challenge.Tab {
		return Binding{}, fmt.Errorf("%w: %s on core %s", ErrChallengeNotOnCore, address, core.ID)
	}
	layout, err := app.List(ctx)
	if err != nil {
		return Binding{}, err
	}
	if err := matchChallenge(layout, challenge, address); err != nil {
		return Binding{}, err
	}
	homeID, err := HomeIdentity(app.Home)
	if err != nil {
		return Binding{}, err
	}
	return Binding{
		CoreID: core.ID, Endpoint: core.Endpoint,
		AppHome: app.Home, AppHomeID: homeID, AppVersion: version,
		VerifiedAt: now.UTC(),
	}, nil
}

// matchChallenge requires exactly one space with the challenge id, local,
// holding the challenge tab, which shows the challenge terminal.
func matchChallenge(layout Layout, challenge PlacementIDs, address string) error {
	var spaces []Space
	for _, s := range layout.Spaces {
		if s.ID == challenge.Space || strings.HasSuffix(s.ID, "/"+challenge.Space) {
			spaces = append(spaces, s)
		}
	}
	switch {
	case len(spaces) == 0:
		return ErrChallengeNotShown
	case len(spaces) > 1:
		return fmt.Errorf("%w: the challenge space is shown %d times", ErrEndpointMismatch, len(spaces))
	}
	space := spaces[0]
	if !space.Local() {
		machine := "another machine"
		if space.Machine != nil {
			machine = "machine " + *space.Machine
		}
		return fmt.Errorf("%w: the challenge is shown on %s, not this computer", ErrEndpointMismatch, machine)
	}
	for _, tab := range space.Tabs {
		if tab.ID != challenge.Tab {
			continue
		}
		for _, t := range tab.Terminals {
			if t.Terminal == address {
				return nil
			}
		}
		return fmt.Errorf("%w: the challenge tab does not show terminal %s", ErrEndpointMismatch, address)
	}
	return fmt.Errorf("%w: the challenge space has no challenge tab", ErrEndpointMismatch)
}

// Valid refuses a binding whose core, endpoint or app home changed, or
// whose app home is a copy (a different directory at the same path).
func (b Binding) Valid(core Core, appHome string) error {
	switch {
	case b.CoreID == "" || b.VerifiedAt.IsZero():
		return fmt.Errorf("%w: none recorded", ErrBindingStale)
	case b.CoreID != core.ID:
		return fmt.Errorf("%w: core %s is not the bound core %s", ErrBindingStale, core.ID, b.CoreID)
	case b.Endpoint != core.Endpoint:
		return fmt.Errorf("%w: endpoint changed", ErrBindingStale)
	case filepath.Clean(b.AppHome) != filepath.Clean(appHome):
		return fmt.Errorf("%w: app home changed", ErrBindingStale)
	}
	id, err := HomeIdentity(appHome)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBindingStale, err)
	}
	if id != b.AppHomeID {
		return fmt.Errorf("%w: app home was replaced or copied", ErrBindingStale)
	}
	return nil
}

// HomeIdentity names the app home directory by device and inode, so a copy
// put at the same path reads as a different home.
func HomeIdentity(home string) (string, error) {
	info, err := os.Stat(home)
	if err != nil {
		return "", fmt.Errorf("herdrapp: app home: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("herdrapp: app home %s is not a directory", home)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("herdrapp: app home identity is not available on this platform")
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), nil
}

// Visibility is an execution's place in the window, one of four states.
type Visibility string

const (
	// Unverified: no binding verified for this app home and core, or the
	// placement labels have not been read back from core. Dispatch is held.
	Unverified Visibility = "unverified"
	// Pending: binding verified, labels read back, the window not
	// confirming the pane now. Dispatch is allowed.
	Pending Visibility = "pending"
	// Verified: the window shows the pane. Dispatch is allowed.
	Verified Visibility = "verified"
	// Missing: the window is open and does not show the pane past the
	// visibility timeout, or the person parked it. Dispatch is held.
	Missing Visibility = "missing"
)

// DispatchAllowed reports whether a new notice may be claimed in this state.
func (v Visibility) DispatchAllowed() bool { return v == Pending || v == Verified }

// Facts are what one reconcile knows about an execution.
type Facts struct {
	// BindingValid: a verified binding exists and Valid accepts it.
	BindingValid bool
	// LabelsReadBack: the placement and ownership labels were read back from
	// the bound core and are in the persisted record.
	LabelsReadBack bool
	// Observation: this reconcile's read of the window.
	Observation Observation
	// Parked: core shows the placement labels gone while the process lives
	// (terminal.park, or the person removed the pane's membership).
	Parked bool
	// TimedOut: visibility_timeout has passed since the labels were read
	// back, or since the window became reachable again.
	TimedOut bool
}

// Report is the outcome of one reconcile: the state and, separately,
// whether the window was closed. app_closed is never a state of its own.
type Report struct {
	State     Visibility
	AppClosed bool
}

// Next applies the transition rules. Writing labels, a pending:true reply or
// a live core terminal reach none of the states; only read-back facts do.
func Next(prev Visibility, f Facts) Report {
	closed := f.Observation.AppClosed
	switch {
	case !f.BindingValid || !f.LabelsReadBack:
		return Report{State: Unverified, AppClosed: closed}
	case f.Parked:
		return Report{State: Missing, AppClosed: closed}
	case closed:
		// A closed window loses nothing: a missing execution stays missing
		// until the window shows it, and every other one waits as pending.
		if prev == Missing {
			return Report{State: Missing, AppClosed: true}
		}
		return Report{State: Pending, AppClosed: true}
	case f.Observation.PanePresent:
		return Report{State: Verified}
	case prev == Verified, prev == Missing, f.TimedOut:
		// Shown before and gone now, or never shown in time: membership is
		// lost and stays lost until the window shows the pane again.
		return Report{State: Missing}
	default:
		return Report{State: Pending}
	}
}
