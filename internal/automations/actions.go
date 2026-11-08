package automations

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/wtree"
)

const killGrace = 2 * time.Second

// Builtin is one built-in action.
type Builtin func(ctx context.Context, ev Event) error

// PluginRunner calls an installed plugin's action.
type PluginRunner interface {
	RunAction(ctx context.Context, plugin, action string, ev Event) error
}

var valueName = regexp.MustCompile(`^[a-z_]+$`)

type shellCommand struct {
	text string
	env  []string
}

// render expands a template so that values never become command text. Each
// variable expands to a quoted reference to an environment variable holding
// the value. The shell then treats the value as data in any quoting context the
// template author chose: a reference is expanded once and never re-read as code.
func render(rule Rule, ev Event) (shellCommand, error) {
	values := maps.Clone(ev.Values)
	if values == nil {
		values = map[string]string{}
	}
	values["hook_type"], values["hook_name"] = ev.Kind, ev.Kind
	references := make(map[string]string, len(values))
	env := []string{"HERDSMAN_EVENT=" + ev.Kind}
	for name, value := range values {
		if !valueName.MatchString(name) {
			continue
		}
		variable := "HERDSMAN_EVENT_" + strings.ToUpper(name)
		references[name] = `"$` + variable + `"`
		env = append(env, variable+"="+value)
	}
	text, err := wtree.Render("automation "+rule.Event, rule.Body, wtree.Vars{Values: references})
	if err != nil {
		return shellCommand{}, fmt.Errorf("automations: %s: %w", rule.Event, err)
	}
	return shellCommand{text: text, env: env}, nil
}

func (e *Engine) timeout(rule Rule) time.Duration {
	switch {
	case rule.Timeout > 0:
		return rule.Timeout
	case e.Timeout > 0:
		return e.Timeout
	}
	return DefaultTimeout
}

func (e *Engine) run(ctx context.Context, rule Rule, ev Event, shell shellCommand, out *Outcome) {
	limited, cancel := context.WithTimeout(ctx, e.timeout(rule))
	defer cancel()
	switch rule.Kind {
	case KindShell:
		e.runShell(limited, shell, out)
	case KindBuiltin:
		builtin := e.Builtins[rule.Body]
		if builtin == nil {
			out.failWith(StatusUnavailable, fmt.Errorf("automations: builtin %q is not available in this build", rule.Body))
			return
		}
		out.finish(builtin(limited, ev))
	case KindPlugin:
		if e.Plugins == nil {
			out.failWith(StatusUnavailable, errors.New("automations: plugin actions are not available in this build"))
			return
		}
		plugin, action, _ := strings.Cut(rule.Body, " ")
		out.finish(e.Plugins.RunAction(limited, plugin, strings.TrimSpace(action), ev))
	}
}

func (o *Outcome) finish(err error) {
	if err != nil {
		o.failWith(StatusFailed, err)
		return
	}
	o.Status, o.Exit = StatusRan, 0
}

func (e *Engine) runShell(ctx context.Context, shell shellCommand, out *Outcome) {
	runs := filepath.Join(e.folder(), "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		out.failWith(StatusError, err)
		return
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	out.Log = filepath.Join(runs, stamp+"-"+strings.TrimPrefix(out.Checksum, "sha256:")[:12]+".log")
	log, err := os.OpenFile(out.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		out.failWith(StatusError, err)
		return
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", shell.text)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = e.Dir, append(os.Environ(), shell.env...), log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = killGrace
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	err = cmd.Run()
	out.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	var exit *exec.ExitError
	switch {
	case err == nil:
		out.Status, out.Exit = StatusRan, 0
	case out.TimedOut:
		out.failWith(StatusFailed, errors.New("automations: the action timed out"))
	case errors.As(err, &exit):
		out.Exit = exit.ExitCode()
		out.failWith(StatusFailed, err)
	default:
		out.failWith(StatusFailed, err)
	}
}
