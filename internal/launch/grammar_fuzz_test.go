package launch

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/wrapper.golden")

func mustCompile(t *testing.T, line string) Spec {
	t.Helper()
	spec, err := Compile(line, Options{Home: "/home/u"})
	if err != nil {
		t.Fatalf("Compile(%q): %v", line, err)
	}
	return spec
}

func cmdOf(argv ...string) Command { return Command{Argv: argv} }

func TestCompileAccepted(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Spec
	}{
		{"plain program", "codex", Spec{Command: []string{"codex"}}},
		{"arguments", "claude --dangerously-skip-permissions -p x",
			Spec{Command: []string{"claude", "--dangerously-skip-permissions", "-p", "x"}}},
		{"prelude then agent", "claude update && claude --flag", Spec{
			Prelude: []Step{{Command: cmdOf("claude", "update")}},
			Command: []string{"claude", "--flag"},
		}},
		{"no blanks around the operator", "a&&b", Spec{
			Prelude: []Step{{Command: cmdOf("a")}},
			Command: []string{"b"},
		}},
		{"tabs separate words", "a\tb", Spec{Command: []string{"a", "b"}}},
		{"single quotes are literal", `a '$x \ "y"'`, Spec{Command: []string{"a", `$x \ "y"`}}},
		{"double quote escapes", `a "b\"c\\d\$e\` + "`" + `f"`, Spec{Command: []string{"a", `b"c\d$e` + "`f"}}},
		{"other double quote backslash stays", `a "b\nc"`, Spec{Command: []string{"a", `b\nc`}}},
		{"adjacent parts join", `a b'c'"d"e`, Spec{Command: []string{"a", "bcde"}}},
		{"empty argument", `a ''`, Spec{Command: []string{"a", ""}}},
		{"quoted metacharacters", `a ';|&()<>*?[]{}#!~'`, Spec{Command: []string{"a", ";|&()<>*?[]{}#!~"}}},
		{"agent K=V prefix is its environment", "K=V L='a b' codex", Spec{
			Command: []string{"codex"},
			Env:     map[string]string{"K": "V", "L": "a b"},
		}},
		{"agent env prefix", "env K=V codex", Spec{
			Command: []string{"codex"},
			Env:     map[string]string{"K": "V"},
		}},
		{"later assignment wins for the agent", "K=1 env K=2 codex", Spec{
			Command: []string{"codex"},
			Env:     map[string]string{"K": "2"},
		}},
		{"prelude prefix is command-local", "K=V env L=M npm ci && codex", Spec{
			Prelude: []Step{{Command: Command{
				Env:  []EnvVar{{"K", "V"}, {"L", "M"}},
				Argv: []string{"npm", "ci"},
			}}},
			Command: []string{"codex"},
		}},
		{"env with a flag is the program", "env -i codex", Spec{Command: []string{"env", "-i", "codex"}}},
		{"env without assignment is the program", "env codex", Spec{Command: []string{"env", "codex"}}},
		{"quoted name is not an assignment", `'K=V' codex`, Spec{Command: []string{"K=V", "codex"}}},
		{"partly quoted name is not an assignment", `K"="V codex`, Spec{Command: []string{"K=V", "codex"}}},
		{"invalid name is the program", "1K=V codex", Spec{Command: []string{"1K=V", "codex"}}},
		{"leading home", "~/bin/codex ~/x", Spec{Command: []string{"/home/u/bin/codex", "/home/u/x"}}},
		{"mid-word tilde is literal", "git show HEAD~1", Spec{Command: []string{"git", "show", "HEAD~1"}}},
		{"quoted tilde is literal", `a '~/x'`, Spec{Command: []string{"a", "~/x"}}},
		{"unicode", "a é", Spec{Command: []string{"a", "é"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mustCompile(t, tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestCompileRefused(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		offset int
		reason string
	}{
		{"empty", "", 0, "empty"},
		{"blank", "   ", 3, "empty"},
		{"leading operator", "&& a", 0, "empty command"},
		{"doubled operator", "a && && b", 5, "empty command"},
		{"trailing operator", "a &&", 4, "empty command"},
		{"or", "a || b", 2, "`|`"},
		{"pipe", "a | b", 2, "`|`"},
		{"semicolon", "a; b", 1, "`;`"},
		{"background", "a & b", 2, "`&`"},
		{"trailing background", "a &", 2, "`&`"},
		{"subshell open", "(a)", 0, "`(`"},
		{"subshell close", "a)", 1, "`)`"},
		{"redirect out", "a > b", 2, "`>`"},
		{"redirect in", "a < b", 2, "`<`"},
		{"variable", "a $HOME", 2, "`$`"},
		{"command substitution", "a `b`", 2, "backtick"},
		{"star", "a *", 2, "`*`"},
		{"question", "a ?", 2, "`?`"},
		{"bracket", "[ a ]", 0, "`[`"},
		{"bracket close", "a ]", 2, "`]`"},
		{"brace", "a {b,c}", 2, "`{`"},
		{"brace close", "a }", 2, "`}`"},
		{"comment", "a #b", 2, "`#`"},
		{"bang", "! a", 0, "`!`"},
		{"backslash", `a \b`, 2, "backslash"},
		{"newline", "a\nb", 1, "control"},
		{"control", "a\x03b", 1, "control"},
		{"tab inside quotes", "a 'b\tc'", 0, "control"},
		{"invalid utf-8", "a \xff", 2, "UTF-8"},
		{"open single quote", "a 'b", 2, "unterminated"},
		{"open double quote", `a "b`, 2, "unterminated"},
		{"trailing double backslash", `a "b\`, 2, "unterminated"},
		{"dollar in double quotes", `a "$b"`, 3, "`$`"},
		{"backtick in double quotes", "a \"`b`\"", 3, "backtick"},
		{"bare tilde", "~", 0, "tilde"},
		{"tilde user", "~root/x", 0, "tilde"},
		{"tilde after equals", "K=~/x a", 2, "tilde"},
		{"tilde after colon", "K=a:~/x a", 4, "tilde"},
		{"tilde in an argument assignment", "a --x=~/y", 6, "tilde"},
		{"assignment alone", "K=V", 0, "no program"},
		{"env assignment alone", "env K=V", 0, "no program"},
		{"prelude assignment alone", "K=V && a", 0, "no program"},
		{"empty program", "'' a", 0, "program"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.line, Options{Home: "/home/u"})
			var refused *RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("Compile(%q) error = %v, want refused", tc.line, err)
			}
			if refused.Offset != tc.offset {
				t.Errorf("offset = %d, want %d (%v)", refused.Offset, tc.offset, err)
			}
			if !strings.Contains(refused.Error(), tc.reason) {
				t.Errorf("error %q does not name %q", refused.Error(), tc.reason)
			}
		})
	}
}

func TestCompileHomeRequiredForTilde(t *testing.T) {
	for _, home := range []string{"", "relative"} {
		if _, err := Compile("~/x", Options{Home: home}); err == nil || !strings.Contains(err.Error(), "home") {
			t.Errorf("Home %q: error = %v", home, err)
		}
	}
}

func TestRefusalHintIsAStructuredSpec(t *testing.T) {
	_, err := Compile("a \"b\" | c\x01 \\ é \xff", Options{})
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v", err)
	}
	want := `[launch]
prelude = [{ command = ["sh", "-c", "a \"b\" | c\u0001 \\ é ` + "�" + `"], retry = "never" }]
command = ["<agent executable>"]
`
	if got := refused.Hint(); got != want {
		t.Fatalf("hint:\n%s\nwant:\n%s", got, want)
	}
}

func TestValidate(t *testing.T) {
	ok := func() Spec { return Spec{Command: []string{"codex"}} }
	valid := []Spec{
		ok(),
		{Command: []string{"a"}, Prelude: []Step{{Command: cmdOf("b"), Retry: RetryIdempotent}}},
		{Command: []string{"a"}, Prelude: []Step{{Command: cmdOf("b"), Retry: RetryNever, Once: true}}},
		{Command: []string{"a"}, Prelude: []Step{{Command: cmdOf("b"), Probe: &Command{Argv: []string{"c"}}}}},
		{Command: []string{"a"}, Env: map[string]string{"A_1": "x y"}, Cwd: "/w", PreludeTimeout: time.Minute},
	}
	for i, spec := range valid {
		if err := spec.Validate(); err != nil {
			t.Errorf("valid %d: %v", i, err)
		}
	}
	tests := []struct {
		name   string
		mutate func(*Spec)
		want   string
	}{
		{"no command", func(s *Spec) { s.Command = nil }, "command"},
		{"empty program", func(s *Spec) { s.Command = []string{""} }, "program"},
		{"control in agent", func(s *Spec) { s.Command = []string{"a", "b\nc"} }, "control"},
		{"invalid utf-8", func(s *Spec) { s.Command = []string{"a", "\xff"} }, "UTF-8"},
		{"bad env name", func(s *Spec) { s.Env = map[string]string{"A-B": "x"} }, "A-B"},
		{"empty env name", func(s *Spec) { s.Env = map[string]string{"": "x"} }, "name"},
		{"control env value", func(s *Spec) { s.Env = map[string]string{"A": "\x1b"} }, "control"},
		{"control cwd", func(s *Spec) { s.Cwd = "/a\r" }, "control"},
		{"negative timeout", func(s *Spec) { s.PreludeTimeout = -1 }, "timeout"},
		{"prelude without argv", func(s *Spec) { s.Prelude = []Step{{}} }, "step 1"},
		{"prelude bad retry", func(s *Spec) { s.Prelude = []Step{{Command: cmdOf("b"), Retry: "always"}} }, "always"},
		{"once and idempotent", func(s *Spec) {
			s.Prelude = []Step{{Command: cmdOf("b"), Retry: RetryIdempotent, Once: true}}
		}, "once"},
		{"bad probe", func(s *Spec) { s.Prelude = []Step{{Command: cmdOf("b"), Probe: &Command{}}} }, "probe"},
		{"prelude env name", func(s *Spec) {
			s.Prelude = []Step{{Command: Command{Env: []EnvVar{{"1", "x"}}, Argv: []string{"b"}}}}
		}, "step 1"},
		{"prelude env value", func(s *Spec) {
			s.Prelude = []Step{{Command: Command{Env: []EnvVar{{"A", "\t"}}, Argv: []string{"b"}}}}
		}, "control"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := ok()
			tc.mutate(&spec)
			err := spec.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTimeoutDefault(t *testing.T) {
	if got := (Spec{}).Timeout(); got != 10*time.Minute {
		t.Errorf("default = %v", got)
	}
	if got := (Spec{PreludeTimeout: time.Second}).Timeout(); got != time.Second {
		t.Errorf("set = %v", got)
	}
}

func TestStepsDir(t *testing.T) {
	got, err := StepsDir("/route", "op-1")
	if err != nil || got != filepath.Join("/route", ".steps", "op-1") {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, op := range []string{"", ".", "..", "a/b", `a\b`, "a\nb"} {
		if _, err := StepsDir("/route", op); err == nil {
			t.Errorf("op %q accepted", op)
		}
	}
}

func goldenCases() []struct {
	name string
	dir  string
	n    int
	step Step
} {
	const dir = "/route/.steps/op-1"
	return []struct {
		name string
		dir  string
		n    int
		step Step
	}{
		{"never default", dir, 1, Step{Command: cmdOf("claude", "update")}},
		{"never explicit", dir, 2, Step{Command: cmdOf("npm", "ci"), Retry: RetryNever}},
		{"idempotent", dir, 3, Step{Command: cmdOf("make", "deps"), Retry: RetryIdempotent}},
		{"once", dir, 4, Step{Command: cmdOf("db", "migrate"), Once: true}},
		{"probe", dir, 5, Step{Command: cmdOf("db", "seed"), Probe: &Command{Argv: []string{"db", "seeded"}}}},
		{"command-local environment", dir, 6, Step{Command: Command{
			Env:  []EnvVar{{"K", "V"}, {"L", "a b"}},
			Argv: []string{"npm", "ci"},
		}}},
		{"hostile value", dir, 7, Step{Command: cmdOf("echo", "; touch x", "$(touch x)", "`touch x`", "it's")}},
		{"assignment-like program", dir, 8, Step{Command: cmdOf("K=V", "--flag=v", "=zsh")}},
		{"reserved word program", dir, 9, Step{Command: cmdOf("if", "x")}},
		{"empty argument", dir, 10, Step{Command: cmdOf("a", "")}},
		{"hostile folder", "/route x/it's/.steps/$(op)", 11, Step{Command: cmdOf("a")}},
	}
}

func renderGolden(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, tc := range goldenCases() {
		line, err := Wrap(tc.dir, tc.n, tc.step)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		fmt.Fprintf(&b, "## %s\n%s\n", tc.name, line)
		if tc.step.Probe != nil {
			probe, err := WrapProbe(tc.dir, tc.n, tc.step)
			if err != nil {
				t.Fatalf("%s probe: %v", tc.name, err)
			}
			fmt.Fprintf(&b, "## %s (its probe)\n%s\n", tc.name, probe)
		}
	}
	return b.String()
}

func TestWrapperGolden(t *testing.T) {
	got := renderGolden(t)
	path := filepath.Join("testdata", "wrapper.golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("wrapper changed:\n%s\nwant:\n%s", got, want)
	}
}

func TestWrapRefuses(t *testing.T) {
	good := Step{Command: cmdOf("a")}
	if _, err := Wrap("/d", 0, good); err == nil {
		t.Error("step 0 accepted")
	}
	if _, err := Wrap("/d", 1, Step{}); err == nil {
		t.Error("empty step accepted")
	}
	if _, err := Wrap("/d\nx", 1, good); err == nil || !strings.Contains(err.Error(), "control") {
		t.Errorf("control in folder: %v", err)
	}
	if _, err := Wrap("/d", 1, Step{Command: cmdOf("a", "\x15")}); err == nil {
		t.Error("control in value accepted")
	}
	if _, err := WrapProbe("/d", 1, good); err == nil || !strings.Contains(err.Error(), "probe") {
		t.Errorf("no probe: %v", err)
	}
	if _, err := WrapProbe("/d", 1, Step{Command: cmdOf("a"), Probe: &Command{}}); err == nil {
		t.Error("empty probe accepted")
	}
}

var hostileValues = []string{
	"; touch SENT", "$(touch SENT)", "`touch SENT`", "a'b", "'", "''", "'; touch SENT; '",
	`"`, `\`, `\'`, "*", "~", "~/x", "!x", "a b", "&& touch SENT", "| touch SENT",
	"> SENT", "#x", "-n", "", "=x", "é", "%1", "{a,b}", "[a]", "$((1+1))", "<(touch SENT)",
}

func availableShells(t *testing.T) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	for name, args := range map[string][]string{"sh": {"-c"}, "bash": {"--norc", "-c"}, "zsh": {"-f", "-c"}} {
		if _, err := exec.LookPath(name); err == nil {
			found[name] = args
		} else {
			t.Logf("shell %s not installed, not exercised", name)
		}
	}
	if len(found) == 0 {
		t.Fatal("no shell found")
	}
	return found
}

func runInShell(t *testing.T, shell string, args []string, dir, script string) string {
	t.Helper()
	cmd := exec.Command(shell, append(append([]string{}, args...), script)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", shell, err, out)
	}
	return string(out)
}

// argvRecorder writes a program that stores its environment value and its
// arguments, one per line, then exits with status 3.
func argvRecorder(t *testing.T, folder string) string {
	t.Helper()
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(folder, "prog")
	body := "#!/bin/sh\n{ printf '%s\\n' \"$HOSTILE\"; for a; do printf '%s\\n' \"$a\"; done; } > \"$OUT\"\nexit 3\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWrapperNoShellInjection(t *testing.T) {
	for shell, args := range availableShells(t) {
		t.Run(shell, func(t *testing.T) {
			root := t.TempDir()
			prog := argvRecorder(t, filepath.Join(root, "it's $(touch SENT) x"))
			out := filepath.Join(root, "out")
			steps := filepath.Join(root, "st'eps `touch SENT`", ".steps")
			if err := os.MkdirAll(steps, 0o755); err != nil {
				t.Fatal(err)
			}
			for i, value := range hostileValues {
				n := i + 1
				step := Step{Command: Command{
					Env:  []EnvVar{{"OUT", out}, {"HOSTILE", value}},
					Argv: []string{prog, value, "tail"},
				}}
				line, err := Wrap(steps, n, step)
				if err != nil {
					t.Fatalf("%q: %v", value, err)
				}
				printed := runInShell(t, shell, args, root, line)
				if want := fmt.Sprintf("herdsman-step %d rc=3\n", n); printed != want {
					t.Errorf("%q: printed %q, want %q", value, printed, want)
				}
				data, err := os.ReadFile(out)
				if err != nil {
					t.Fatalf("%q: %v", value, err)
				}
				if want := value + "\n" + value + "\ntail\n"; string(data) != want {
					t.Errorf("value changed: got %q, want %q", data, want)
				}
				marker, err := ReadMarker(steps, n)
				if err != nil || marker != (Marker{State: MarkerFinished, RC: 3}) {
					t.Errorf("%q: marker %+v, %v", value, marker, err)
				}
				if _, err := os.Stat(filepath.Join(root, "SENT")); err == nil {
					t.Fatalf("%q ran an injected command", value)
				}
			}
		})
	}
}

// aliasSetup holds the lines a user's shell configuration could have run
// before the wrapper line reaches it.
func aliasSetup(shell string) string {
	lines := []string{`PATH="$PWD:$PATH"`, "alias prog=true", "alias printf=false"}
	switch shell {
	case "bash":
		lines = append(lines, "shopt -s expand_aliases")
	case "zsh":
		lines = append(lines, `alias -g H='| cat'`, "setopt magic_equal_subst")
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestWrapperAliasesCannotChangeTheLine(t *testing.T) {
	for shell, args := range availableShells(t) {
		t.Run(shell, func(t *testing.T) {
			root := t.TempDir()
			argvRecorder(t, root)
			out := filepath.Join(root, "out")
			line, err := Wrap(root, 1, Step{Command: cmdOf("prog", "H", "--x==ls")})
			if err != nil {
				t.Fatal(err)
			}
			script := aliasSetup(shell) + "export OUT='" + out + "'\n" + line
			printed := runInShell(t, shell, args, root, script)
			if printed != "herdsman-step 1 rc=3\n" {
				t.Errorf("printed %q", printed)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if want := "\nH\n--x==ls\n"; string(data) != want {
				t.Errorf("arguments %q, want %q", data, want)
			}
			marker, err := ReadMarker(root, 1)
			if err != nil || marker != (Marker{State: MarkerFinished, RC: 3}) {
				t.Errorf("marker %+v, %v", marker, err)
			}
		})
	}
}

func TestWrapperWritesMarkersUnderNoclobber(t *testing.T) {
	setup := map[string]string{"sh": "set -o noclobber\n", "bash": "set -o noclobber\n", "zsh": "setopt noclobber\n"}
	for shell, args := range availableShells(t) {
		t.Run(shell, func(t *testing.T) {
			root := t.TempDir()
			step := Step{Command: cmdOf("false"), Probe: &Command{Argv: []string{"true"}}}
			for _, name := range []string{"1.start", "1.rc", "1.probe.start", "1.probe.rc"} {
				writeFile(t, filepath.Join(root, name), "7")
			}
			line, err := Wrap(root, 1, step)
			if err != nil {
				t.Fatal(err)
			}
			if printed := runInShell(t, shell, args, root, setup[shell]+line); printed != "herdsman-step 1 rc=1\n" {
				t.Errorf("step printed %q", printed)
			}
			if m, err := ReadMarker(root, 1); err != nil || m != (Marker{State: MarkerFinished, RC: 1}) {
				t.Errorf("step marker %+v, %v", m, err)
			}
			probe, err := WrapProbe(root, 1, step)
			if err != nil {
				t.Fatal(err)
			}
			if printed := runInShell(t, shell, args, root, setup[shell]+probe); printed != "herdsman-step 1.probe rc=0\n" {
				t.Errorf("probe printed %q", printed)
			}
			if m, err := ReadProbeMarker(root, 1); err != nil || m != (Marker{State: MarkerFinished}) {
				t.Errorf("probe marker %+v, %v", m, err)
			}
		})
	}
}

func TestWrapProbeMarkersAreSeparate(t *testing.T) {
	root := t.TempDir()
	step := Step{Command: cmdOf("false"), Probe: &Command{Argv: []string{"true"}}}
	line, err := WrapProbe(root, 4, step)
	if err != nil {
		t.Fatal(err)
	}
	printed := runInShell(t, "sh", []string{"-c"}, root, line)
	if printed != "herdsman-step 4.probe rc=0\n" {
		t.Errorf("printed %q", printed)
	}
	probe, err := ReadProbeMarker(root, 4)
	if err != nil || probe != (Marker{State: MarkerFinished}) {
		t.Errorf("probe marker %+v, %v", probe, err)
	}
	marker, err := ReadMarker(root, 4)
	if err != nil || marker.State != MarkerNotStarted {
		t.Errorf("step marker %+v, %v", marker, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadMarkerStates(t *testing.T) {
	dir := t.TempDir()
	if m, err := ReadMarker(dir, 1); err != nil || m != (Marker{State: MarkerNotStarted}) {
		t.Fatalf("none: %+v, %v", m, err)
	}
	if m, err := ReadMarker(filepath.Join(dir, "missing"), 1); err != nil || m.State != MarkerNotStarted {
		t.Fatalf("missing folder: %+v, %v", m, err)
	}
	writeFile(t, filepath.Join(dir, "1.start"), "")
	if m, err := ReadMarker(dir, 1); err != nil || m != (Marker{State: MarkerUncertain}) {
		t.Fatalf("start only: %+v, %v", m, err)
	}
	writeFile(t, filepath.Join(dir, "1.rc"), "")
	if m, err := ReadMarker(dir, 1); err != nil || m != (Marker{State: MarkerUncertain}) {
		t.Fatalf("empty rc (crash during the write): %+v, %v", m, err)
	}
	for _, tc := range []struct {
		text string
		want int
	}{{"0", 0}, {"1", 1}, {"127", 127}, {"255", 255}} {
		writeFile(t, filepath.Join(dir, "1.rc"), tc.text)
		if m, err := ReadMarker(dir, 1); err != nil || m != (Marker{State: MarkerFinished, RC: tc.want}) {
			t.Fatalf("rc %q: %+v, %v", tc.text, m, err)
		}
	}
}

func TestReadMarkerRefuses(t *testing.T) {
	if _, err := ReadMarker(t.TempDir(), 0); err == nil {
		t.Error("step 0 accepted")
	}
	if _, err := ReadProbeMarker(t.TempDir(), 0); err == nil {
		t.Error("probe of step 0 accepted")
	}
	for _, text := range []string{"x", "-1", "+1", "007", "256", "1 ", "12345"} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "1.start"), "")
		writeFile(t, filepath.Join(dir, "1.rc"), text)
		if m, err := ReadMarker(dir, 1); err == nil {
			t.Errorf("rc %q accepted as %+v", text, m)
		}
	}
	t.Run("rc without start", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "1.rc"), "0")
		if _, err := ReadMarker(dir, 1); err == nil || !strings.Contains(err.Error(), "without") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("start is a symlink", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "target"), "")
		if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "1.start")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadMarker(dir, 1); err == nil || !strings.Contains(err.Error(), "regular") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("rc is a folder", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "1.start"), "")
		if err := os.Mkdir(filepath.Join(dir, "1.rc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadMarker(dir, 1); err == nil || !strings.Contains(err.Error(), "regular") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("steps path is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		writeFile(t, file, "")
		if _, err := ReadMarker(file, 1); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("rc unreadable", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root reads every file")
		}
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "1.start"), "")
		writeFile(t, filepath.Join(dir, "1.rc"), "0")
		if err := os.Chmod(filepath.Join(dir, "1.rc"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadMarker(dir, 1); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("rc path check fails", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root reads every file")
		}
		dir := filepath.Join(t.TempDir(), "steps")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		if _, err := ReadMarker(dir, 1); err == nil {
			t.Error("accepted")
		}
	})
}

