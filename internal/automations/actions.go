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
// variable first expands to a marker; placeReferences then swaps the marker for
// a reference to an environment variable that holds the value.
func render(rule Rule, ev Event) (shellCommand, error) {
	values := maps.Clone(ev.Values)
	if values == nil {
		values = map[string]string{}
	}
	values["hook_type"], values["hook_name"] = ev.Kind, ev.Kind
	markers := make(map[string]string, len(values))
	env := []string{"HERDSMAN_EVENT=" + ev.Kind}
	for name, value := range values {
		if !valueName.MatchString(name) {
			continue
		}
		markers[name] = markerStart + name + markerEnd
		env = append(env, "HERDSMAN_EVENT_"+strings.ToUpper(name)+"="+value)
	}
	text, err := wtree.Render("automation "+rule.Event, rule.Body, wtree.Vars{Values: markers})
	if err == nil {
		text, err = placeReferences(text)
	}
	if err != nil {
		return shellCommand{}, fmt.Errorf("automations: %s: %w", rule.Event, err)
	}
	return shellCommand{text: text, env: env}, nil
}

const (
	markerStart = "\x01"
	markerEnd   = "\x02"
)

// A marker in one of these may be read twice by a shell, or not as a reference
// at all, and a scan of the text cannot follow them.
var unprovable = []string{"$(", "`", "<<"}

// placeReferences writes each marker in the form that keeps its value one
// piece of data where it stands: quoted outside quotes, plain inside double
// quotes. Anywhere else it refuses. Inside single quotes a reference would stay
// text, and closing the quotes would hand the value to any inner shell as code.
func placeReferences(text string) (string, error) {
	if !strings.Contains(text, markerStart) {
		return text, nil
	}
	refuse := func(name string) (string, error) {
		return "", fmt.Errorf(`variable %s cannot be used here: write it outside quotes or in double quotes, or read "$HERDSMAN_EVENT_%s"`, name, strings.ToUpper(name))
	}
	var out strings.Builder
	single, double := false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == markerStart[0] {
			size := strings.IndexByte(text[i:], markerEnd[0])
			name := text[i+1 : i+size]
			if single || containsAny(text, unprovable) {
				return refuse(name)
			}
			if double {
				out.WriteString("${HERDSMAN_EVENT_" + strings.ToUpper(name) + "}")
			} else {
				out.WriteString(`"${HERDSMAN_EVENT_` + strings.ToUpper(name) + `}"`)
			}
			i += size
			continue
		}
		out.WriteByte(c)
		switch {
		case c == '\\' && !single && i+1 < len(text):
			if text[i+1] == markerStart[0] {
				return refuse(text[i+2 : i+1+strings.IndexByte(text[i+1:], markerEnd[0])])
			}
			i++
			out.WriteByte(text[i])
		case c == '\'' && !double:
			single = !single
		case c == '"' && !single:
			double = !double
		}
	}
	return out.String(), nil
}

func containsAny(text string, parts []string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
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
