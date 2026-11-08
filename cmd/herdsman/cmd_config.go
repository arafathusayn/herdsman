package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/approvals"
	"github.com/arafathusayn/herdsman/sdk/config"
)

const configUsage = `usage: herdsman config get <key> [-c <path>]
       herdsman config set <key> <value> [-c <path>]
       herdsman config unset <key> [-c <path>]
       herdsman config show [--strict] [-c <path>]
       herdsman config export --format toml|yaml|json|bin [-c <path>]
       herdsman config update [-c <path>]
       herdsman config init [--format toml|yaml|json|bin] [-c <path>]
       herdsman config schema --format jsonschema|descriptor
       herdsman config approvals list
       herdsman config approvals clear [<project>]
       herdsman config shell install|uninstall [bash|zsh|fish] [--json]
`

const configHelp = configUsage + `
Keys are dotted paths such as merge.strategy; a kebab-case spelling is the same
key. -c names a file; otherwise HERDSMAN_CONFIG, then the repository's parent
folder, then the repository root.

show prints the file in use and then one "key = value (source)" line each.
A warning goes to stderr; --strict makes it an error. An unknown key in the
policy or launch section always stops the command.

shell adds or removes the wrapper that lets herdsman go change the shell's
folder, with static completions. The shell defaults to $SHELL.

exit codes:
  0  done
  1  the configuration is invalid, or the file cannot be read or written
  2  usage error
`

func init() {
	register(command{name: "config", summary: "Read and change the configuration; install the shell integration.", help: configHelp, run: runConfig})
}

// configArgs is a config command line; flags may come in any position.
type configArgs struct {
	path, format string
	strict       bool
	words        []string
}

func parseConfigArgs(args []string) (*configArgs, error) {
	out := &configArgs{}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "-c", "--format":
			if i+1 == len(args) {
				return nil, fmt.Errorf("%s needs a value", arg)
			}
			i++
			if arg == "-c" {
				out.path = args[i]
			} else {
				out.format = args[i]
			}
		case "--strict":
			out.strict = true
		default:
			out.words = append(out.words, arg)
		}
	}
	return out, nil
}

// configVerbs maps each verb to its handler and how many words it takes.
var configVerbs = map[string]struct {
	minWords, maxWords int
	run                func(*configArgs, io.Writer, io.Writer) error
}{
	"get":       {1, 1, configGet},
	"set":       {2, 2, configSet},
	"unset":     {1, 1, configUnset},
	"show":      {0, 0, configShow},
	"export":    {0, 0, configExport},
	"update":    {0, 0, configUpdate},
	"init":      {0, 0, configInit},
	"schema":    {0, 0, configSchema},
	"approvals": {1, 2, configApprovals},
}

