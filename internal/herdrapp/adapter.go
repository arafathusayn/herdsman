// Package herdrapp drives the native herdr app through its own command,
// herdr-app, and nothing else.
//
// The app owns its layout document and its presentation labels. This package
// never writes that document and never writes herdr.seen, herdr.pinned or
// where. It reads the open window with `herdr-app ls`, changes only what an
// explicit-target `herdr-app do` command can change, and reads every effect
// back, because a reply (even one with pending:true) is not the effect.
package herdrapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultBinary is the app command a configuration names by default.
const DefaultBinary = "herdr-app"

// MinVersion is the oldest app whose command surface this package was
// written against: the probed 0.1.14 and the installed 0.1.15 both qualify.
const MinVersion = "0.1.14"

// TestedVersion is the app release the release notes name as tested.
const TestedVersion = "0.1.15"

// requiredCommands are the explicit-target commands the adapter uses. An app
// whose `commands` list lacks one is unsupported.
var requiredCommands = []string{"space.rename", "tab.rename", "agent.focus", "pane.focus"}

// TUIAlternative is the configuration a person can choose when the app is
// missing or unsupported. Every such error names it.
const TUIAlternative = `backend = "herdr-tui"`

var (
	// ErrUnavailable means the app command cannot be run on this machine.
	ErrUnavailable = errors.New("herdr-app is not available")
	// ErrUnsupported means the app runs but is too old or lacks a command.
	ErrUnsupported = errors.New("herdr-app is not supported")
	// ErrAppClosed means no window answers for the app home. It is not a
	// loss of placement: terminals keep running and keep their labels.
	ErrAppClosed = errors.New("herdr app window is not open")
)

// unavailable wraps cause in ErrUnavailable or ErrUnsupported with the TUI
// alternative named, so the diagnostic tells the person what to configure.
func unavailable(kind error, detail string) error {
	return fmt.Errorf("%w: %s; to run without the native app set [runtime] %s", kind, detail, TUIAlternative)
}

// Invocation is one run of the app command.
type Invocation struct {
	// Home is the app home, passed as HERDR_APP_HOME. It is always explicit,
	// so a surrounding HERDR_APP_HOME never picks the window.
	Home string
	Args []string
}

// Output is what one run printed and how it ended.
type Output struct {
	Stdout []byte
	Stderr []byte
	Exit   int
}

// Runner runs the app command. Tests use apptest's fake.
type Runner interface {
	Run(ctx context.Context, inv Invocation) (Output, error)
}

// ExecRunner runs the real binary with explicit arguments, a bounded output
// and a timeout.
type ExecRunner struct {
	Binary    string        // Program to run; empty means DefaultBinary.
	Timeout   time.Duration // Per run; zero means 10 s.
	MaxOutput int           // Bytes per stream; zero means 4 MiB.
}

// errOutputTooLong is returned when a stream exceeds MaxOutput.
var errOutputTooLong = errors.New("herdr-app: output exceeds its bound")

// boundedBuffer keeps at most limit bytes and remembers an overflow.
type boundedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if len(p) > room {
		b.overflow = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

// Run starts the binary once. A missing binary returns ErrUnavailable; a
// non-zero exit is reported in Output.Exit with a nil error, because the app
// uses exit codes for answers (1: a command error, 2: no window).
func (r ExecRunner) Run(ctx context.Context, inv Invocation) (Output, error) {
	if inv.Home == "" || !filepath.IsAbs(inv.Home) {
		return Output{Exit: -1}, fmt.Errorf("herdr-app: app home %q is not an absolute path", inv.Home)
	}
	binary := r.Binary
	if binary == "" {
		binary = DefaultBinary
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	limit := r.MaxOutput
	if limit <= 0 {
		limit = 4 << 20
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return Output{Exit: -1}, fmt.Errorf("herdr-app: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, inv.Args...)
	cmd.Env = append(withoutAppHome(os.Environ()), "HERDR_APP_HOME="+inv.Home)
	cmd.WaitDelay = time.Second
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: limit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	out := Output{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}
	if stdout.overflow || stderr.overflow {
		out.Exit = -1
		return out, errOutputTooLong
	}
	if runErr == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		out.Exit = -1
		return out, fmt.Errorf("herdr-app %s: %w", firstArg(inv.Args), ctx.Err())
	}
	var exited *exec.ExitError
	if errors.As(runErr, &exited) && exited.ExitCode() >= 0 {
		out.Exit = exited.ExitCode()
		return out, nil
	}
	out.Exit = -1
	return out, unavailable(ErrUnavailable, fmt.Sprintf("%s did not run: %v", binary, runErr))
}

// withoutAppHome drops an inherited HERDR_APP_HOME.
func withoutAppHome(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		return strings.HasPrefix(entry, "HERDR_APP_HOME=")
	})
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// DefaultHome is the app's own default home, used when the configuration
// names none: ~/.config/herdr-app.
func DefaultHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "herdr-app"), nil
}

// App is one configured app: the command and the home that picks its window.
type App struct {
	Runner Runner
	Home   string
}

// New returns an App for an absolute home.
func New(runner Runner, home string) *App { return &App{Runner: runner, Home: home} }

// run runs the app command once. A binary that is not there is reported as
// ErrUnavailable with the TUI alternative named.
func (a *App) run(ctx context.Context, args ...string) (Output, error) {
	out, err := a.Runner.Run(ctx, Invocation{Home: a.Home, Args: args})
	if errors.Is(err, exec.ErrNotFound) {
		return out, unavailable(ErrUnavailable, err.Error())
	}
	return out, err
}

// noWindow is how the app reports a home with no open window (exit 2).
const noWindow = "no Herdr window"

