package coreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// helperPane verifies both the immutable TUI terminal and its foreground
// process ancestry. Moving a pane is fine; replacing its helper is not.
func helperPane(ctx context.Context, c *herdr.Client, e Entry, pid int) (herdr.Pane, error) {
	pane, err := findPane(ctx, c, e)
	if err != nil {
		return pane, err
	}
	var r struct {
		Info struct {
			Foreground []struct {
				PID int `json:"pid"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	if err := c.Call(ctx, "pane.process_info", map[string]any{"pane_id": pane.ID}, &r); err != nil {
		return pane, err
	}
	for _, p := range r.Info.Foreground {
		for n, id := 0, p.PID; n < 8 && id > 1; n++ {
			if id == pid {
				return pane, nil
			}
			id, err = (core.SystemProcs{}).Parent(id)
			if err != nil {
				break
			}
		}
	}
	return pane, errors.New("mirror helper no longer owns its foreground process")
}

func prove(ctx context.Context, e Entry) error {
	c, err := hc.Dial(ctx, e.CoreSocket, hc.Options{ExpectCore: e.Execution.EndpointID, ConnectTimeout: 2 * time.Second})
	if err != nil {
		return err
	}
	defer c.Close()
	_, ok, err := core.Proven(ctx, c, core.SystemProcs{}, e.Execution)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("core execution identity changed")
	}
	return nil
}

// Run attaches only to the recorded core/address after proving its process.
// Display reports have a short native TTL: daemon death, helper death, core
// loss and reporting failures all expire without a lifecycle override.
func Run(ctx context.Context, path string, stdin io.Reader, stdout, stderr io.Writer) error {
	var e Entry
	if err := read(path, &e); err != nil {
		return err
	}
	if e.Terminal == "" || e.Execution.ID == "" {
		return errors.New("incomplete mirror binding")
	}
	ev, err := core.Evidence(core.SystemProcs{}, os.Getpid())
	if err != nil {
		return err
	}
	state := helperState{Evidence: ev}
	var old helperState
	if read(path+".helper", &old) == nil {
		if alive, _ := core.Alive(core.SystemProcs{}, old.Evidence); alive {
			return errors.New("mirror already has a live helper")
		}
		state.Seq = old.Seq
	}
	if err := write(path+".helper", state); err != nil {
		return err
	}
	defer func() { state.Detached = true; _ = write(path+".helper", state) }()
	c := herdr.New(e.Socket)
	check := func() error {
		var lease heartbeat
		if err := read(path+".lease", &lease); err != nil {
			return err
		}
		if time.Now().After(lease.Until) {
			return errors.New("mirror daemon lease expired")
		}
		if err := prove(ctx, e); err != nil {
			return err
		}
		pane, err := helperPane(ctx, c, e, os.Getpid())
		if err != nil {
			return err
		}
		state.Seq++
		if err := write(path+".helper", state); err != nil {
			return err
		}
		// Metadata is display-only, never agent.report_session or native identity.
		// No lifecycle override survives the helper's lease or crash.
		title := fmt.Sprintf("%s · %s · %s", e.Execution.Agent, lease.State, e.Execution.HarnessKind)
		return c.Call(ctx, "pane.report_metadata", map[string]any{"pane_id": pane.ID, "source": "herdsman-core-view", "seq": state.Seq, "title": title, "display_agent": "core: " + e.Execution.HarnessKind, "ttl_ms": 3000}, nil)
	}
	for attempt := 0; attempt < 3; attempt++ {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := prove(checkCtx, e)
		cancel()
		if err != nil {
			return err
		}
		binary := e.CoreBinary
		if binary == "" {
			binary = "hc"
		}
		attachCtx, stop := context.WithCancel(ctx)
		cmd := exec.CommandContext(attachCtx, binary, "attach", e.Execution.TerminalAddress)
		cmd.Env = append(cleanEnv(os.Environ(), e.Socket), "HC_SOCKET="+e.CoreSocket)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
		if err := cmd.Start(); err != nil {
			stop()
			return err
		}
		ended := make(chan error, 1)
		go func() { ended <- cmd.Wait() }()
		tick := time.NewTicker(time.Second)
		var result error
	wait:
		for {
			select {
			case result = <-ended:
				break wait
			case <-ctx.Done():
				stop()
				<-ended
				result = ctx.Err()
				break wait
			case <-tick.C:
				if err := check(); err != nil {
					stop()
					<-ended
					result = err
					break wait
				}
			}
		}
		tick.Stop()
		stop()
		if result == nil {
			return nil
		} // user detach is sticky, never an automatic reattach
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := check(); err != nil {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("mirror attachment failed after three attempts; runtime show may retry")
}
