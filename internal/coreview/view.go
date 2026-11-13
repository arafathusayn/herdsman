// Package coreview provides an optional, display-only Herdr TUI attachment.
// The native app remains the visibility authority. A mirror owns no execution.
package coreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

const Lease = 15 * time.Second

// Entry is the durable creation intent and immutable binding. No prompt,
// credentials, or harness conversation identity belongs in a mirror.
type Entry struct {
	Execution  runtime.Execution `json:"execution"`
	CoreSocket string            `json:"core_socket"`
	CoreBinary string            `json:"core_binary"`
	Socket     string            `json:"socket"`
	Session    string            `json:"session"`
	Label      string            `json:"label"`
	Terminal   string            `json:"terminal,omitempty"`
	Pane       string            `json:"pane,omitempty"`
	Phase      string            `json:"phase"`
	Helper     string            `json:"helper"`
}

type heartbeat struct {
	Until time.Time `json:"until"`
	State string    `json:"state"`
}
type helperState struct {
	Evidence runtime.ProcessEvidence `json:"evidence"`
	Detached bool                    `json:"detached"`
	Seq      uint64                  `json:"seq"`
}

// Manager serializes creation. An uncertain create or helper launch is never
// replayed; snapshots recover a unique intent, and Show permits a new attempt.
type Manager struct {
	Root, Helper string
	mu           sync.Mutex
}

func (m *Manager) path(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(m.Root, hex.EncodeToString(sum[:])+".json")
}

func read(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() > 64<<10 {
		return errors.New("invalid mirror record")
	}
	return json.NewDecoder(f).Decode(out)
}

func write(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".mirror-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// session pins this socket incarnation, independently of reusable pane IDs.
func session(socket string) (string, error) {
	st, err := os.Stat(socket)
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return "", errors.New("mirror endpoint is not a socket")
	}
	return st.ModTime().UTC().Format(time.RFC3339Nano), nil
}

func findPane(ctx context.Context, c *herdr.Client, e Entry) (herdr.Pane, error) {
	key, err := session(e.Socket)
	if err != nil || key != e.Session {
		return herdr.Pane{}, errors.New("mirror session changed")
	}
	snap, err := c.Snapshot(ctx)
	if err != nil {
		return herdr.Pane{}, err
	}
	var found []herdr.Pane
	for _, p := range snap.Panes {
		if p.TerminalID == e.Terminal {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		return herdr.Pane{}, errors.New("mirror terminal is missing or ambiguous")
	}
	return found[0], nil
}

// Ensure maintains a lease or creates one dedicated mirror. Explicit Show
// may recreate a closed view; a periodic pass never does.
func (m *Manager) Ensure(ctx context.Context, e runtime.Execution, socket, coreSocket, coreBinary string, show bool) (*runtime.TUIBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !filepath.IsAbs(m.Helper) {
		return nil, errors.New("mirror helper must be an absolute executable path")
	}
	if e.Backend != runtime.BackendCore || e.Evidence.PID <= 0 || e.Desired != runtime.DesiredRunning {
		return nil, errors.New("mirror requires a running owned core execution")
	}
	if err := prove(ctx, Entry{Execution: e, CoreSocket: coreSocket}); err != nil {
		return nil, err
	}
	path := m.path(e.ID)
	var entry Entry
	err := read(path, &entry)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	c := herdr.New(socket)
	if _, err := c.Check(ctx); err != nil {
		return nil, err
	}
	if err == nil {
		if entry.Socket != socket || entry.CoreSocket != coreSocket || entry.Execution.EndpointID != e.EndpointID || entry.Execution.Evidence != e.Evidence {
			return nil, errors.New("mirror binding changed")
		}
		if entry.Terminal != "" {
			pane, paneErr := findPane(ctx, c, entry)
			var state helperState
			stateErr := read(path+".helper", &state)
			alive, _ := core.Alive(core.SystemProcs{}, state.Evidence)
			if paneErr == nil && stateErr == nil && alive && !state.Detached {
				if err := write(path+".lease", heartbeat{Until: time.Now().Add(Lease), State: string(e.Observation.Program)}); err != nil {
					return nil, err
				}
				if entry.Pane != pane.ID {
					entry.Pane = pane.ID
					if err := write(path, entry); err != nil {
						return nil, err
					}
				}
				return &runtime.TUIBinding{Socket: socket, Pane: pane.ID}, nil
			}
			if !show {
				return nil, errors.New("mirror detached; runtime show reattaches it")
			}
			// A slow startup is not permission to launch a second helper.
			if stateErr == nil && alive {
				return nil, errors.New("mirror helper is still running")
			}
			entry = Entry{}
		} else if entry.Phase != "creating" {
			return nil, errors.New("mirror creation needs attention")
		}
	}
	if entry.Label == "" {
		key, err := session(socket)
		if err != nil {
			return nil, err
		}
		clean := e
		clean.Conversation = nil
		clean.App = nil
		clean.TUI = nil
		entry = Entry{Execution: clean, Socket: socket, CoreSocket: coreSocket, CoreBinary: coreBinary, Session: key, Label: "herdsman-core-" + runtime.NewExecutionID(), Phase: "creating", Helper: m.Helper}
		if err := write(path, entry); err != nil {
			return nil, err
		}
		// The intent is durable before the non-idempotent create.
		var result struct {
			Root herdr.Pane `json:"root_pane"`
		}
		if err := c.Call(ctx, "workspace.create", map[string]any{"cwd": e.Cwd, "label": entry.Label, "focus": false}, &result); err != nil {
			return nil, err
		}
		entry.Pane, entry.Terminal = result.Root.ID, result.Root.TerminalID
	} else {
		snap, err := c.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		var matches []herdr.Pane
		for _, w := range snap.Workspaces {
			if w.Label == entry.Label {
				for _, p := range snap.Panes {
					if p.WorkspaceID == w.ID {
						matches = append(matches, p)
					}
				}
			}
		}
		if len(matches) != 1 {
			return nil, errors.New("uncertain mirror creation: expected one dedicated pane")
		}
		entry.Pane, entry.Terminal = matches[0].ID, matches[0].TerminalID
	}
	if entry.Terminal == "" || entry.Pane == "" {
		return nil, errors.New("TUI omitted immutable terminal identity")
	}
	entry.Phase = "starting"
	if err := write(path, entry); err != nil {
		return nil, err
	}
	if err := write(path+".lease", heartbeat{Until: time.Now().Add(Lease), State: string(e.Observation.Program)}); err != nil {
		return nil, err
	}
	if _, err := findPane(ctx, c, entry); err != nil {
		return nil, err
	}
	if err := bootstrapReady(ctx, c, entry.Pane); err != nil {
		return nil, err
	}
	// Bootstrap only the new dedicated shell after checking its foreground;
	// this command never carries a task or targets an existing agent.
	cmd := exec.CommandContext(ctx, "herdr", "pane", "run", entry.Pane, m.Helper, "launch", "--view", path)
	cmd.Env = cleanEnv(os.Environ(), socket)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("mirror helper launch uncertain: %w", err)
	}
	return &runtime.TUIBinding{Socket: socket, Pane: entry.Pane}, nil
}