// windowCall runs a command that needs the open window and maps the
// no-window answer to ErrAppClosed.
func (a *App) windowCall(ctx context.Context, args ...string) (Output, error) {
	out, err := a.run(ctx, args...)
	if err != nil {
		return out, err
	}
	if out.Exit == 2 && strings.Contains(string(out.Stderr), noWindow) {
		return out, fmt.Errorf("%w for app home %s", ErrAppClosed, a.Home)
	}
	return out, nil
}

// Version returns the app version from `herdr-app --version`
// ("herdr-app 0.1.15").
func (a *App) Version(ctx context.Context) (string, error) {
	out, err := a.run(ctx, "--version")
	if err != nil {
		return "", err
	}
	if out.Exit != 0 {
		return "", unavailable(ErrUnavailable, fmt.Sprintf("herdr-app --version exited %d: %s", out.Exit, strings.TrimSpace(string(out.Stderr))))
	}
	name, version, ok := strings.Cut(strings.TrimSpace(string(out.Stdout)), " ")
	if !ok || name != "herdr-app" || parseVersion(version) == nil {
		return "", unavailable(ErrUnsupported, fmt.Sprintf("unrecognized version line %q", strings.TrimSpace(string(out.Stdout))))
	}
	return version, nil
}

// Command is one entry of `herdr-app commands`.
type Command struct {
	Name string `json:"name"`
	Args []struct {
		Name     string `json:"name"`
		Required bool   `json:"required"`
	} `json:"args"`
	About string `json:"about"`
}

// Commands returns the app's command list. It needs no open window.
func (a *App) Commands(ctx context.Context) ([]Command, error) {
	out, err := a.run(ctx, "commands")
	if err != nil {
		return nil, err
	}
	if out.Exit != 0 {
		return nil, unavailable(ErrUnsupported, fmt.Sprintf("herdr-app commands exited %d", out.Exit))
	}
	var commands []Command
	if err := json.Unmarshal(out.Stdout, &commands); err != nil {
		return nil, unavailable(ErrUnsupported, fmt.Sprintf("herdr-app commands printed no command list: %v", err))
	}
	return commands, nil
}

// Check confirms the app runs, is at least MinVersion and has every command
// the adapter uses. It needs no open window and returns the version.
func (a *App) Check(ctx context.Context) (string, error) {
	version, err := a.Version(ctx)
	if err != nil {
		return "", err
	}
	if compareVersions(parseVersion(version), parseVersion(MinVersion)) < 0 {
		return version, unavailable(ErrUnsupported, fmt.Sprintf("herdr-app %s is older than %s", version, MinVersion))
	}
	commands, err := a.Commands(ctx)
	if err != nil {
		return version, err
	}
	for _, name := range requiredCommands {
		if !slices.ContainsFunc(commands, func(c Command) bool { return c.Name == name }) {
			return version, unavailable(ErrUnsupported, fmt.Sprintf("herdr-app %s has no %s command", version, name))
		}
	}
	return version, nil
}

// List reads the open window's layout with `herdr-app ls`.
func (a *App) List(ctx context.Context) (Layout, error) {
	out, err := a.windowCall(ctx, "ls")
	if err != nil {
		return Layout{}, err
	}
	if out.Exit != 0 {
		return Layout{}, fmt.Errorf("herdr-app ls exited %d: %s", out.Exit, strings.TrimSpace(string(out.Stderr)))
	}
	var layout Layout
	if err := json.Unmarshal(out.Stdout, &layout); err != nil {
		return Layout{}, fmt.Errorf("herdr-app ls: bad layout: %w", err)
	}
	return layout, nil
}

// CommandError is an app command's own refusal ({"error": "..."}, exit 1).
type CommandError struct {
	Command string
	Message string
}

func (e *CommandError) Error() string { return "herdr-app do " + e.Command + ": " + e.Message }

// Reply is what a command printed on success. Pending means the app only
// acknowledged the command; the effect must still be read back.
type Reply struct {
	Pending bool `json:"pending"`
	Raw     json.RawMessage
}

// allowedCommands are the only commands do may send: each targets an
// explicit id and none writes a presentation label or ends a terminal.
var allowedCommands = []string{"space.rename", "tab.rename", "agent.focus", "pane.focus"}

// do sends one command as a single JSON object, the form the app documents
// for exact arguments.
func (a *App) do(ctx context.Context, command string, args map[string]string) (Reply, error) {
	if !slices.Contains(allowedCommands, command) {
		return Reply{}, fmt.Errorf("herdrapp: command %s is not one the adapter sends", command)
	}
	body, err := json.Marshal(args)
	if err != nil {
		return Reply{}, err
	}
	out, err := a.windowCall(ctx, "do", command, string(body))
	if err != nil {
		return Reply{}, err
	}
	var reply struct {
		Error   *string `json:"error"`
		Pending bool    `json:"pending"`
	}
	decodeErr := json.Unmarshal(out.Stdout, &reply)
	if out.Exit != 0 {
		if decodeErr == nil && reply.Error != nil {
			return Reply{}, &CommandError{Command: command, Message: *reply.Error}
		}
		return Reply{}, &CommandError{Command: command, Message: fmt.Sprintf("exit %d: %s", out.Exit, strings.TrimSpace(string(out.Stderr)))}
	}
	if decodeErr != nil {
		return Reply{}, fmt.Errorf("herdr-app do %s: bad reply: %w", command, decodeErr)
	}
	return Reply{Pending: reply.Pending, Raw: slices.Clone(out.Stdout)}, nil
}

// parseVersion reads "major.minor.patch"; nil when it is not one.
func parseVersion(s string) []int {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return nil
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		nums[i] = n
	}
	return nums
}

func compareVersions(a, b []int) int {
	if a == nil {
		return -1
	}
	return slices.Compare(a, b)
}
