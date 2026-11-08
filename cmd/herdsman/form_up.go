package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/tui"
	"github.com/arafathusayn/herdsman/internal/tui/forms"
	"github.com/arafathusayn/herdsman/sdk/config"
)

// planTimeout bounds the run that fills a form's last screen.
const planTimeout = 60 * time.Second

// knownHarnesses are the programs the up form offers when they are installed.
var knownHarnesses = []string{"claude", "codex", "opencode", "muse"}

// These are replaced by tests.
var (
	lookHarness   = exec.LookPath
	openForm      = forms.Open
	selfPath      = os.Executable
	projectFile   = config.ProjectFile
	execVerb      = defaultExecVerb
	planProgram   = defaultPlanProgram
	formTerminals = defaultFormTerminals
)

// defaultExecVerb replaces this process with the command the form built. A form
// leaves a goroutine reading the terminal, and a verb that asks a question or
// attaches to a session needs the terminal to itself.
func defaultExecVerb(argv []string) error {
	self, err := selfPath()
	if err != nil {
		return err
	}
	return syscall.Exec(self, append([]string{"herdsman"}, argv...), os.Environ())
}

func defaultFormTerminals(stdin io.Reader, stdout io.Writer) (*os.File, *os.File, bool) {
	in, isFile := stdin.(*os.File)
	out, outIsFile := stdout.(*os.File)
	if !isFile || !outIsFile || !tui.IsTerminal(in) || !tui.IsTerminal(out) {
		return nil, nil, false
	}
	return in, out, true
}

// addFormFlags lets a verb accept the form's flags beside its other arguments,
// where they have no effect because a form opens only with no arguments.
func addFormFlags(fs *flag.FlagSet) {
	fs.Bool("plain", false, "ask with line prompts")
	fs.Bool("no-tui", false, "never open the form")
}

// formMode reads a command line that is empty except for the form's own flags,
// and removes those flags, since most verbs do not define them.
func formMode(args *[]string) (open, plain bool) {
	open = true
	for _, arg := range *args {
		switch arg {
		case "--plain", "-plain":
			plain = true
		case "--no-tui", "-no-tui":
			open = false
		default:
			return false, false
		}
	}
	*args = nil
	return open, plain
}

// presentForm opens a form when the command has no arguments and a terminal,
// then replaces this process with the command the answers describe. The second
// result is false when the command should go on down its scripted path.
func presentForm(args *[]string, stdin io.Reader, stdout, stderr io.Writer, build func() (forms.Spec, bool)) (int, bool) {
	open, plain := formMode(args)
	if !open {
		return 0, false
	}
	in, out, ok := formTerminals(stdin, stdout)
	if !ok {
		return 0, false
	}
	spec, ok := build()
	if !ok {
		return 0, false
	}
	argv, submitted, err := openForm(context.Background(), spec, in, out, plain || os.Getenv("TERM") == "dumb")
	if err != nil {
		return fail(stderr, err), true
	}
	if !submitted {
		fmt.Fprintln(stdout, "canceled, nothing ran")
		return 0, true
	}
	if err := execVerb(argv); err != nil {
		return fail(stderr, err), true
	}
	return 0, true
}

// planOutput runs a command with no terminal and returns what it printed. A
// verb that wants consent prints its plan and exits 2 at the question, so its
// error line is noise then; any other failure says why the plan cannot run.
func planOutput(program string, argv []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), planTimeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, program, argv...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out.String()
	case !errors.As(err, &exit):
		return "could not build the plan: " + err.Error()
	case exit.ExitCode() == 2 && out.Len() > 0:
		return out.String()
	}
	return out.String() + errOut.String()
}

func defaultPlanProgram(argv []string) string {
	self, err := selfPath()
	if err != nil {
		return "could not build the plan: " + err.Error()
	}
	return planOutput(self, argv)
}

func formCommon() forms.Common {
	return forms.Common{Style: tui.NewStyle(os.Getenv), Plan: func(argv []string) string {
		return strings.TrimRight(planProgram(argv), "\n")
	}}
}

// foundHarnesses lists the known programs that are installed.
func foundHarnesses() []string {
	var found []string
	for _, name := range knownHarnesses {
		if _, err := lookHarness(name); err == nil {
			found = append(found, name)
		}
	}
	return found
}

func openUpForm(args *[]string, stdin io.Reader, stdout, stderr io.Writer) (int, bool) {
	return presentForm(args, stdin, stdout, stderr, func() (forms.Spec, bool) {
		path, err := projectFile(config.Options{RepoRoot: repoRoot()})
		if err != nil {
			path = ""
		}
		return forms.Up(forms.UpDefaults{Common: formCommon(), Harnesses: foundHarnesses(), Config: path}), true
	})
}