func cleanEnv(env []string, socket string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, "HERDR_") && !strings.HasPrefix(k, "HC_") {
			out = append(out, kv)
		}
	}
	return append(out, "HERDR_SOCKET_PATH="+socket)
}

// Binding reads the durable view location only while its helper lease is live.
func (m *Manager) Binding(id string) *runtime.TUIBinding {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := m.path(id)
	var e Entry
	var h heartbeat
	var state helperState
	if read(path, &e) != nil || read(path+".lease", &h) != nil || read(path+".helper", &state) != nil || state.Detached || time.Now().After(h.Until) {
		return nil
	}
	if alive, _ := core.Alive(core.SystemProcs{}, state.Evidence); !alive {
		return nil
	}
	return &runtime.TUIBinding{Socket: e.Socket, Pane: e.Pane}
}

// bootstrapReady refuses to paste the helper command into a foreground
// program, including an agent a person started while creation was pending.
func bootstrapReady(ctx context.Context, c *herdr.Client, pane string) error {
	var r struct {
		Info bootstrapInfo `json:"process_info"`
	}
	if err := c.Call(ctx, "pane.process_info", map[string]any{"pane_id": pane}, &r); err != nil {
		return err
	}
	if !r.Info.idleShell() {
		return errors.New("dedicated mirror pane is no longer an idle shell; no input sent")
	}
	return nil
}

type bootstrapInfo struct {
	Shell      int `json:"shell_pid"`
	Group      int `json:"foreground_process_group_id"`
	Foreground []struct {
		PID  int      `json:"pid"`
		Name string   `json:"name"`
		Argv []string `json:"argv"`
	} `json:"foreground_processes"`
}

func (p bootstrapInfo) idleShell() bool {
	if p.Shell <= 0 || p.Group != p.Shell || len(p.Foreground) != 1 || p.Foreground[0].PID != p.Shell {
		return false
	}
	f := p.Foreground[0]
	if len(f.Argv) == 0 {
		return false
	}
	switch f.Name {
	case "sh", "bash", "zsh", "fish", "nu":
	default:
		return false
	}
	for _, arg := range f.Argv[1:] {
		if arg != "-l" && arg != "-i" {
			return false
		}
	}
	return true
}
