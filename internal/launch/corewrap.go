package launch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/secrets"
)

// The core wrapper is the one place in this package that starts processes: it
// runs inside a reserved herdr-core terminal, proves itself to the daemon by
// process, runs the prelude as its own children, registers, and then replaces
// itself with the harness, which keeps its pid (spec sections 6, 8 and 13).

// WrapProtocol is the version of the launch socket's messages. Both ends are
// the same build. Version 2 requires restricted descriptors to be verified
// before registration: a version 1 wrapper would silently ignore the sandbox
// field and must be refused.
const WrapProtocol = 2

// The wrapper's requests, in the order they are sent on one connection.
const (
	WrapAuthenticate = "authenticate"
	WrapRegister     = "register"
	WrapFailed       = "failed"
)

// WrapRequest is one line the wrapper sends.
type WrapRequest struct {
	Op       string `json:"op"`
	Protocol int    `json:"protocol"`
	Intent   string `json:"intent"`
	// Reason says why a launch failed, for WrapFailed.
	Reason string `json:"reason,omitempty"`
}

// WrapReply is the daemon's answer to one request.
type WrapReply struct {
	OK         bool            `json:"ok"`
	Error      string          `json:"error,omitempty"`
	Descriptor *CoreDescriptor `json:"descriptor,omitempty"`
}

// Exit codes of the core wrapper. Every failure exits with one of them, which
// is final under keep:true: no shell is left behind as a successful agent.
const (
	ExitUsage         = 64
	ExitRefused       = 70 // the daemon did not authenticate this process
	ExitPrelude       = 71 // a prelude step failed or needs a person
	ExitNotRegistered = 72
	ExitExec          = 73
	ExitConnection    = 74 // the launch socket went away
)

// Wrapper is one run of the core wrapper. Zero fields take the system's.
type Wrapper struct {
	Socket string
	Intent string
	// Environ is the wrapper's own environment: whatever started the core,
	// plus core's HC_* variables.
	Environ  []string
	Keychain secrets.Keychain
	// Run runs one marked prelude line under /bin/sh in env and dir, and
	// returns when it has ended.
	Run func(ctx context.Context, line string, env []string, dir string) error
	// Exec replaces the process with the harness.
	Exec   func(argv0 string, argv, env []string) error
	Dial   func(ctx context.Context, socket string) (net.Conn, error)
	Stderr io.Writer
	// DialWait bounds the wait for the launch socket (default 30 s).
	DialWait time.Duration
	// VerifySandbox is installed by the executable's server wiring. A
	// restricted descriptor is refused when no verifier is configured.
	VerifySandbox func(ctx context.Context, binary, cwd string, env, config, args []string) error
}

func (w *Wrapper) defaults() {
	if w.Environ == nil {
		w.Environ = os.Environ()
	}
	if w.Keychain == nil {
		w.Keychain = secrets.System()
	}
	if w.Run == nil {
		w.Run = runLine
	}
	if w.Exec == nil {
		w.Exec = syscall.Exec
	}
	if w.Dial == nil {
		w.Dial = func(ctx context.Context, socket string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}
	}
	if w.Stderr == nil {
		w.Stderr = os.Stderr
	}
	if w.DialWait == 0 {
		w.DialWait = 30 * time.Second
	}
}

// runLine runs one marked line as a child, with exactly env.
func runLine(ctx context.Context, line string, env []string, dir string) error {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", line)
	cmd.Env, cmd.Dir = env, dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil // the status is in the step's .rc marker
	}
	return err
}

// conn is the wrapper's one connection to the launch socket.
type wrapConn struct {
	c net.Conn
	r *bufio.Reader
}

func (c wrapConn) call(req WrapRequest) (WrapReply, error) {
	req.Protocol = WrapProtocol
	b, err := json.Marshal(req)
	if err != nil {
		return WrapReply{}, err
	}
	if _, err := c.c.Write(append(b, '\n')); err != nil {
		return WrapReply{}, err
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return WrapReply{}, err
	}
	var rep WrapReply
	if err := json.Unmarshal(line, &rep); err != nil {
		return WrapReply{}, fmt.Errorf("launch: unreadable answer: %w", err)
	}
	return rep, nil
}

func (w *Wrapper) fail(code int, format string, args ...any) int {
	fmt.Fprintf(w.Stderr, "herdsman launch: "+format+"\n", args...)
	return code
}