func runConfig(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, configUsage)
		return 2
	}
	if args[0] == "shell" {
		return runConfigShell(args, stdout, stderr)
	}
	verb, ok := configVerbs[args[0]]
	parsed, err := parseConfigArgs(args[1:])
	if !ok || err != nil || len(parsed.words) < verb.minWords || len(parsed.words) > verb.maxWords {
		fmt.Fprint(stderr, configUsage)
		return 2
	}
	if err := verb.run(parsed, stdout, stderr); err != nil {
		var usage configUsageError
		if errors.As(err, &usage) {
			fmt.Fprintf(stderr, "herdsman config %s: %v\n%s", args[0], err, configUsage)
			return 2
		}
		fmt.Fprintf(stderr, "herdsman config %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

// configUsageError is a command line that cannot be acted on.
type configUsageError string

func (e configUsageError) Error() string { return string(e) }

// repoRoot walks up from the working folder to the nearest repository.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// repositoryKey names the repository at root in the user file, from its
// origin remote. A repository with no remote has no overrides.
func repositoryKey(root string) string {
	if root == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", root, "remote", "get-url", "origin")
	cmd.Env = slices.DeleteFunc(os.Environ(), func(pair string) bool {
		return strings.HasPrefix(pair, "GIT_DIR=") || strings.HasPrefix(pair, "GIT_WORK_TREE=")
	})
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return config.RepositoryKey(string(out))
}

func (a *configArgs) options() config.Options {
	root := repoRoot()
	return config.Options{ConfigPath: a.path, RepoRoot: root, ProjectKey: repositoryKey(root), Strict: a.strict}
}

func (a *configArgs) load(stderr io.Writer) (*config.Result, error) {
	result, err := config.Load(a.options())
	if err != nil {
		return nil, err
	}
	for _, warning := range result.Warnings {
		fmt.Fprintln(stderr, "warning:", warning)
	}
	return result, nil
}

// target is the project file an edit acts on.
func (a *configArgs) target() (string, error) {
	path, err := config.ProjectFile(a.options())
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", errors.New("no configuration file; create one with herdsman config init")
	}
	return path, nil
}

func configGet(a *configArgs, stdout, stderr io.Writer) error {
	result, err := a.load(stderr)
	if err != nil {
		return err
	}
	value, err := result.Get(a.words[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, value)
	return nil
}

func configSet(a *configArgs, stdout, _ io.Writer) error {
	path, err := a.target()
	if err != nil {
		return err
	}
	if err := config.SetFile(path, a.words[0], a.words[1]); err != nil {
		return err
	}
	fmt.Fprintln(stdout, path)
	return nil
}

func configUnset(a *configArgs, stdout, _ io.Writer) error {
	path, err := a.target()
	if err != nil {
		return err
	}
	removed, err := config.UnsetFile(path, a.words[0])
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintf(stdout, "%s: not set in %s\n", a.words[0], path)
		return nil
	}
	fmt.Fprintln(stdout, path)
	return nil
}

func configShow(a *configArgs, stdout, stderr io.Writer) error {
	result, err := a.load(stderr)
	if err != nil {
		return err
	}
	if result.ProjectPath != "" {
		fmt.Fprintf(stdout, "file: %s\n", result.ProjectPath)
	} else {
		fmt.Fprintln(stdout, "file: none")
	}
	for _, value := range result.Show() {
		fmt.Fprintf(stdout, "%s = %s (%s)\n", value.Key, value.Value, value.Source)
	}
	return nil
}

func configExport(a *configArgs, stdout, _ io.Writer) error {
	if a.format == "" {
		return configUsageError("--format is required")
	}
	path, err := a.target()
	if err != nil {
		return err
	}
	data, err := config.ExportFile(path, a.format)
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}

func configUpdate(a *configArgs, stdout, _ io.Writer) error {
	path, err := a.target()
	if err != nil {
		return err
	}
	if err := config.UpdateFile(path); err != nil {
		return err
	}
	fmt.Fprintln(stdout, path)
	return nil
}

func configInit(a *configArgs, stdout, _ io.Writer) error {
	format := a.format
	if format == "" {
		format = config.FormatTOML
	}
	path := a.path
	if path == "" {
		root := repoRoot()
		if root == "" {
			return errors.New("not inside a repository; name a file with -c")
		}
		path = filepath.Join(filepath.Dir(root), "herdsman.config."+format)
	}
	if err := config.InitFile(path, format); err != nil {
		return err
	}
	fmt.Fprintln(stdout, path)
	if format == config.FormatJSON || format == config.FormatBin {
		fmt.Fprintln(stdout, "this format has no comments; herdsman config schema --format jsonschema describes every key")
	}
	return nil
}

func configSchema(a *configArgs, stdout, _ io.Writer) error {
	var data []byte
	var err error
	switch a.format {
	case "jsonschema":
		data, err = config.JSONSchema()
	case "descriptor":
		data, err = config.DescriptorSet()
	default:
		return configUsageError("--format must be jsonschema or descriptor")
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}

func configApprovals(a *configArgs, stdout, _ io.Writer) error {
	store, err := approvals.Default(os.Environ())
	if err != nil {
		return err
	}
	switch a.words[0] {
	case "list":
		if len(a.words) != 1 {
			return configUsageError("list takes no project")
		}
		held, err := store.List()
		if err != nil {
			return err
		}
		projects := make([]string, 0, len(held))
		for project := range held {
			projects = append(projects, project)
		}
		slices.Sort(projects)
		for _, project := range projects {
			for _, template := range held[project] {
				fmt.Fprintf(stdout, "%s\t%s\n", project, template)
			}
		}
		return nil
	case "clear":
		project := ""
		if len(a.words) == 2 {
			project = a.words[1]
		}
		removed, err := store.Clear(project)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed %d\n", removed)
		return nil
	default:
		return configUsageError("approvals takes list or clear")
	}
}
