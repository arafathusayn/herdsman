// Package shell writes the integration a shell needs for `herdsman go` to change
// its folder: a wrapper function and static completions, kept between two marker
// lines so that a later version can replace them in place.
//
// The wrapper hands the binary a directive file through HERDSMAN_DIRECTIVE_FILE.
// A line starting with "/" is a folder to enter; any other line belongs to a
// later directive kind and is ignored.
package shell

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	startMark = "# >>> herdsman shell integration >>>"
	endMark   = "# <<< herdsman shell integration <<<"

	verbs = "new ls go hook config"
)

// Shells are the shells with an integration.
var Shells = []string{"bash", "zsh", "fish"}

// Env is where a shell's startup files live.
type Env struct {
	Home    string
	ZDotDir string
}

const posixWrapper = `herdsman() {
  local directive code line target
  directive="$(mktemp "${TMPDIR:-/tmp}/herdsman-directive.XXXXXX")" || return 1
  HERDSMAN_DIRECTIVE_FILE="$directive" command herdsman "$@"
  code=$?
  while IFS= read -r line; do
    case "$line" in /*) target="$line" ;; esac
  done < "$directive"
  rm -f "$directive"
  if [ -n "$target" ] && [ -d "$target" ]; then
    cd -- "$target" || return 1
  fi
  return "$code"
}`

const fishWrapper = `function herdsman
    set -l directive (mktemp)
    or return 1
    command env HERDSMAN_DIRECTIVE_FILE=$directive herdsman $argv
    set -l code $status
    set -l target
    for line in (cat $directive)
        if string match -q '/*' -- $line
            set target $line
        end
    end
    rm -f $directive
    if test -n "$target"; and test -d "$target"
        cd $target
    end
    return $code
end`

// Detect names the integration for a shell program such as the value of $SHELL.
func Detect(program string) (string, error) {
	name := filepath.Base(program)
	if !slices.Contains(Shells, name) {
		return "", fmt.Errorf("no shell integration for %q; pass one of %s", program, strings.Join(Shells, ", "))
	}
	return name, nil
}

// Wrapper is the function that lets the binary change the shell's folder.
func Wrapper(name string) (string, error) {
	switch name {
	case "bash", "zsh":
		return posixWrapper, nil
	case "fish":
		return fishWrapper, nil
	}
	return "", fmt.Errorf("no shell integration for %q", name)
}

// Completions is the static completion script.
func Completions(name string) (string, error) {
	switch name {
	case "bash":
		return `complete -W "` + verbs + `" herdsman`, nil
	case "zsh":
		return `if (( $+functions[compdef] )); then
  _herdsman() { compadd ` + verbs + `; }
  compdef _herdsman herdsman
fi`, nil
	case "fish":
		return `complete -c herdsman -f -a "` + verbs + `"`, nil
	}
	return "", fmt.Errorf("no shell integration for %q", name)
}

// RCFile is the startup file that holds the integration.
func RCFile(name string, env Env) (string, error) {
	switch name {
	case "bash":
		return filepath.Join(env.Home, ".bashrc"), nil
	case "zsh":
		if env.ZDotDir != "" {
			return filepath.Join(env.ZDotDir, ".zshrc"), nil
		}
		return filepath.Join(env.Home, ".zshrc"), nil
	case "fish":
		return filepath.Join(env.Home, ".config", "fish", "conf.d", "herdsman.fish"), nil
	}
	return "", fmt.Errorf("no shell integration for %q", name)
}

// locate returns the block's bounds; end is past the end marker.
func locate(text string) (start, end int, found bool, err error) {
	start = strings.Index(text, startMark)
	if start < 0 {
		return 0, 0, false, nil
	}
	length := strings.Index(text[start:], endMark)
	if length < 0 {
		return 0, 0, false, errors.New("the file has a start marker and no end marker; fix it by hand")
	}
	return start, start + length + len(endMark), true, nil
}

func readRC(path string) (string, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0o644, nil
	}
	if err != nil {
		return "", 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	return string(data), info.Mode().Perm(), nil
}

// writeRC replaces the file atomically, through a symbolic link when the file
// is one, so that a dotfile manager's link survives.
func writeRC(path, text string, mode os.FileMode) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".herdsman-shell-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.WriteString(text)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(temp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(temp.Name(), path)
	}
	return err
}

// Install writes the integration, or replaces an older one where it stands,
// and returns the file it changed.
func Install(name string, env Env) (string, error) {
	wrapper, err := Wrapper(name)
	if err != nil {
		return "", err
	}
	completions, err := Completions(name)
	if err != nil {
		return "", err
	}
	path, err := RCFile(name, env)
	if err != nil {
		return "", err
	}
	text, mode, err := readRC(path)
	if err != nil {
		return "", err
	}
	block := startMark + "\n" + wrapper + "\n" + completions + "\n" + endMark
	start, end, found, err := locate(text)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	switch {
	case found:
		text = text[:start] + block + text[end:]
	case text == "" || strings.HasSuffix(text, "\n"):
		text += block + "\n"
	default:
		text += "\n" + block + "\n"
	}
	return path, writeRC(path, text, mode)
}

// Uninstall removes the integration and says whether there was one.
func Uninstall(name string, env Env) (bool, error) {
	path, err := RCFile(name, env)
	if err != nil {
		return false, err
	}
	text, mode, err := readRC(path)
	if err != nil {
		return false, err
	}
	start, end, found, err := locate(text)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !found {
		return false, nil
	}
	if end < len(text) && text[end] == '\n' {
		end++
	}
	return true, writeRC(path, text[:start]+text[end:], mode)
}