func (w *Wrapper) dial(ctx context.Context) (net.Conn, error) {
	deadline := time.Now().Add(w.DialWait)
	for {
		c, err := w.Dial(ctx, w.Socket)
		if err == nil || time.Now().After(deadline) {
			return c, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Main runs the wrapper and returns its exit code. On success it does not
// return: Exec replaces the process with the harness.
func (w *Wrapper) Main(ctx context.Context) int {
	w.defaults()
	if w.Socket == "" || w.Intent == "" {
		return w.fail(ExitUsage, "the launch socket and intent are required")
	}
	nc, err := w.dial(ctx)
	if err != nil {
		return w.fail(ExitConnection, "reach the daemon's launch socket: %v", err)
	}
	defer nc.Close()
	c := wrapConn{c: nc, r: bufio.NewReaderSize(nc, 1<<16)}

	// Moment one: the daemon authenticates this process as the reserved
	// terminal's program and hands over the launch environment's policy.
	rep, err := c.call(WrapRequest{Op: WrapAuthenticate, Intent: w.Intent})
	if err != nil {
		return w.fail(ExitConnection, "authenticate: %v", err)
	}
	if !rep.OK || rep.Descriptor == nil {
		return w.fail(ExitRefused, "the daemon refused this launch: %s", rep.Error)
	}
	d := *rep.Descriptor
	if d.Intent != w.Intent {
		return w.fail(ExitRefused, "the daemon answered for launch %s, not %s", d.Intent, w.Intent)
	}
	if err := d.Validate(); err != nil {
		return w.fail(ExitRefused, "%v", err)
	}
	granted, err := grantedSecrets(w.Keychain, d.SecretRefs)
	if err != nil {
		_, _ = c.call(WrapRequest{Op: WrapFailed, Intent: w.Intent, Reason: "a granted secret cannot be read"})
		return w.fail(ExitPrelude, "%v", err)
	}
	env := CoreEnv(d, w.Environ, granted)

	// The prelude runs in the launch environment, granted secrets included,
	// before anything is registered.
	if err := RunCorePrelude(ctx, d, env, w.Run); err != nil {
		_, _ = c.call(WrapRequest{Op: WrapFailed, Intent: w.Intent, Reason: err.Error()})
		return w.fail(ExitPrelude, "%v", err)
	}

	if len(d.Sandbox) > 0 {
		if d.Kind != "codex" {
			return w.fail(ExitRefused, "only Codex has a qualified core sandbox")
		}
		if w.VerifySandbox == nil {
			return w.fail(ExitRefused, "restricted launch has no sandbox verifier")
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := w.VerifySandbox(checkCtx, d.Harness[0], d.Cwd, env, d.Sandbox, d.Harness[1:])
		cancel()
		if err != nil {
			_, _ = c.call(WrapRequest{Op: WrapFailed, Intent: w.Intent, Reason: err.Error()})
			return w.fail(ExitRefused, "%v", err)
		}
	}

	// Moment two: the daemon checks the peer again and records it as the
	// execution's process evidence.
	rep, err = c.call(WrapRequest{Op: WrapRegister, Intent: w.Intent})
	if err != nil {
		return w.fail(ExitConnection, "register: %v", err)
	}
	if !rep.OK {
		return w.fail(ExitNotRegistered, "the daemon did not register this launch: %s", rep.Error)
	}
	_ = nc.Close()
	if err := w.Exec(d.Harness[0], d.Argv(), env); err != nil {
		return w.fail(ExitExec, "start %s: %v", d.Harness[0], err)
	}
	return 0 // only a test's Exec returns
}

// grantedSecrets reads the values of the granted references as NAME=value.
func grantedSecrets(k secrets.Keychain, refs string) ([]string, error) {
	parsed, err := secrets.ParseRefs(refs)
	if err != nil || len(parsed) == 0 {
		return nil, err
	}
	set, err := secrets.Load(k, parsed)
	if err != nil {
		return nil, err
	}
	return set.Environ(nil), nil
}

// RunCorePrelude runs the descriptor's prelude steps in order, each as a
// marked line under run, with the same marker files and retry rules as a
// prelude typed into a TUI pane: a step recorded done is skipped, an
// interrupted step runs again only when it is idempotent or its probe says
// so, and anything else stops the launch for a person.
func RunCorePrelude(ctx context.Context, d CoreDescriptor, env []string, run func(context.Context, string, []string, string) error) error {
	if len(d.Prelude) == 0 {
		return nil
	}
	if err := os.MkdirAll(d.StepsDir, 0o700); err != nil {
		return err
	}
	timeout := time.Duration(d.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = DefaultPreludeTimeout
	}
	for _, s := range d.Prelude {
		if err := runCoreStep(ctx, d, s, env, timeout, run); err != nil {
			return err
		}
	}
	return nil
}

func runCoreStep(ctx context.Context, d CoreDescriptor, s NumberedStep, env []string, timeout time.Duration, run func(context.Context, string, []string, string) error) error {
	marker, err := ReadMarker(d.StepsDir, s.N)
	if err != nil {
		return err
	}
	decision := Decide(s.Step, s.N, false, marker)
	for range 4 {
		switch decision.Action {
		case ActionDone, ActionSkip:
			return nil
		case ActionFail, ActionStop:
			reason := decision.Reason
			if len(decision.Resume) > 0 {
				reason += "; resume with: " + strings.Join(decision.Resume, " or ")
			}
			return errors.New(reason)
		case ActionRun:
			if err := ClearMarkers(d.StepsDir, s.N); err != nil {
				return err
			}
			line, err := Wrap(d.StepsDir, s.N, s.Step)
			if err != nil {
				return err
			}
			if err := runTimed(ctx, run, line, env, d.Cwd, timeout); err != nil {
				return fmt.Errorf("step %d: %w", s.N, err)
			}
			if marker, err = ReadMarker(d.StepsDir, s.N); err != nil {
				return err
			}
			if decision = Decide(s.Step, s.N, false, marker); decision.Action == ActionRun {
				return fmt.Errorf("step %d left no markers", s.N)
			}
		case ActionProbe:
			if err := ClearProbeMarkers(d.StepsDir, s.N); err != nil {
				return err
			}
			line, err := WrapProbe(d.StepsDir, s.N, s.Step)
			if err != nil {
				return err
			}
			if err := runTimed(ctx, run, line, env, d.Cwd, timeout); err != nil {
				return fmt.Errorf("the probe of step %d: %w", s.N, err)
			}
			pm, err := ReadProbeMarker(d.StepsDir, s.N)
			if err != nil {
				return err
			}
			if decision = DecideProbe(s.Step, s.N, pm); decision.Action == ActionProbe {
				return fmt.Errorf("the probe of step %d gave no result", s.N)
			}
		default:
			return fmt.Errorf("step %d: unexpected decision %s", s.N, decision.Action)
		}
	}
	return fmt.Errorf("step %d did not settle", s.N)
}

func runTimed(ctx context.Context, run func(context.Context, string, []string, string) error, line string, env []string, dir string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := run(ctx, line, env, dir); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("timed out after %s", timeout)
	}
	return nil
}
