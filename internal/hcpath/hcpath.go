// Package hcpath holds herdr-core's socket path rules and nothing else, so a
// client (the CLI's doctor, the config resolver) can name a core endpoint
// without importing the hc client, which is a server package.
package hcpath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultSession is the session hc uses when nothing names another.
const DefaultSession = "default"

// SessionSocket is the socket of a named core session under hc's path rules:
// under XDG_RUNTIME_DIR, or under the state directory (XDG_STATE_HOME, else
// HOME's .local/state) when XDG_RUNTIME_DIR is unset. It never reads
// HC_SOCKET or HC_SESSION: the session is the caller's to name. A nil getenv
// reads the process environment.
func SessionSocket(session string, getenv func(string) string) (string, error) {
	if err := ValidSession(session); err != nil {
		return "", err
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	return Socket(session, getenv)
}

// Socket applies the path rules to a session without validating its name;
// SessionSocket is the checked form.
func Socket(session string, getenv func(string) string) (string, error) {
	if x := getenv("XDG_RUNTIME_DIR"); x != "" {
		return filepath.Join(x, "herdr-core", session+".sock"), nil
	}
	state := getenv("XDG_STATE_HOME")
	if state == "" {
		home := getenv("HOME")
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil {
				return "", err
			}
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "herdr-core", session, session+".sock"), nil
}

// ValidSession refuses a session name that is not one path segment: under
// hc's path rules it becomes a folder and a file name.
func ValidSession(session string) error {
	if session == "" || session == "." || session == ".." || strings.ContainsAny(session, "/\\\x00") {
		return fmt.Errorf("%q is not a core session name: it must be one non-empty path segment", session)
	}
	return nil
}