func TestClearMarkers(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"2.start", "2.rc", "2.probe.start", "2.probe.rc", "3.start"} {
		writeFile(t, filepath.Join(dir, name), "0")
	}
	if err := ClearMarkers(dir, 2); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "3.start" {
		t.Fatalf("left %v, %v", entries, err)
	}
	if err := ClearMarkers(dir, 2); err != nil {
		t.Errorf("second clear: %v", err)
	}
	if err := ClearMarkers(dir, 0); err == nil {
		t.Error("step 0 accepted")
	}
}

func TestClearMarkersRemovesRCBeforeStart(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "1.rc"), "0")
	if err := os.Mkdir(filepath.Join(dir, "1.start"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "1.start", "pin"), "")
	if err := ClearMarkers(dir, 1); err == nil {
		t.Fatal("undeletable start reported no error")
	}
	if _, err := os.Lstat(filepath.Join(dir, "1.rc")); !os.IsNotExist(err) {
		t.Errorf("rc still present after a stopped clear: %v", err)
	}
}

func TestClearProbeMarkersKeepsStepEvidence(t *testing.T) {
	probe := &Command{Argv: []string{"seeded"}}
	for name, step := range map[string]Step{
		"never": {Command: cmdOf("seed"), Probe: probe},
		"once":  {Command: cmdOf("seed"), Once: true, Probe: probe},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, marker := range []string{"1.start", "1.probe.start", "1.probe.rc"} {
				writeFile(t, filepath.Join(dir, marker), "1")
			}
			if got := Decide(step, 1, false, Marker{State: MarkerUncertain}); got.Action != ActionProbe {
				t.Fatalf("first decision %+v", got)
			}
			if err := ClearProbeMarkers(dir, 1); err != nil {
				t.Fatal(err)
			}
			marker, err := ReadMarker(dir, 1)
			if err != nil || marker.State != MarkerUncertain {
				t.Fatalf("step marker after the clear: %+v, %v", marker, err)
			}
			if got := Decide(step, 1, false, marker); got.Action != ActionProbe {
				t.Fatalf("a crash after the clear decides %+v", got)
			}
			probeMarker, err := ReadProbeMarker(dir, 1)
			if err != nil || probeMarker.State != MarkerNotStarted {
				t.Fatalf("probe marker after the clear: %+v, %v", probeMarker, err)
			}
		})
	}
	t.Run("the step's status stays", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "2.start"), "")
		writeFile(t, filepath.Join(dir, "2.rc"), "4")
		if err := ClearProbeMarkers(dir, 2); err != nil {
			t.Fatal(err)
		}
		if m, err := ReadMarker(dir, 2); err != nil || m != (Marker{State: MarkerFinished, RC: 4}) {
			t.Fatalf("marker %+v, %v", m, err)
		}
	})
	t.Run("step 0", func(t *testing.T) {
		if err := ClearProbeMarkers(t.TempDir(), 0); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("rc goes before start", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "1.probe.rc"), "0")
		if err := os.Mkdir(filepath.Join(dir, "1.probe.start"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "1.probe.start", "pin"), "")
		if err := ClearProbeMarkers(dir, 1); err == nil {
			t.Fatal("undeletable start reported no error")
		}
		if _, err := os.Lstat(filepath.Join(dir, "1.probe.rc")); !os.IsNotExist(err) {
			t.Errorf("rc still present: %v", err)
		}
	})
}

