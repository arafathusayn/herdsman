package launch

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// StepsDir returns the marker folder of one operation.
func StepsDir(routeFolder, op string) (string, error) {
	if op == "" || op == "." || op == ".." || strings.ContainsAny(op, `/\`) {
		return "", fmt.Errorf("operation id %q cannot name a folder", op)
	}
	if err := checkText(op); err != nil {
		return "", fmt.Errorf("operation id: %w", err)
	}
	return filepath.Join(routeFolder, ".steps", op), nil
}

// Wrap returns the line submitted to the pane with pane.run for step n, whose
// markers go under stepsDir. Every value is quoted, so the line runs exactly
// the step's words and nothing else. On ActionRun, call ClearMarkers first.
func Wrap(stepsDir string, n int, step Step) (string, error) {
	if err := checkWrap(stepsDir, n, step); err != nil {
		return "", err
	}
	return wrapLine(stepsDir, strconv.Itoa(n), step.Command), nil
}

// WrapProbe returns the line that runs step n's probe, with its own markers.
func WrapProbe(stepsDir string, n int, step Step) (string, error) {
	if step.Probe == nil {
		return "", fmt.Errorf("step %d has no probe", n)
	}
	if err := checkWrap(stepsDir, n, step); err != nil {
		return "", err
	}
	return wrapLine(stepsDir, probeID(n), *step.Probe), nil
}

func checkWrap(stepsDir string, n int, step Step) error {
	if err := checkStepNumber(n); err != nil {
		return err
	}
	if err := checkText(stepsDir); err != nil {
		return fmt.Errorf("steps folder: %w", err)
	}
	if err := step.validate(); err != nil {
		return fmt.Errorf("step %d: %w", n, err)
	}
	return nil
}

func checkStepNumber(n int) error {
	if n < 1 {
		return errors.New("step numbers start at 1")
	}
	return nil
}

func probeID(n int) string { return strconv.Itoa(n) + ".probe" }

// wrapLine writes .start before the command, so a missing .start proves the
// command never ran, and .rc after it. The files are the authority; the
// printed line is display. Every fixed word is quoted as well, because the line
// reaches an interactive shell with the user's aliases, and >| writes a marker
// even under noclobber, where > would record its own failure as the status.
func wrapLine(stepsDir, id string, command Command) string {
	start := quote(filepath.Join(stepsDir, id+startSuffix))
	rc := quote(filepath.Join(stepsDir, id+rcSuffix))
	return fmt.Sprintf(`'printf' '' >| %s && %s; rc=$?; 'printf' '%%s' "$rc" >| %s; 'printf' 'herdsman-step %%s rc=%%s\n' %s "$rc"`,
		start, command, rc, quote(id))
}

// quote renders one word for a POSIX shell, bash or zsh. Single quotes stop
// every expansion, including alias, global alias and history expansion in an
// interactive shell.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
