package coreview

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hclive"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "launch" && os.Args[2] == "--view" {
		if err := Run(context.Background(), os.Args[3], os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func privateTUI(t *testing.T) (string, *herdr.Client) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "hsv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	env := []string{"HOME=" + root, "PATH=" + os.Getenv("PATH"), "SHELL=/bin/sh", "TERM=xterm-256color", "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_RUNTIME_DIR=" + filepath.Join(root, "run")}
	if err := os.MkdirAll(filepath.Join(root, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(root, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("herdr", "--session", "view-test", "server")
	cmd.Env = env
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = log.Close() })
	socket := filepath.Join(root, "config", "herdr", "sessions", "view-test", "herdr.sock")
	c := herdr.New(socket)
	eventually(t, func() bool { _, err := c.Check(context.Background()); return err == nil })
	return socket, c
}

func eventually(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(12 * time.Second)
	for time.Now().Before(until) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition did not settle")
}

func TestLiveOptionalTUIView(t *testing.T) {
	real := hclive.Start(t, hclive.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := hc.Dial(ctx, real.Socket, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	terminal, err := c.Run(ctx, hc.RunRequest{Argv: []string{"/bin/sh", "-c", "printf 'private core view fixture\\n'; exec sleep 120"}, Cwd: real.Dir, Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = c.Kill(context.Background(), terminal.Address) })
	if terminal.PID == nil {
		t.Fatal("missing pid")
	}
	ev, err := core.Evidence(core.SystemProcs{}, int(*terminal.PID))
	if err != nil {
		t.Fatal(err)
	}
	e := runtime.Execution{ID: runtime.NewExecutionID(), Backend: runtime.BackendCore, Agent: "view-fixture", HarnessKind: "fixture", EndpointID: c.Core().ID, TerminalAddress: terminal.Address, Evidence: ev, Cwd: real.Dir, Desired: runtime.DesiredRunning, Observation: runtime.Observation{Program: runtime.ProgramWorking}}
	socket, tui := privateTUI(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Root: filepath.Join(real.Dir, "views"), Helper: self}
	// Simulate a daemon crash after workspace creation but before its reply
	// was persisted. A unique durable intent must adopt the existing pane.
	key, err := session(socket)
	if err != nil {
		t.Fatal(err)
	}
	intent := Entry{Execution: e, CoreSocket: real.Socket, CoreBinary: real.Bin, Socket: socket, Session: key, Label: "crash-" + e.ID, Phase: "creating", Helper: self}
	if err := write(m.path(e.ID), intent); err != nil {
		t.Fatal(err)
	}
	if err := tui.Call(ctx, "workspace.create", map[string]any{"cwd": e.Cwd, "label": intent.Label, "focus": false}, nil); err != nil {
		t.Fatal(err)
	}
	binding, err := m.Ensure(ctx, e, socket, real.Socket, real.Bin, false)
	if err != nil {
		t.Fatal(err)
	}
	var entry Entry
	if err := read(m.path(e.ID), &entry); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			if snap, err := tui.Snapshot(context.Background()); err == nil {
				for _, p := range snap.Panes {
					out, _ := tui.ReadPane(context.Background(), p.ID, herdr.ReadOptions{Lines: 40})
					t.Log(out.Text)
				}
			}
		}
	})
	eventually(t, func() bool {
		var state helperState
		return read(m.path(e.ID)+".helper", &state) == nil && !state.Detached && state.Seq > 0
	})
	// A new manager (daemon restart) adopts the same immutable TUI terminal.
	restarted := &Manager{Root: m.Root, Helper: self}
	again, err := restarted.Ensure(ctx, e, socket, real.Socket, real.Bin, false)
	if err != nil || again.Pane != binding.Pane {
		t.Fatalf("restart: %+v %v", again, err)
	}
	// A lost helper-launch reply cannot paste into the now-running helper.
	if err := write(m.path(e.ID), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Ensure(ctx, e, socket, real.Socket, real.Bin, false); err == nil {
		t.Fatal("replayed helper launch into an occupied pane")
	}
	var collision struct {
		Root herdr.Pane `json:"root_pane"`
	}
	if err := tui.Call(ctx, "workspace.create", map[string]any{"cwd": e.Cwd, "label": intent.Label, "focus": false}, &collision); err != nil {
		t.Fatal(err)
	}
	if err := write(m.path(e.ID), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Ensure(ctx, e, socket, real.Socket, real.Bin, false); err == nil || !strings.Contains(err.Error(), "uncertain mirror creation") {
		t.Fatalf("ambiguous creation was adopted: %v", err)
	}
	if err := tui.Call(ctx, "pane.close", map[string]any{"pane_id": collision.Root.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := write(m.path(e.ID), entry); err != nil {
		t.Fatal(err)
	}
	// A move changes public location, not terminal identity or attachment.
	if err := tui.Call(ctx, "pane.move", map[string]any{"pane_id": binding.Pane, "destination": map[string]any{"type": "new_workspace", "label": "moved view"}}, nil); err != nil {
		t.Fatal(err)
	}
	again, err = restarted.Ensure(ctx, e, socket, real.Socket, real.Bin, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := findPane(ctx, tui, entry)
	if err != nil || p.ID != again.Pane {
		t.Fatalf("moved: %+v %v", p, err)
	}
	// Reports use a custom label only, with no native harness session identity.
	eventually(t, func() bool {
		snap, err := tui.Snapshot(ctx)
		if err != nil {
			return false
		}
		for _, p := range snap.Panes {
			if p.TerminalID == entry.Terminal && p.DisplayTitle != nil {
				return strings.Contains(*p.DisplayTitle, "working")
			}
		}
		return false
	})
	// A user closing the mirror cannot kill or re-create the core execution.
	if err := tui.Call(ctx, "pane.close", map[string]any{"pane_id": again.Pane}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := core.Proven(ctx, c, core.SystemProcs{}, e); err != nil || !ok {
		t.Fatalf("mirror close ended core: %v", err)
	}
	if _, err := restarted.Ensure(ctx, e, socket, real.Socket, real.Bin, false); err == nil {
		t.Fatal("automatic recreation after close")
	}
	eventually(t, func() bool {
		var s helperState
		if read(m.path(e.ID)+".helper", &s) != nil {
			return false
		}
		alive, _ := core.Alive(core.SystemProcs{}, s.Evidence)
		return !alive
	})
	fresh, err := restarted.Ensure(ctx, e, socket, real.Socket, real.Bin, true)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Pane == again.Pane {
		t.Fatal("explicit show reused the closed pane")
	}
	var freshState helperState
	eventually(t, func() bool {
		return read(m.path(e.ID)+".helper", &freshState) == nil && !freshState.Detached && freshState.Seq > 0
	})
	// Lease expiry removes display state even if the daemon never returns.
	if err := write(m.path(e.ID)+".lease", heartbeat{Until: time.Now().Add(-time.Second), State: "working"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { var s helperState; return read(m.path(e.ID)+".helper", &s) == nil && s.Detached })
	if _, ok, err := core.Proven(ctx, c, core.SystemProcs{}, e); err != nil || !ok {
		t.Fatalf("expired view lease ended core: %v", err)
	}
	eventually(t, func() bool {
		snap, err := tui.Snapshot(ctx)
		if err != nil {
			return false
		}
		for _, p := range snap.Panes {
			if p.ID == fresh.Pane {
				return p.DisplayTitle == nil || !strings.Contains(*p.DisplayTitle, "working")
			}
		}
		return false
	})
	// A hard helper crash also expires metadata without a cleanup handler.
	eventually(t, func() bool { alive, _ := core.Alive(core.SystemProcs{}, freshState.Evidence); return !alive })
	crashed, err := restarted.Ensure(ctx, e, socket, real.Socket, real.Bin, true)
	if err != nil {
		t.Fatal(err)
	}
	var crashState helperState
	eventually(t, func() bool {
		return read(m.path(e.ID)+".helper", &crashState) == nil && !crashState.Detached && crashState.Seq > freshState.Seq
	})
	eventually(t, func() bool {
		snap, err := tui.Snapshot(ctx)
		if err != nil {
			return false
		}
		for _, p := range snap.Panes {
			if p.ID == crashed.Pane && p.DisplayTitle != nil {
				return strings.Contains(*p.DisplayTitle, "working")
			}
		}
		return false
	})
	proc, err := os.FindProcess(crashState.Evidence.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		snap, err := tui.Snapshot(ctx)
		if err != nil {
			return false
		}
		for _, p := range snap.Panes {
			if p.ID == crashed.Pane {
				return p.DisplayTitle == nil || !strings.Contains(*p.DisplayTitle, "working")
			}
		}
		return false
	})
	if _, ok, err := core.Proven(ctx, c, core.SystemProcs{}, e); err != nil || !ok {
		t.Fatalf("helper crash ended core: %v", err)
	}
	if err := tui.Call(ctx, "pane.close", map[string]any{"pane_id": crashed.Pane}, nil); err != nil {
		t.Fatal(err)
	}
}
