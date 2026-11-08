// Package statedir decides the one folder that holds a daemon's state. Every
// other path (socket, databases, the route registry, logs) is derived from it.
package statedir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Env is the variable that points a process at another state directory, for
// example a copy made for a rehearsal.
const Env = "HERDSMAN_STATE_DIR"

const envPrivateHome = "HERDSMAN_PRIVATE_HOME"

// Dir is a resolved state directory.
type Dir struct {
	Root string
	// Overridden reports that Env chose Root: such a daemon is self-contained
	// and shares nothing with the account's regular one.
	Overridden bool
}

// Resolve picks the state directory: the option, then Env, then the private
// home variable, then the default under the user's home. An option and an Env
// that disagree are refused, since either answer could open the wrong data.
func Resolve(option string, getenv func(string) string) (Dir, error) {
	env := getenv(Env)
	if env != "" && !filepath.IsAbs(env) {
		return Dir{}, fmt.Errorf("%s must be absolute, got %q", Env, env)
	}
	switch {
	case option != "" && env != "" && filepath.Clean(option) != filepath.Clean(env):
		return Dir{}, fmt.Errorf("--private-home %s and %s=%s name different state directories; keep one", option, Env, env)
	case env != "":
		return Dir{Root: filepath.Clean(env), Overridden: true}, nil
	case option != "":
		return Dir{Root: option}, nil
	}
	if v := getenv(envPrivateHome); v != "" {
		return Dir{Root: v}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Dir{}, err
	}
	return Dir{Root: filepath.Join(home, ".herdsman-private")}, nil
}

// LockHome is the folder of the singleton lock: the user's home for the
// regular daemon, the state directory itself for an overridden one, so two
// state directories can be served at once.
func (d Dir) LockHome(home string) string {
	if d.Overridden {
		return d.Root
	}
	return home
}
