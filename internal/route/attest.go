package route

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/sys"
)

// Caller is who asked for a route's start. The daemon fills PID, Started, TTY
// and HarnessAncestor from the system and the socket's peer, never from the
// request; Pane and Confirmed are the caller's word, and are checked. It lives only as long as the daemon holds it: a restart forgets it, and the
// pair then waits for a new call with a fresh attestation.
type Caller struct {
	Pane string
	PID  int
	// Started is the caller process's start time.
	Started time.Time
	TTY     string
	// HarnessAncestor names a coding agent found among the caller's ancestors.
	HarnessAncestor string
	// Confirmed is the checksum a person confirmed on the terminal, for
	// --pair manual.
	Confirmed string
}

// Callers remembers the caller of each route start, by the start's key.
type Callers struct {
	mu sync.Mutex
	m  map[string]Caller
}

// NewCallers makes an empty table.
func NewCallers() *Callers { return &Callers{m: map[string]Caller{}} }

// Set records the caller of a key.
func (c *Callers) Set(key string, caller Caller) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = caller
}

// Get returns the caller of a key.
func (c *Callers) Get(key string) (Caller, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	caller, ok := c.m[key]
	return caller, ok
}

type paneProcessInfo struct {
	TTY                 *string `json:"tty"`
	ForegroundProcesses []struct {
		PID int `json:"pid"`
	} `json:"foreground_processes"`
}

// attest decides whether the caller may approve a pair. It asks herdr fresh
// each time. What a pass proves: the request came from this pane, no agent is
// attached to it, and no recognized harness is among the caller's ancestors.
// Nothing here identifies the person behind the pane.
func attest(ctx context.Context, c *herdr.Client, caller Caller) error {
	if err := verifyCaller(caller); err != nil {
		return err
	}
	pane := caller.Pane
	if pane == "" {
		found, err := paneOfTerminal(ctx, c, caller.TTY)
		if err != nil {
			return err
		}
		if found == "" {
			return nil // a terminal outside herdr
		}
		if _, err := c.AgentGet(ctx, found); err == nil {
			return fmt.Errorf("an agent is attached to pane %s, the caller's terminal", found)
		} else if !isNotFound(err) {
			return fmt.Errorf("herdr cannot attest pane %s: %w", found, err)
		}
		return nil
	}
	info, err := processInfo(ctx, c, pane)
	if err != nil {
		return fmt.Errorf("herdr cannot attest pane %s: %w", pane, err)
	}
	pids := make([]int, 0, len(info.ForegroundProcesses))
	for _, p := range info.ForegroundProcesses {
		pids = append(pids, p.PID)
	}
	if !slices.Contains(pids, caller.PID) {
		return fmt.Errorf("process %d is not in pane %s", caller.PID, pane)
	}
	if _, err := c.AgentGet(ctx, pane); err == nil {
		return fmt.Errorf("an agent is attached to pane %s", pane)
	} else if !isNotFound(err) {
		return fmt.Errorf("herdr cannot attest pane %s: %w", pane, err)
	}
	return nil
}

// verifyCaller holds for a caller whose facts the daemon read from the
// system, and whose process is still the one that asked.
func verifyCaller(caller Caller) error {
	if caller.HarnessAncestor != "" {
		return fmt.Errorf("up runs under the harness %s", caller.HarnessAncestor)
	}
	if caller.TTY == "" {
		return errors.New("up runs without a controlling terminal")
	}
	if caller.Started.IsZero() {
		return fmt.Errorf("process %d has no verified start time to attest", caller.PID)
	}
	running, err := sys.Identify(caller.PID)
	if err != nil {
		return fmt.Errorf("process %d cannot be identified: %w", caller.PID, err)
	}
	if !running.Same(sys.Process{PID: caller.PID, Start: caller.Started}) {
		return fmt.Errorf("process %d is not the one that asked", caller.PID)
	}
	return nil
}

func processInfo(ctx context.Context, c *herdr.Client, pane string) (paneProcessInfo, error) {
	var r struct {
		ProcessInfo paneProcessInfo `json:"process_info"`
	}
	err := c.Call(ctx, "pane.process_info", map[string]any{"pane_id": pane}, &r)
	return r.ProcessInfo, err
}

// paneOfTerminal finds the pane that owns a terminal, so a caller inside herdr
// cannot pass for one outside it by leaving out its pane.
func paneOfTerminal(ctx context.Context, c *herdr.Client, tty string) (string, error) {
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return "", fmt.Errorf("herdr cannot attest the terminal: %w", err)
	}
	for _, p := range snapshot.Panes {
		info, err := processInfo(ctx, c, p.ID)
		if err != nil {
			continue // a pane closing now holds no caller
		}
		if info.TTY != nil && *info.TTY == tty {
			return p.ID, nil
		}
	}
	return "", nil
}