func TestDecideMarkerStates(t *testing.T) {
	never := Step{Command: cmdOf("db", "migrate")}
	once := Step{Command: cmdOf("db", "migrate"), Once: true}
	idem := Step{Command: cmdOf("make"), Retry: RetryIdempotent}
	probe := Step{Command: cmdOf("seed"), Probe: &Command{Argv: []string{"seeded"}}}
	onceProbe := Step{Command: cmdOf("seed"), Once: true, Probe: &Command{Argv: []string{"seeded"}}}
	idemProbe := Step{Command: cmdOf("seed"), Retry: RetryIdempotent, Probe: &Command{Argv: []string{"seeded"}}}
	notStarted := Marker{State: MarkerNotStarted}
	uncertain := Marker{State: MarkerUncertain}
	ok := Marker{State: MarkerFinished}
	failed := Marker{State: MarkerFinished, RC: 2}
	both := []string{"herdsman up --continue --step 5=done", "herdsman up --continue --step 5=rerun"}
	doneOnly := both[:1]

	tests := []struct {
		name   string
		step   Step
		done   bool
		marker Marker
		action Action
		rc     int
		resume []string
	}{
		{"recorded done is skipped whatever the marker", never, true, uncertain, ActionSkip, 0, nil},
		{"no start: never ran, runs", never, false, notStarted, ActionRun, 0, nil},
		{"no start: once also runs", once, false, notStarted, ActionRun, 0, nil},
		{"start without rc: idempotent runs again", idem, false, uncertain, ActionRun, 0, nil},
		{"start without rc: idempotent wins over a probe", idemProbe, false, uncertain, ActionRun, 0, nil},
		{"start without rc: probe runs the probe", probe, false, uncertain, ActionProbe, 0, nil},
		{"start without rc: once with a probe runs the probe", onceProbe, false, uncertain, ActionProbe, 0, nil},
		{"start without rc: never stops", never, false, uncertain, ActionStop, 0, both},
		{"start without rc: once stops without rerun", once, false, uncertain, ActionStop, 0, doneOnly},
		{"unknown state is uncertain", never, false, Marker{State: "?"}, ActionStop, 0, both},
		{"rc zero is done", never, false, ok, ActionDone, 0, nil},
		{"rc nonzero fails", never, false, failed, ActionFail, 2, both},
		{"rc nonzero fails once without rerun", once, false, failed, ActionFail, 2, doneOnly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.step, 5, tc.done, tc.marker)
			if got.Action != tc.action || got.RC != tc.rc || !reflect.DeepEqual(got.Resume, tc.resume) {
				t.Fatalf("got %+v, want %s rc=%d resume=%v", got, tc.action, tc.rc, tc.resume)
			}
			if got.Reason == "" {
				t.Error("no reason")
			}
		})
	}
	t.Run("a stop names the step and its command", func(t *testing.T) {
		got := Decide(never, 5, false, uncertain)
		if !strings.Contains(got.Reason, "step 5") || !strings.Contains(got.Reason, "'db' 'migrate'") {
			t.Errorf("reason %q", got.Reason)
		}
	})
}

