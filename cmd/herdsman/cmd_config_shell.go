package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/arafathusayn/herdsman/internal/shell"
)

// shellSchemaVersion is the version of this command's JSON. It only grows.
const shellSchemaVersion = "1.0.0"

func printJSON(stdout, stderr io.Writer, value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, string(data))
	return 0
}

// runConfigShell adds or removes the shell wrapper. args start at "shell".
func runConfigShell(args []string, stdout, stderr io.Writer) int {
	asJSON := slices.Contains(args, "--json")
	args = slices.DeleteFunc(slices.Clone(args), func(arg string) bool { return arg == "--json" })
	if len(args) < 2 || len(args) > 3 || args[0] != "shell" || (args[1] != "install" && args[1] != "uninstall") {
		return usageError(stderr, "usage: herdsman config shell install|uninstall [bash|zsh|fish]")
	}
	var name string
	var err error
	if len(args) == 3 {
		name = args[2]
	} else if name, err = shell.Detect(os.Getenv("SHELL")); err != nil {
		return fail(stderr, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fail(stderr, err)
	}
	env := shell.Env{Home: home, ZDotDir: os.Getenv("ZDOTDIR")}
	if args[1] == "install" {
		path, err := shell.Install(name, env)
		if err != nil {
			return fail(stderr, err)
		}
		if asJSON {
			return printJSON(stdout, stderr, map[string]any{"schema": map[string]string{"version": shellSchemaVersion}, "shell": name, "path": path})
		}
		fmt.Fprintf(stdout, "installed in %s; open a new shell to use it\n", path)
		return 0
	}
	removed, err := shell.Uninstall(name, env)
	if err != nil {
		return fail(stderr, err)
	}
	if asJSON {
		return printJSON(stdout, stderr, map[string]any{"schema": map[string]string{"version": shellSchemaVersion}, "shell": name, "removed": removed})
	}
	if removed {
		fmt.Fprintf(stdout, "removed the %s integration\n", name)
	} else {
		fmt.Fprintf(stdout, "nothing to remove for %s\n", name)
	}
	return 0
}
