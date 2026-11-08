package guard

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// offlineEnv is the repository mode: the vendored tree, and no module proxy.
func offlineEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOFLAGS=") && !strings.HasPrefix(entry, "GOPROXY=") {
			env = append(env, entry)
		}
	}
	return append(env, "GOFLAGS=-mod=vendor", "GOPROXY=off")
}

// noNetworkBuild builds every package of the module at dir in the repository
// mode, so a package missing from the vendored tree cannot be fetched.
func noNetworkBuild(dir string) (string, error) {
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	cmd.Env = offlineEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestNoNetworkBuildFromEveryModuleRoot(t *testing.T) {
	dirs := workspaceDirs(readRepoFile(t, "go.work"))
	if len(dirs) == 0 {
		t.Fatal("go.work lists no modules")
	}
	for _, dir := range dirs {
		t.Run(dir, func(t *testing.T) {
			if out, err := noNetworkBuild(filepath.Join(repoRoot, filepath.FromSlash(dir))); err != nil {
				t.Fatalf("offline build from %s: %v\n%s", dir, err, out)
			}
		})
	}
}

// A module the vendored tree does not hold must fail, not download.
func TestNoNetworkBuildFailsOnAnUnvendoredModule(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.test/root\n\ngo 1.26.0\n\nrequire example.test/absent v1.0.0\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nimport _ \"example.test/absent\"\n\nfunc main() {}\n")
	out, err := noNetworkBuild(dir)
	if err == nil {
		t.Fatalf("offline build passed without the module:\n%s", out)
	}
}

func TestOfflineEnvReplacesInheritedSettings(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "https://proxy.golang.org")
	env := offlineEnv()
	for _, stale := range []string{"GOFLAGS=-mod=mod", "GOPROXY=https://proxy.golang.org"} {
		if slices.Contains(env, stale) {
			t.Errorf("offlineEnv kept %s", stale)
		}
	}
	for _, want := range []string{"GOFLAGS=-mod=vendor", "GOPROXY=off"} {
		if !slices.Contains(env, want) {
			t.Errorf("offlineEnv lacks %s", want)
		}
	}
}

// releaseConfigProblems returns how a goreleaser config leaves the repository
// mode: a tidy hook resolves modules over the network and dirties the tree.
func releaseConfigProblems(config string) []string {
	node, problems := parseYAML(config)
	root, ok := node.(map[string]any)
	if !ok {
		return append(problems, "the release config is not a mapping")
	}
	if _, ok := root["gomod"]; ok {
		problems = append(problems, "the release config sets gomod, which can enable the module proxy")
	}
	builds, ok := yamlList(root["builds"])
	if !ok {
		problems = append(problems, "builds is not a list")
	} else if len(builds) == 0 {
		problems = append(problems, "the release config has no builds")
	}

	var hooks []string
	if before, ok := root["before"].(map[string]any); ok {
		found, bad := yamlHooks("before.hooks", before["hooks"])
		hooks, problems = append(hooks, found...), append(problems, bad...)
	} else if root["before"] != nil {
		problems = append(problems, "before is not a mapping")
	}

	global, bad := yamlEnv("the release config", map[string]string{}, root)
	problems = append(problems, bad...)
	for i, item := range builds {
		build, ok := item.(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("build %d is not a mapping", i+1))
			continue
		}
		label := fmt.Sprintf("build %d", i+1)
		if id, ok := build["id"].(string); ok {
			label = "build " + id
		}
		env, bad := yamlEnv(label, global, build)
		problems = append(problems, bad...)
		problems = append(problems, effectiveBuildProblems(label, env, build)...)

		if phases, ok := build["hooks"].(map[string]any); ok {
			for phase, commands := range phases {
				found, bad := yamlHooks(label+": hooks."+phase, commands)
				hooks, problems = append(hooks, found...), append(problems, bad...)
			}
		} else if build["hooks"] != nil {
			problems = append(problems, label+": hooks is not a mapping")
		}

		overrides, ok := yamlList(build["overrides"])
		if !ok {
			problems = append(problems, label+": overrides is not a list")
		}
		for j, entry := range overrides {
			name := fmt.Sprintf("%s, override %d", label, j+1)
			override, ok := entry.(map[string]any)
			if !ok {
				problems = append(problems, name+" is not a mapping")
				continue
			}
			effective, bad := yamlEnv(name, env, override)
			problems = append(problems, bad...)
			problems = append(problems, effectiveBuildProblems(name, effective, override)...)
		}
	}
	for _, hook := range hooks {
		if regexp.MustCompile(`\bgo (mod|get|work|install)\b`).MatchString(hook) {
			problems = append(problems, "hook "+hook+" resolves modules")
		}
	}
	return problems
}

// effectiveBuildProblems checks one build's environment after every override.
func effectiveBuildProblems(label string, env map[string]string, build map[string]any) []string {
	var problems []string
	for _, want := range [][2]string{{"GOFLAGS", "-mod=vendor"}, {"GOPROXY", "off"}} {
		if env[want[0]] != want[1] {
			problems = append(problems, fmt.Sprintf("%s: %s is %q, want %q", label, want[0], env[want[0]], want[1]))
		}
	}
	flags, ok := yamlList(build["flags"])
	if !ok {
		return append(problems, label+": flags is not a list")
	}
	for _, flag := range flags {
		text, ok := flag.(string)
		if !ok {
			problems = append(problems, label+": flags is not a list of strings")
			continue
		}
		// go accepts a flag with one dash or two.
		if strings.HasPrefix(strings.TrimLeft(text, "-"), "mod") {
			problems = append(problems, fmt.Sprintf("%s: flag %s leaves the repository mode", label, text))
		}
	}
	return problems
}

// yamlList reads an optional list; ok is false for any other shape.
func yamlList(node any) ([]any, bool) {
	if node == nil {
		return nil, true
	}
	list, ok := node.([]any)
	return list, ok
}

