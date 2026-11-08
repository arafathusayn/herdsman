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
// the step's words and nothing else. Before a rerun, call ClearMarkers.
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
// printed line is display.
func wrapLine(stepsDir, id string, command Command) string {
	start := quote(filepath.Join(stepsDir, id+startSuffix))
	rc := quote(filepath.Join(stepsDir, id+rcSuffix))
	return fmt.Sprintf(`printf '' > %s && %s; rc=$?; printf %%s $rc > %s; printf 'herdsman-step %%s rc=%%s\n' %s $rc`,
		start, command, rc, id)
}

// quote renders one word for a POSIX shell, bash or zsh. Single quotes stop
// every expansion, including history expansion in an interactive shell. A
// leading = is quoted for zsh, which expands =cmd.
func quote(s string) string {
	if s == "" {
		return "''"
	}
	if s[0] != '=' && !strings.ContainsFunc(s, unsafeInWord) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func unsafeInWord(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("_@%+=:,./-", r)
}

// reservedWords are parsed as syntax when unquoted in command position, in
// POSIX shells, bash or zsh.
var reservedWords = map[string]bool{
	"case": true, "coproc": true, "do": true, "done": true, "elif": true, "else": true,
	"end": true, "esac": true, "fi": true, "for": true, "foreach": true, "function": true,
	"if": true, "in": true, "nocorrect": true, "repeat": true, "select": true, "then": true,
	"time": true, "until": true, "while": true,
}

// quoteProgram also quotes what is special only as the first word: an unquoted
// K=V would be an assignment and an unquoted if would open a compound command.
func quoteProgram(s string) string {
	if strings.Contains(s, "=") || reservedWords[s] {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return quote(s)
}