func TestProbeOutcome(t *testing.T) {
	probe := Step{Command: cmdOf("seed"), Probe: &Command{Argv: []string{"seeded"}}}
	once := Step{Command: cmdOf("seed"), Once: true, Probe: &Command{Argv: []string{"seeded"}}}
	finished := func(rc int) Marker { return Marker{State: MarkerFinished, RC: rc} }
	tests := []struct {
		name   string
		step   Step
		marker Marker
		action Action
		resume []string
	}{
		{"zero is done", probe, finished(0), ActionDone, nil},
		{"nonzero runs the step again", probe, finished(1), ActionRun, nil},
		{"once is done on zero", once, finished(0), ActionDone, nil},
		{"once is never rerun", once, finished(1), ActionStop, []string{"herdsman up --continue --step 2=done"}},
		{"probe never ran", probe, Marker{State: MarkerNotStarted}, ActionProbe, nil},
		{"probe uncertain runs again", probe, Marker{State: MarkerUncertain}, ActionProbe, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideProbe(tc.step, 2, tc.marker)
			if got.Action != tc.action || !reflect.DeepEqual(got.Resume, tc.resume) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestApplyOverride(t *testing.T) {
	never := Step{Command: cmdOf("a")}
	once := Step{Command: cmdOf("a"), Once: true}
	if got, err := ApplyOverride(never, 1, OverrideDone); err != nil || got.Action != ActionDone {
		t.Errorf("done: %+v, %v", got, err)
	}
	if got, err := ApplyOverride(once, 1, OverrideDone); err != nil || got.Action != ActionDone {
		t.Errorf("once done: %+v, %v", got, err)
	}
	if got, err := ApplyOverride(never, 1, OverrideRerun); err != nil || got.Action != ActionRun {
		t.Errorf("rerun: %+v, %v", got, err)
	}
	if _, err := ApplyOverride(once, 1, OverrideRerun); err == nil || !strings.Contains(err.Error(), "once") {
		t.Errorf("once rerun: %v", err)
	}
	if _, err := ApplyOverride(never, 1, "maybe"); err == nil {
		t.Error("unknown override accepted")
	}
}

func TestPlan(t *testing.T) {
	spec := mustCompile(t, "claude update && K='a b' npm ci && A=1 codex --x 'y z'")
	spec.Prelude[1].Retry = RetryIdempotent
	spec.Prelude[0].Probe = &Command{Argv: []string{"claude", "--version"}}
	spec.Cwd = "/work/tree"
	spec.PreludeTimeout = 2 * time.Minute

	plan, err := NewPlan(spec, "/route/.steps/op-1")
	if err != nil {
		t.Fatal(err)
	}
	want := `cwd: /work/tree
prelude timeout: 2m0s
step 1: 'claude' 'update'
  retry: never
  probe: 'claude' '--version'
  submit: 'printf' '' >| '/route/.steps/op-1/1.start' && 'claude' 'update'; rc=$?; 'printf' '%s' "$rc" >| '/route/.steps/op-1/1.rc'; 'printf' 'herdsman-step %s rc=%s\n' '1' "$rc"
step 2: K='a b' 'npm' 'ci'
  retry: idempotent
  submit: 'printf' '' >| '/route/.steps/op-1/2.start' && K='a b' 'npm' 'ci'; rc=$?; 'printf' '%s' "$rc" >| '/route/.steps/op-1/2.rc'; 'printf' 'herdsman-step %s rc=%s\n' '2' "$rc"
env: A='1'
agent: 'codex' '--x' 'y z'
`
	if got := plan.String(); got != want {
		t.Fatalf("plan:\n%s\nwant:\n%s", got, want)
	}

	t.Run("once and bare", func(t *testing.T) {
		plan, err := NewPlan(Spec{Command: []string{"a"}, Prelude: []Step{{Command: cmdOf("b"), Once: true}}}, "/d")
		if err != nil {
			t.Fatal(err)
		}
		got := plan.String()
		for _, want := range []string{"prelude timeout: 10m0s\n", "  retry: never (once)\n", "agent: 'a'\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
		for _, absent := range []string{"cwd:", "env:", "probe:"} {
			if strings.Contains(got, absent) {
				t.Errorf("unexpected %q in:\n%s", absent, got)
			}
		}
	})
	t.Run("refuses an invalid spec", func(t *testing.T) {
		if _, err := NewPlan(Spec{}, "/d"); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("refuses an invalid folder", func(t *testing.T) {
		if _, err := NewPlan(spec, "/d\n"); err == nil {
			t.Error("accepted")
		}
	})
}

// canonicalLine prints a spec in the grammar, quoting every word.
func canonicalLine(spec Spec) string {
	word := func(s string) string {
		if !strings.Contains(s, "'") {
			return "'" + s + "'"
		}
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(s) + `"`
	}
	assign := func(k, v string) string { return k + "=" + word(v) }
	var parts []string
	for _, step := range spec.Prelude {
		var words []string
		for _, e := range step.Command.Env {
			words = append(words, assign(e.Name, e.Value))
		}
		for _, a := range step.Command.Argv {
			words = append(words, word(a))
		}
		parts = append(parts, strings.Join(words, " "))
	}
	names := make([]string, 0, len(spec.Env))
	for name := range spec.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	var words []string
	for _, name := range names {
		words = append(words, assign(name, spec.Env[name]))
	}
	for _, a := range spec.Command {
		words = append(words, word(a))
	}
	parts = append(parts, strings.Join(words, " "))
	return strings.Join(parts, " && ")
}

func FuzzCompile(f *testing.F) {
	for _, seed := range []string{
		"codex", "claude update && claude --flag", "K=V env L='a b' a && b", `a "b\"c" 'd'`,
		"~/x ~/y", "a || b", "a; b", "$(x)", "a `b`", "\"", "'", "&&", "& &", "K=", "=", "env", "env K=V",
		"a\tb", "a\nb", "\xff", "~", "~/", "~root", "'' a", "a ''", `"\`, `"\\"`, "A=~/x b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		spec, err := Compile(line, Options{Home: "/home/u"})
		if err != nil {
			var refused *RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("not a refusal: %v", err)
			}
			if refused.Offset < 0 || refused.Offset > len(line) {
				t.Fatalf("offset %d outside %q", refused.Offset, line)
			}
			if !strings.HasPrefix(refused.Hint(), "[launch]\n") {
				t.Fatalf("hint %q", refused.Hint())
			}
			return
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("accepted %q is invalid: %v", line, err)
		}
		for i, step := range spec.Prelude {
			if step.RetryPolicy() != RetryNever {
				t.Fatalf("%q step %d not never", line, i+1)
			}
			if _, err := Wrap("/route/.steps/op", i+1, step); err != nil {
				t.Fatalf("%q step %d: %v", line, i+1, err)
			}
		}
		again, err := Compile(canonicalLine(spec), Options{})
		if err != nil {
			t.Fatalf("canonical of %q: %v", line, err)
		}
		if !reflect.DeepEqual(again, spec) {
			t.Fatalf("round trip of %q: got %#v, want %#v", line, again, spec)
		}
	})
}

func FuzzWrapQuoting(f *testing.F) {
	for _, seed := range hostileValues {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		root := t.TempDir()
		prog := argvRecorder(t, filepath.Join(root, "bin"))
		out := filepath.Join(root, "out")
		line, err := Wrap(root, 1, Step{Command: Command{
			Env:  []EnvVar{{"OUT", out}, {"HOSTILE", value}},
			Argv: []string{prog, value},
		}})
		if err != nil {
			return
		}
		if len(value) > 4096 {
			return
		}
		runInShell(t, "sh", []string{"-c"}, root, line)
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if want := value + "\n" + value + "\n"; string(data) != want {
			t.Fatalf("got %q, want %q", data, want)
		}
		if _, err := os.Stat(filepath.Join(root, "SENT")); err == nil {
			t.Fatalf("%q ran an injected command", value)
		}
	})
}