// yamlEnv returns base with the node's env list applied over it. An env the
// guard cannot read is a problem, so it can never hide a setting.
func yamlEnv(label string, base map[string]string, node map[string]any) (map[string]string, []string) {
	env := maps.Clone(base)
	entries, ok := yamlList(node["env"])
	if !ok {
		return env, []string{label + ": env is not a list of KEY=VALUE strings"}
	}
	var problems []string
	for _, entry := range entries {
		text, _ := entry.(string)
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			problems = append(problems, label+": env is not a list of KEY=VALUE strings")
			continue
		}
		env[key] = value
	}
	return env, problems
}

// yamlHooks returns the commands of a hook list: strings, or mappings such as
// `cmd: ...`.
func yamlHooks(label string, node any) ([]string, []string) {
	items, ok := yamlList(node)
	if !ok {
		return nil, []string{label + " is not a list"}
	}
	var commands, problems []string
	for _, item := range items {
		switch item.(type) {
		case string, map[string]any:
			commands = append(commands, yamlStrings(item)...)
		default:
			problems = append(problems, label+" is not a list of commands")
		}
	}
	return commands, problems
}

// yamlStrings lists every scalar under a node.
func yamlStrings(node any) []string {
	switch value := node.(type) {
	case string:
		return []string{value}
	case []any:
		var all []string
		for _, item := range value {
			all = append(all, yamlStrings(item)...)
		}
		return all
	case map[string]any:
		var all []string
		for _, item := range value {
			all = append(all, yamlStrings(item)...)
		}
		return all
	}
	return nil
}

type yamlLine struct {
	number, indent int
	text           string
}

// yamlReader holds the lines and collects every one it cannot read.
type yamlReader struct {
	lines    []yamlLine
	problems []string
}

var (
	yamlKey       = regexp.MustCompile(`^([A-Za-z_][\w-]*):(?:\s+(.*))?$`)
	yamlQuotedKey = regexp.MustCompile(`^("[^"]*"|'[^']*')\s*:(\s|$)`)
)

func (r *yamlReader) report(line yamlLine) {
	r.problems = append(r.problems, fmt.Sprintf("cannot read line %d of the release config: %s", line.number, line.text))
}

// parseYAML reads the block subset .goreleaser.yaml uses: mappings, lists,
// block scalars and flow lists. It fails closed: every line outside that subset
// is reported and reading continues, so nothing after it goes unseen. The
// repository takes no YAML dependency.
func parseYAML(text string) (any, []string) {
	r := &yamlReader{}
	number := 0
	for line := range strings.Lines(text) {
		number++
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entry := yamlLine{number, len(line) - len(strings.TrimLeft(line, " ")), trimmed}
		if strings.HasPrefix(strings.TrimLeft(line, " "), "\t") || trimmed == "---" || trimmed == "..." {
			r.report(entry)
			continue
		}
		r.lines = append(r.lines, entry)
	}
	if len(r.lines) == 0 {
		return nil, r.problems
	}
	node, i := r.block(0, r.lines[0].indent)
	for ; i < len(r.lines); i++ {
		r.report(r.lines[i])
	}
	return node, r.problems
}

// value reads an inline value: a scalar or a flow list.
func (r *yamlReader) value(text string, line yamlLine) any {
	switch {
	case strings.HasPrefix(text, "["):
		list, ok := parseYAMLFlowList(text)
		if !ok {
			r.report(line)
			return nil
		}
		return list
	case strings.HasPrefix(text, "{"), strings.HasPrefix(text, "&"), strings.HasPrefix(text, "*"), isYAMLItem(text):
		r.report(line)
		return nil
	}
	return strings.Trim(text, `"'`)
}

// parseYAMLFlowList reads `[a, 'b,c']`; ok is false for anything nested or
// unterminated.
func parseYAMLFlowList(text string) ([]any, bool) {
	if !strings.HasSuffix(text, "]") {
		return nil, false
	}
	list := []any{}
	add := func(item string) bool {
		item = strings.TrimSpace(item)
		if item == "" {
			return true
		}
		if strings.ContainsAny(item[:1], "[{&*") {
			return false
		}
		list = append(list, strings.Trim(item, `"'`))
		return true
	}
	inner := text[1 : len(text)-1]
	quote, start := byte(0), 0
	for i := 0; i < len(inner); i++ {
		switch c := inner[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ',':
			if !add(inner[start:i]) {
				return nil, false
			}
			start = i + 1
		}
	}
	return list, quote == 0 && add(inner[start:])
}

func isYAMLItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ")
}

func (r *yamlReader) block(i, indent int) (any, int) {
	if isYAMLItem(r.lines[i].text) {
		return r.list(i, indent)
	}
	mapping := map[string]any{}
	for i < len(r.lines) && r.lines[i].indent >= indent {
		line := r.lines[i]
		match := yamlKey.FindStringSubmatch(line.text)
		if line.indent > indent || match == nil {
			r.report(line)
			i++
			continue
		}
		key, value := match[1], match[2]
		i++
		switch {
		case strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">"):
			for i < len(r.lines) && r.lines[i].indent > indent {
				i++
			}
			mapping[key] = ""
		case value != "":
			mapping[key] = r.value(value, line)
		case i < len(r.lines) && (r.lines[i].indent > indent || (r.lines[i].indent == indent && isYAMLItem(r.lines[i].text))):
			mapping[key], i = r.block(i, r.lines[i].indent)
		default:
			mapping[key] = nil
		}
	}
	return mapping, i
}

func (r *yamlReader) list(i, indent int) (any, int) {
	list := []any{}
	for i < len(r.lines) && r.lines[i].indent == indent && isYAMLItem(r.lines[i].text) {
		line := r.lines[i]
		rest := strings.TrimSpace(strings.TrimPrefix(line.text, "-"))
		switch {
		case yamlKey.MatchString(rest):
			r.lines[i] = yamlLine{line.number, indent + 2, rest}
			var item any
			item, i = r.block(i, indent+2)
			list = append(list, item)
		case yamlQuotedKey.MatchString(rest):
			r.report(line)
			i++
		default:
			list = append(list, r.value(rest, line))
			i++
		}
	}
	return list, i
}

func TestParseYAMLShapes(t *testing.T) {
	const text = "# comment\na: 1\nb:\n  - x\n  - 'y'\n  - k: v\n    n:\n      - z\nc: |\n  block\n  more\nd:\ne:\n- same\n"
	node, problems := parseYAML(text)
	if len(problems) != 0 {
		t.Errorf("problems on clean text: %v", problems)
	}
	got, _ := node.(map[string]any)
	if got["a"] != "1" || got["c"] != "" || got["d"] != nil {
		t.Errorf("scalars: %v", got)
	}
	b, _ := got["b"].([]any)
	if len(b) != 3 || b[0] != "x" || b[1] != "y" {
		t.Fatalf("b = %v", b)
	}
	if item, _ := b[2].(map[string]any); item["k"] != "v" || !slices.Equal(yamlStrings(item["n"]), []string{"z"}) {
		t.Errorf("nested item = %v", b[2])
	}
	if e, _ := got["e"].([]any); len(e) != 1 || e[0] != "same" {
		t.Errorf("list at the key's indent = %v", got["e"])
	}
	if empty, problems := parseYAML("\n# only a comment\n"); empty != nil || len(problems) != 0 {
		t.Errorf("empty text = %v, %v", empty, problems)
	}
	node, problems = parseYAML("a: 1\nnot a key\nb: 2\n")
	if got, _ := node.(map[string]any); got["a"] != "1" || got["b"] != "2" {
		t.Errorf("reading stopped at a line it could not read: %v", node)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "line 2") {
		t.Errorf("problems = %v", problems)
	}
	if list, ok := parseYAMLFlowList(`[a, "b,c", ]`); !ok || !slices.Equal(list, []any{"a", "b,c"}) {
		t.Errorf("flow list = %v, %v", list, ok)
	}
	for _, bad := range []string{`[a`, `[[a]]`, `[a, {b: c}]`, `[a, "b]`, `[*a, b]`} {
		if list, ok := parseYAMLFlowList(bad); ok {
			t.Errorf("%s read as %v", bad, list)
		}
	}
	if got := releaseConfigProblems("- a\n"); !slices.Contains(got, "the release config is not a mapping") {
		t.Errorf("a list at the top: %v", got)
	}
	if got := strings.Join(releaseConfigProblems("env:\n  - GOFLAGS=-mod=vendor\n  - GOPROXY=off\nbuilds:\n  - id: a\n    flags:\n      - x: -mod=mod\n"), "\n"); !strings.Contains(got, "flags is not a list of strings") {
		t.Errorf("a flag that is a mapping: %v", got)
	}
	if got := strings.Join(releaseConfigProblems("env:\n  - GOFLAGS=-mod=vendor\n  - GOPROXY=off\nbuilds:\n  - env:\n      - GOFLAGS=x\n"), "\n"); !strings.Contains(got, "build 1: GOFLAGS") {
		t.Errorf("a build without an id: %v", got)
	}
}

func TestReleaseBuildsInRepositoryMode(t *testing.T) {
	for _, problem := range releaseConfigProblems(readRepoFile(t, ".goreleaser.yaml")) {
		t.Error(problem)
	}
}

func TestReleaseConfigProblemsCatchesEachBreak(t *testing.T) {
	const global = "env:\n  - GOFLAGS=-mod=vendor\n  - GOPROXY=off\n"
	const builds = "builds:\n  - id: a\n    env:\n      - CGO_ENABLED=1\n    overrides:\n      - goos: linux\n        env:\n          - CC=x\n"
	if got := releaseConfigProblems(global + builds); len(got) != 0 {
		t.Errorf("clean config: %v", got)
	}
	for _, tc := range []struct{ name, config, want string }{
		{"no builds", global, "no builds"},
		{"no global env", builds, "build a: GOFLAGS is"},
		{"no proxy setting", "env:\n  - GOFLAGS=-mod=vendor\n" + builds, "build a: GOPROXY is"},
		{"commented out", "env:\n  # - GOFLAGS=-mod=vendor\n  - GOPROXY=off\n" + builds, "build a: GOFLAGS is"},
		{"nested under another key", "before:\n  env:\n    - GOFLAGS=-mod=vendor\n    - GOPROXY=off\n" + builds, "build a: GOFLAGS is"},
		{"build overrides GOFLAGS", global + "builds:\n  - id: a\n    env:\n      - GOFLAGS=-mod=mod\n", `build a: GOFLAGS is "-mod=mod"`},
		{"build overrides GOPROXY", global + "builds:\n  - id: a\n    env:\n      - GOPROXY=https://proxy.golang.org\n", `build a: GOPROXY is "https://proxy.golang.org"`},
		{"target override re-enables", global + "builds:\n  - id: a\n    overrides:\n      - goos: linux\n        env:\n          - GOPROXY=direct\n", `build a, override 1: GOPROXY is "direct"`},
		{"build flag", global + "builds:\n  - id: a\n    flags:\n      - -mod=mod\n", "build a: flag -mod=mod"},
		{"override flag", global + "builds:\n  - id: a\n    overrides:\n      - goos: linux\n        flags:\n          - -modfile=x\n", "build a, override 1: flag -modfile=x"},
		{"proxy pipe", global + builds + "gomod:\n  proxy: true\n", "gomod"},
		{"tidy hook", global + builds + "before:\n  hooks:\n    - go mod tidy\n", "hook go mod tidy"},
		{"build hook", global + "builds:\n  - id: a\n    hooks:\n      pre:\n        - go get x\n", "hook go get x"},
		// Flow lists are read, so what they set is checked.
		{"flow env on a build", global + "builds:\n  - id: a\n    env: [GOFLAGS=-mod=mod]\n", `build a: GOFLAGS is "-mod=mod"`},
		{"flow env on an override", global + "builds:\n  - id: a\n    overrides:\n      - goos: linux\n        env: [GOPROXY=direct]\n", `build a, override 1: GOPROXY is "direct"`},
		{"flow env at the top", "env: [GOFLAGS=-mod=mod, GOPROXY=off]\n" + builds, `build a: GOFLAGS is "-mod=mod"`},
		{"flow flags", global + "builds:\n  - id: a\n    flags: [-trimpath, -mod=mod]\n", "build a: flag -mod=mod"},
		{"flow hooks", global + "builds:\n  - id: a\n    hooks:\n      pre: [go mod tidy]\n", "hook go mod tidy"},
		// A flag go accepts with two dashes.
		{"double-dash flag", global + "builds:\n  - id: a\n    flags:\n      - --mod=mod\n", "build a: flag --mod=mod"},
		{"double-dash flow flag", global + "builds:\n  - id: a\n    overrides:\n      - goos: linux\n        flags: [--modfile=x]\n", "build a, override 1: flag --modfile=x"},
		// A node of a shape the guard cannot check is rejected.
		{"builds not a list", global + "builds: a\n", "builds is not a list"},
		{"build not a mapping", global + "builds:\n  - a\n", "build 1 is not a mapping"},
		{"env a scalar", "env: GOFLAGS=-mod=vendor\n" + builds, "env is not a list"},
		{"env a mapping", global + "builds:\n  - id: a\n    env:\n      GOFLAGS: -mod=mod\n", "build a: env is not a list"},
		{"env item without a value", global + "builds:\n  - id: a\n    env:\n      - GOFLAGS\n", "build a: env is not a list"},
		{"override env a scalar", global + "builds:\n  - id: a\n    overrides:\n      - goos: linux\n        env: GOPROXY=direct\n", "build a, override 1: env is not a list"},
		{"flags a scalar", global + "builds:\n  - id: a\n    flags: -mod=mod\n", "build a: flags is not a list"},
		{"flags a mapping", global + "builds:\n  - id: a\n    flags:\n      x: -mod=mod\n", "build a: flags is not a list"},
		{"overrides a scalar", global + "builds:\n  - id: a\n    overrides: x\n", "build a: overrides is not a list"},
		{"override not a mapping", global + "builds:\n  - id: a\n    overrides:\n      - x\n", "build a, override 1 is not a mapping"},
		{"before a scalar", global + builds + "before: x\n", "before is not a mapping"},
		{"before hooks a scalar", global + builds + "before:\n  hooks: go mod tidy\n", "before.hooks is not a list"},
		{"build hooks a scalar", global + "builds:\n  - id: a\n    hooks: x\n", "build a: hooks is not a mapping"},
		{"build hook phase a scalar", global + "builds:\n  - id: a\n    hooks:\n      pre: go mod tidy\n", "build a: hooks.pre is not a list"},
		{"hook item a list", global + builds + "before:\n  hooks:\n    - [go mod tidy]\n", "before.hooks is not a list"},
		{"nested block list", global + builds + "before:\n  hooks:\n    - - go mod tidy\n", "cannot read"},
		// What the reader cannot parse is reported, and reading goes on.
		{"quoted key", global + "builds:\n  - id: a\n    \"env\":\n      - GOFLAGS=-mod=mod\n", "cannot read"},
		{"quoted key on an item", global + "builds:\n  - \"env\": x\n", "cannot read"},
		{"quoted key does not hide what follows", global + builds + "\"x\": 1\ngomod:\n  proxy: true\n", "gomod"},
		{"not a key", global + builds + "not a key\n", "cannot read"},
		{"a tab", global + builds + "\tgomod: x\n", "cannot read"},
		{"flow mapping", global + "builds:\n  - {id: a, env: [GOFLAGS=-mod=mod]}\n", "cannot read"},
		{"flow mapping value", global + "builds:\n  - id: a\n    env: {GOFLAGS: x}\n", "cannot read"},
		{"unterminated flow list", global + "builds:\n  - id: a\n    env: [GOFLAGS=x\n", "cannot read"},
		{"nested flow list", global + "builds:\n  - id: a\n    env: [[GOFLAGS=x]]\n", "cannot read"},
		{"anchor", global + "builds:\n  - id: a\n    env: &shared\n      - GOFLAGS=-mod=mod\n", "cannot read"},
		{"alias", global + "builds:\n  - id: a\n    env: *shared\n", "cannot read"},
		{"merge key", global + "builds:\n  - id: a\n    <<: x\n", "cannot read"},
		{"second document", global + builds + "---\ngomod: x\n", "cannot read"},
		{"a line indented too far", global + "builds:\n  - id: a\n      env:\n        - GOFLAGS=-mod=mod\n", "cannot read"},
		{"a line indented too little", "  a: 1\nb: 2\n", "cannot read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(releaseConfigProblems(tc.config), "\n"); !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
	if got := releaseConfigProblems(global + "builds:\n  - id: a\n    env: [CGO_ENABLED=1, 'X=a,b', ]\n    flags: [-trimpath]\n    hooks:\n      pre: [echo]\n      post:\n        - cmd: echo\nbefore:\n  hooks:\n    - make web\n    - cmd: echo\n"); len(got) != 0 {
		t.Errorf("a clean config in flow style has problems: %v", got)
	}
}

// The reviewer's plants, on the real file: each changes what goreleaser builds
// with, so each must fail however it is spelled.
func TestReleaseGuardCatchesPlantsOnTheRealConfig(t *testing.T) {
	const buildEnv = "    env:\n      - CGO_ENABLED=1\n"
	const overrideEnv = "        env:\n          - CC=clang -arch x86_64\n          - CXX=clang++ -arch x86_64\n          - MACOSX_DEPLOYMENT_TARGET=13.0\n"
	real := readRepoFile(t, ".goreleaser.yaml")
	for _, tc := range []struct{ name, old, new, want string }{
		{"flow env on the build", buildEnv, "    env: [CGO_ENABLED=1, GOFLAGS=-mod=mod, GOPROXY=https://proxy.golang.org]\n", `GOFLAGS is "-mod=mod"`},
		{"flow env on a target override", overrideEnv, "        env: [GOFLAGS=-mod=mod, GOPROXY=direct]\n", `override 1: GOFLAGS is "-mod=mod"`},
		{"quoted env key", buildEnv, "    \"env\":\n      - CGO_ENABLED=1\n      - GOFLAGS=-mod=mod\n", "cannot read"},
		{"double-dash flag", "    flags:\n      - -trimpath\n", "    flags: [-trimpath, --mod=mod]\n", "flag --mod=mod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(real, tc.old) {
				t.Fatalf(".goreleaser.yaml does not contain %q", tc.old)
			}
			got := strings.Join(releaseConfigProblems(strings.Replace(real, tc.old, tc.new, 1)), "\n")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// makeFakes puts a logging go and staticcheck on the path and returns the log.
func makeFakes(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	for _, name := range []string{"go", "staticcheck", "govulncheck", "goreleaser"} {
		script := "#!/bin/bash\necho \"" + name + "|$GOFLAGS|$GOPROXY|$GOWORK|$*\" >>\"" + log + "\"\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TOOLS_BIN", bin)
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "https://proxy.golang.org")
	return log
}

func loggedCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// Every check runs in the repository mode, whatever the caller's environment.
func TestMakeChecksRunInRepositoryMode(t *testing.T) {
	for _, target := range []string{"test", "lint", "guard-report", "check-sdk", "check-conformance"} {
		t.Run(target, func(t *testing.T) {
			log := makeFakes(t)
			out, err := runMakeTarget(t, target, map[string]string{"sdk/x": "", "sdk/conformance/x": ""})
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			for _, call := range loggedCalls(t, log) {
				if !strings.Contains(call, "|-mod=vendor|off|") {
					t.Errorf("not in the repository mode: %s", call)
				}
			}
		})
	}
}

func TestChecksRunPinnedToolsOutsideGoRun(t *testing.T) {
	log := makeFakes(t)
	if out, err := runMakeTarget(t, "lint", nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	calls := strings.Join(loggedCalls(t, log), "\n")
	if strings.Contains(calls, "go|-mod=vendor|off||run") {
		t.Errorf("lint resolved a tool with go run:\n%s", calls)
	}
	if !strings.Contains(calls, "staticcheck|") {
		t.Errorf("lint did not run the installed staticcheck:\n%s", calls)
	}
}

func TestMissingToolNamesTheInstallStep(t *testing.T) {
	makeFakes(t)
	t.Setenv("TOOLS_BIN", t.TempDir())
	out, err := runMakeTarget(t, "lint", nil)
	if err == nil || !strings.Contains(out, "make tools-go") {
		t.Fatalf("want a failure naming make tools-go, got %v:\n%s", err, out)
	}
}

// Maintenance is the one mode that reaches a proxy, and it says so explicitly.
func TestToolsGoInstallsPinnedVersionsInMaintenanceMode(t *testing.T) {
	log := makeFakes(t)
	if out, err := runMakeTarget(t, "tools-go", nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	calls := loggedCalls(t, log)
	if len(calls) != 3 {
		t.Fatalf("want three installs, got %v", calls)
	}
	for _, call := range calls {
		if !strings.HasPrefix(call, "go||https://proxy.golang.org,direct|off|install ") || !regexp.MustCompile(`@v\d+\.\d+\.\d+$`).MatchString(call) {
			t.Errorf("not a pinned maintenance install: %s", call)
		}
	}
}

func TestVendorRunsInMaintenanceMode(t *testing.T) {
	log := makeFakes(t)
	if out, err := runMakeTarget(t, "vendor", map[string]string{"third_party/vendored/.keep": ""}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if calls := loggedCalls(t, log); len(calls) != 1 || !strings.HasPrefix(calls[0], "go||https://proxy.golang.org,direct||work vendor") {
		t.Errorf("calls = %v", calls)
	}
}

// A local replace hides a module from govulncheck, so the one online step asks
// OSV about every pin as well, and the release gate reaches it.
func TestVulnRunsGovulncheckAndTheAdvisoryScript(t *testing.T) {
	log := makeFakes(t)
	const script = ".github/scripts/vendor-advisories.sh"
	out, err := runMakeTarget(t, "vuln", map[string]string{script: "echo osv-asked\n"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "osv-asked") {
		t.Errorf("vuln did not run the advisory script:\n%s", out)
	}
	if calls := strings.Join(loggedCalls(t, log), "\n"); !strings.Contains(calls, "govulncheck|") {
		t.Errorf("vuln did not run govulncheck:\n%s", calls)
	}
	if out, err := runMakeTarget(t, "vuln", map[string]string{script: "exit 1\n"}); err == nil {
		t.Errorf("vuln passed on a failing advisory script:\n%s", out)
	}
}

func TestReleaseGateReachesTheAdvisories(t *testing.T) {
	seen := false
	for line := range strings.Lines(readRepoFile(t, "Makefile")) {
		if strings.HasPrefix(line, "check-root:") && slices.Contains(strings.Fields(line), "vuln") {
			seen = true
		}
	}
	if !seen {
		t.Error("check-root does not run vuln, so release.yml's make check skips it")
	}
	if !workflowRuns(readRepoFile(t, ".github/workflows/vuln.yml"), "vuln") {
		t.Error("vuln.yml does not run make vuln")
	}
}

func TestWorkflowsInstallToolsBeforeChecks(t *testing.T) {
	for _, name := range []string{"tests.yml", "vuln.yml"} {
		if !workflowRuns(readRepoFile(t, ".github/workflows/"+name), "tools-go") {
			t.Errorf("%s does not install the pinned Go tools", name)
		}
	}
}

// depsFixture is a three-module workspace with one replaced dependency and a
// committed vendored tree, so the maintenance target runs with no network.
func depsFixture(t *testing.T) (string, []string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	for name, content := range map[string]string{
		"Makefile":               readRepoFile(t, "Makefile"),
		"MODULES.md":             readRepoFile(t, "MODULES.md"),
		"go.work":                "go 1.26.0\n\nuse (\n\t.\n\t./sdk\n\t./sdk/conformance\n)\n",
		"go.mod":                 "module example.test/root\n\ngo 1.26.0\n\nrequire (\n\texample.test/dep v0.0.0\n\texample.test/note v0.0.0\n)\n\nreplace example.test/dep => ./third_party/dep\n\nreplace example.test/note => ./note\n",
		"main.go":                "package main\n\nimport (\n\t_ \"example.test/dep\"\n\t_ \"example.test/note\"\n)\n\nfunc main() {}\n",
		"sdk/go.mod":             "module example.test/root/sdk\n\ngo 1.26.0\n",
		"sdk/a.go":               "package sdk\n",
		"sdk/conformance/go.mod": "module example.test/root/sdk/conformance\n\ngo 1.26.0\n",
		"sdk/conformance/a.go":   "package conformance\n",
		"third_party/dep/go.mod": "module example.test/dep\n\ngo 1.26.0\n",
		"third_party/dep/dep.go": "package dep\n",
		"third_party/vendored/example.test/note/VENDOR.md": "kept across a re-vendor\n",
		"note/note.go": "package note\n",
	} {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	// The noted module is a second dependency, so a re-vendor has a directory
	// to restore the VENDOR.md template into.
	writeFile(t, filepath.Join(root, "note", "go.mod"), "module example.test/note\n\ngo 1.26.0\n")

	gitConfig := filepath.Join(base, "gitconfig")
	writeFile(t, gitConfig, "[user]\n\tname = fixture\n\temail = fixture@example.com\n")
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL="+gitConfig, "GIT_CONFIG_NOSYSTEM=1", "GOTOOLCHAIN=local")
	git(t, root, env, "init", "--quiet")
	if out, err := runDepsMake(root, env, "vendor"); err != nil {
		t.Fatalf("make vendor: %v\n%s", err, out)
	}
	commitAll(t, root, env, "tree")
	return root, env
}

func commitAll(t *testing.T, root string, env []string, message string) {
	t.Helper()
	git(t, root, env, "add", ".")
	git(t, root, env, "commit", "--quiet", "-m", message)
}

// runDepsMake runs a target with the proxy off: the fixture needs no network.
func runDepsMake(root string, env []string, target string) (string, error) {
	cmd := exec.Command("make", "--no-print-directory", target, "DEPS_GOPROXY=off")
	cmd.Dir = root
	for _, entry := range env {
		if !strings.HasPrefix(entry, "MAKEFLAGS=") && !strings.HasPrefix(entry, "MFLAGS=") && !strings.HasPrefix(entry, "MAKELEVEL=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestDepsVerifyPassesOnACurrentTree(t *testing.T) {
	root, env := depsFixture(t)
	if _, err := os.Stat(filepath.Join(root, "vendor/example.test/note/VENDOR.md")); err != nil {
		t.Fatalf("make vendor did not restore the template: %v", err)
	}
	if out, err := runDepsMake(root, env, "deps-verify"); err != nil {
		t.Fatalf("deps-verify failed on a current tree: %v\n%s", err, out)
	}
}

func TestDepsVerifyFailsOnAStaleModulesTxt(t *testing.T) {
	root, env := depsFixture(t)
	file := filepath.Join(root, "vendor", "modules.txt")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, strings.Replace(string(data), "example.test/dep", "example.test/stale", 1))
	commitAll(t, root, env, "stale")
	out, err := runDepsMake(root, env, "deps-verify")
	if err == nil {
		t.Fatalf("deps-verify passed on a stale modules.txt:\n%s", out)
	}
	if !strings.Contains(out, "vendor/modules.txt") {
		t.Fatalf("deps-verify did not name the stale file:\n%s", out)
	}
}

func TestDepsVerifyFailsOnAnUntrackedVendorFile(t *testing.T) {
	root, env := depsFixture(t)
	if err := os.Remove(filepath.Join(root, "vendor/example.test/dep/dep.go")); err != nil {
		t.Fatal(err)
	}
	commitAll(t, root, env, "deleted")
	if out, err := runDepsMake(root, env, "deps-verify"); err == nil {
		t.Fatalf("deps-verify passed when a re-vendor added a file back:\n%s", out)
	}
}

// The workspace resolves a root-only dependency for sdk/ too, so only the
// module's own tidy shows the import its go.mod does not carry.
func TestDepsVerifyFailsOnAnSDKImportOfARootOnlyDependency(t *testing.T) {
	root, env := depsFixture(t)
	writeFile(t, filepath.Join(root, "sdk", "planted.go"), "package sdk\n\nimport _ \"example.test/dep\"\n")
	commitAll(t, root, env, "planted")
	out, err := runDepsMake(root, env, "deps-verify")
	if err == nil {
		t.Fatalf("deps-verify passed on the planted import:\n%s", out)
	}
	if !strings.Contains(out, "example.test/dep") {
		t.Fatalf("deps-verify did not name the dependency:\n%s", out)
	}
}

func TestCIRunsDepsVerify(t *testing.T) {
	if !workflowRuns(readRepoFile(t, ".github/workflows/tests.yml"), "deps-verify") {
		t.Error("tests.yml has no step that runs make deps-verify")
	}
}

// Upstream code keeps its own formatting: reformatting would break its sum.
func TestLintFormatSkipsPinnedCopies(t *testing.T) {
	const misformatted = "package x\nfunc  F() {}\n"
	out, err := runMakeTarget(t, "lint-fmt", map[string]string{"third_party/x/x.go": misformatted, "vendor/x/x.go": misformatted, "ok/ok.go": "package ok\n"})
	if err != nil {
		t.Fatalf("lint-fmt failed on pinned copies: %v\n%s", err, out)
	}
	out, err = runMakeTarget(t, "lint-fmt", map[string]string{"own/x.go": misformatted})
	if err == nil || !strings.Contains(out, "x.go") {
		t.Fatalf("lint-fmt missed the repository's own file: %v\n%s", err, out)
	}
}

const advisoryScript = ".github/scripts/vendor-advisories.sh"

// runAdvisories runs the job's script on a tree against a fake OSV endpoint
// that reports an advisory for the affected version only.
func runAdvisories(t *testing.T, root, affected string) (string, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var query struct {
			Version string `json:"version"`
			Package struct {
				Name, Ecosystem string
			} `json:"package"`
		}
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil || query.Package.Ecosystem != "Go" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		if query.Version == affected {
			w.Write([]byte(`{"vulns":[{"id":"GO-0000-0001"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	return runAdvisoryScript(t, root, server.URL)
}

func runAdvisoryScript(t *testing.T, root, url string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(repoRoot, filepath.FromSlash(advisoryScript)))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, root)
	cmd.Env = append(os.Environ(), "OSV_API="+url)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func advisoryTree(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "third_party/dep/VENDOR.md"), fixtureVendorMD("example.test/dep", version, "h1:x"))
	writeFile(t, filepath.Join(root, "vendor/example.test/kept/VENDOR.md"), fixtureVendorMD("example.test/kept", "v1.2.0", ""))
	return root
}

func TestAdvisoryJobFailsOnAPlantedOldPin(t *testing.T) {
	out, err := runAdvisories(t, advisoryTree(t, "v0.9.0"), "v0.9.0")
	if err == nil {
		t.Fatalf("the advisory job passed on an old pin:\n%s", out)
	}
	if !strings.Contains(out, "example.test/dep") || !strings.Contains(out, "GO-0000-0001") {
		t.Fatalf("the failure names neither the module nor the advisory:\n%s", out)
	}
}

func TestAdvisoryJobPassesOnCurrentPins(t *testing.T) {
	out, err := runAdvisories(t, advisoryTree(t, "v1.0.0"), "v0.9.0")
	if err != nil {
		t.Fatalf("clean pins failed: %v\n%s", err, out)
	}
	for _, module := range []string{"example.test/dep", "example.test/kept"} {
		if !strings.Contains(out, module) {
			t.Errorf("the job did not report %s:\n%s", module, out)
		}
	}
}

// An unanswered query is not a clean result.
func TestAdvisoryJobFailsWhenOSVIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer server.Close()
	if out, err := runAdvisoryScript(t, advisoryTree(t, "v1.0.0"), server.URL); err == nil {
		t.Fatalf("the job passed with OSV down:\n%s", out)
	}
}

func TestAdvisoryJobFailsWithoutPins(t *testing.T) {
	out, err := runAdvisoryScript(t, t.TempDir(), "http://127.0.0.1:1")
	if err == nil {
		t.Fatalf("the job passed with no VENDOR.md:\n%s", out)
	}
	if !strings.Contains(out, "no VENDOR.md") {
		t.Fatalf("the job failed without saying why:\n%s", out)
	}
}

func TestAdvisoryJobFailsOnAPinItCannotRead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "third_party/dep/VENDOR.md"), "# no fields\n")
	out, err := runAdvisoryScript(t, root, "http://127.0.0.1:1")
	if err == nil {
		t.Fatalf("the job passed on an unreadable pin:\n%s", out)
	}
	if !strings.Contains(out, "no Module or Version") {
		t.Fatalf("the job failed without saying why:\n%s", out)
	}
}

const reproducibleScript = ".github/scripts/reproducible-build.sh"

// fakeGoreleaser stands in for the release build. Its behaviour on the second
// run depends on mode, and it leaves a dist/ file that always differs and that
// is not an artifact, so only the binaries can decide the result.
const fakeGoreleaser = `#!/bin/bash
echo "$*|$GOCACHE" >>calls.log
runs=$(($(wc -l <calls.log)))
rm -rf dist
mkdir -p dist/a_linux dist/b_darwin
echo "$RANDOM" >dist/metadata.json
case "$FAKE_MODE:$runs" in
none:*) exit 0 ;;
differ:2) echo two >dist/b_darwin/herdsman ;;
*) echo same >dist/b_darwin/herdsman ;;
esac
if [ "$FAKE_MODE:$runs" != missing:2 ]; then echo same >dist/a_linux/herdsman; fi
`

func runReproducible(t *testing.T, mode string, args ...string) (string, string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(repoRoot, filepath.FromSlash(reproducibleScript)))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-goreleaser")
	if err := os.WriteFile(fake, []byte(fakeGoreleaser), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GORELEASER="+fake, "FAKE_MODE="+mode)
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	return string(out), string(calls), err
}

func TestReproducibleBuildRunsTheReleaseConfigTwice(t *testing.T) {
	out, calls, err := runReproducible(t, "same", "--single-target")
	if err != nil {
		t.Fatalf("identical builds failed: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(calls), "\n")
	if len(lines) != 2 {
		t.Fatalf("want two release builds, got %q", calls)
	}
	first, firstCache, _ := strings.Cut(lines[0], "|")
	second, secondCache, _ := strings.Cut(lines[1], "|")
	if first != "build --snapshot --clean --single-target" || second != first {
		t.Errorf("builds = %q, %q", first, second)
	}
	if firstCache == "" || firstCache == secondCache {
		t.Errorf("the builds shared a cache: %q, %q", firstCache, secondCache)
	}
}

func TestReproducibleBuildFailsOnADifference(t *testing.T) {
	for mode, want := range map[string]string{"differ": "differ", "missing": "differ", "none": "no release binaries"} {
		t.Run(mode, func(t *testing.T) {
			out, _, err := runReproducible(t, mode)
			if err == nil {
				t.Fatalf("passed:\n%s", out)
			}
			if !strings.Contains(out, want) {
				t.Fatalf("want %q in:\n%s", want, out)
			}
		})
	}
}

// The job must rebuild every release target, so it needs the release's runner
// and compilers, and must not fall back to a plain go build.
func TestReproducibleJobUsesTheReleaseBuild(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/reproducible.yml")
	for _, want := range []string{"macos-latest", "setup-zig@", "GORELEASER:", ".github/scripts/reproducible-build.sh"} {
		if !strings.Contains(workflow, want) {
			t.Errorf("reproducible.yml lacks %q", want)
		}
	}
	if strings.Contains(workflow, "--single-target") || strings.Contains(workflow, "./cmd/herdsman") {
		t.Error("reproducible.yml does not rebuild every release target")
	}
}

// watchProblems returns the pins neither bot would see.
func watchProblems(dependabot, renovate string, moduleDirs []string) []string {
	var problems []string
	for _, dir := range moduleDirs {
		want := strings.TrimPrefix(dir, ".")
		if want == "" {
			want = "/"
		}
		if !regexp.MustCompile(`(?m)^\s+(- )?directory: "?` + regexp.QuoteMeta(want) + `"?\s*$`).MatchString(dependabot) {
			problems = append(problems, "dependabot does not watch "+want)
		}
	}
	if !strings.Contains(dependabot, "package-ecosystem: github-actions") {
		problems = append(problems, "dependabot does not watch the workflow actions")
	}
	var config struct {
		CustomManagers []struct {
			ManagedFilesRE []string `json:"managerFilePatterns"`
			Datasource     string   `json:"datasourceTemplate"`
		} `json:"customManagers"`
	}
	if err := json.Unmarshal([]byte(renovate), &config); err != nil {
		return append(problems, "renovate.json: "+err.Error())
	}
	for _, name := range []string{"third_party/qr/VENDOR.md", "third_party/vendored/golang.org/x/sys/VENDOR.md"} {
		seen := false
		for _, manager := range config.CustomManagers {
			for _, pattern := range manager.ManagedFilesRE {
				re, err := regexp.Compile(strings.Trim(pattern, "/"))
				if err == nil && re.MatchString(name) && manager.Datasource == "go" {
					seen = true
				}
			}
		}
		if !seen {
			problems = append(problems, "renovate does not watch "+name)
		}
	}
	return problems
}

func TestBotsWatchEveryPin(t *testing.T) {
	for _, problem := range watchProblems(readRepoFile(t, ".github/dependabot.yml"), readRepoFile(t, "renovate.json"), workspaceDirs(readRepoFile(t, "go.work"))) {
		t.Error(problem)
	}
}

func TestWatchProblemsCatchesEachBreak(t *testing.T) {
	const dependabot = "updates:\n  - package-ecosystem: gomod\n    directory: /\n  - package-ecosystem: github-actions\n    directory: /\n"
	const renovate = `{"customManagers":[{"managerFilePatterns":["/(^|/)VENDOR\\.md$/"],"datasourceTemplate":"go"}]}`
	if got := watchProblems(dependabot, renovate, []string{"."}); len(got) != 0 {
		t.Errorf("clean config: %v", got)
	}
	for _, tc := range []struct {
		name, dependabot, renovate, want string
	}{
		{"nested module", dependabot, renovate, "dependabot does not watch /sdk"},
		{"actions", "updates:\n  - package-ecosystem: gomod\n    directory: /\n    directory: /sdk\n", renovate, "workflow actions"},
		{"no manager", dependabot + "    directory: /sdk\n", `{}`, "renovate does not watch"},
		{"wrong datasource", dependabot + "    directory: /sdk\n", `{"customManagers":[{"managerFilePatterns":["/VENDOR/"],"datasourceTemplate":"npm"}]}`, "renovate does not watch"},
		{"bad json", dependabot + "    directory: /sdk\n", `{`, "renovate.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(watchProblems(tc.dependabot, tc.renovate, []string{".", "./sdk"}), "\n")
			if !strings.Contains(got, tc.want) {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// adrProblems checks the record folder: the number of records, each record's
// shape, and that the index and the folder name the same files.
func adrProblems(want int, files map[string]string, index string) []string {
	var problems []string
	if len(files) != want {
		problems = append(problems, fmt.Sprintf("%d decision records, want %d", len(files), want))
	}
	heading := regexp.MustCompile(`^# (\d{4}): \S`)
	for name, content := range files {
		match := heading.FindStringSubmatch(content)
		switch {
		case match == nil:
			problems = append(problems, name+" has no `# NNNN: title` heading")
		case !strings.HasPrefix(name, match[1]+"-"):
			problems = append(problems, name+" is headed "+match[1])
		}
		for _, section := range []string{"## Context", "## Decision", "## Consequences"} {
			if !strings.Contains(content, section+"\n") {
				problems = append(problems, name+" has no "+section)
			}
		}
		if !strings.Contains(index, "("+name+")") {
			problems = append(problems, "the index does not list "+name)
		}
	}
	for _, link := range regexp.MustCompile(`\((\d{4}-[^)]+\.md)\)`).FindAllStringSubmatch(index, -1) {
		if _, ok := files[link[1]]; !ok {
			problems = append(problems, "the index lists "+link[1]+", which does not exist")
		}
	}
	return problems
}

// adrRecordCount is the number of decisions recorded. A new decision raises it
// and adds its record together.
const adrRecordCount = 31

func TestEveryDecisionRecordIsIndexed(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join(repoRoot, "docs/adr/[0-9]*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, entry := range entries {
		data, err := os.ReadFile(entry)
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.Base(entry)] = string(data)
	}
	for _, problem := range adrProblems(adrRecordCount, files, readRepoFile(t, "docs/adr/README.md")) {
		t.Error(problem)
	}
}

func TestADRProblemsCatchesEachBreak(t *testing.T) {
	const body = "\n\nStatus: accepted\n\n## Context\n\nc\n\n## Decision\n\nd\n\n## Consequences\n\ne\n"
	const record = "# 0001: A title" + body
	files := map[string]string{"0001-a-title.md": record}
	const index = "[0001](0001-a-title.md)"
	if got := adrProblems(1, files, index); len(got) != 0 {
		t.Errorf("clean: %v", got)
	}
	for _, tc := range []struct {
		name  string
		files map[string]string
		index string
		want  string
	}{
		{"a record removed", map[string]string{}, index, "0 decision records, want 1"},
		{"a record added", map[string]string{"0001-a-title.md": record, "0002-b.md": "# 0002: B" + body}, index + "(0002-b.md)", "2 decision records, want 1"},
		{"missing index line", files, "", "the index does not list 0001-a-title.md"},
		{"index names a missing file", files, index + " [0002](0002-gone.md)", "the index lists 0002-gone.md, which does not exist"},
		{"no title", map[string]string{"0001-a-title.md": body}, index, "0001-a-title.md has no `# NNNN: title` heading"},
		{"number differs from the file", map[string]string{"0001-a-title.md": "# 0002: A title" + body}, index, "0001-a-title.md is headed 0002"},
		{"no context", map[string]string{"0001-a-title.md": strings.Replace(record, "## Context", "## Other", 1)}, index, "0001-a-title.md has no ## Context"},
		{"no decision", map[string]string{"0001-a-title.md": strings.Replace(record, "## Decision", "## Other", 1)}, index, "0001-a-title.md has no ## Decision"},
		{"no consequences", map[string]string{"0001-a-title.md": strings.Replace(record, "## Consequences", "## Other", 1)}, index, "0001-a-title.md has no ## Consequences"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(adrProblems(1, tc.files, tc.index), "\n"); !strings.Contains(got, tc.want) {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}
