package forms

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/arafathusayn/herdsman/internal/tui"
	"github.com/arafathusayn/herdsman/internal/tui/components"
)

var update = flag.Bool("update", false, "rewrite the golden frames")

func common() Common {
	return Common{
		Style:   tui.Style{},
		Plan:    func(argv []string) string { return "PLAN " + strings.Join(argv, " ") },
		Consent: func() []string { return []string{"--approve-plan", "ID"} },
	}
}

func upSpec() Spec {
	return Up(UpDefaults{Common: common(), Harnesses: []string{"claude", "codex"}})
}

var targets = []string{"alpha", "beta"}

func goSpec() Spec { return Go(GoDefaults{Common: common(), Targets: targets}) }
func teardownSpec() Spec {
	return Teardown(TargetDefaults{Common: common(), Targets: targets})
}
func cleanSpec() Spec { return Clean(TargetDefaults{Common: common(), Targets: targets}) }
func downSpec() Spec  { return Down(TargetDefaults{Common: common(), Targets: targets}) }
func taskSpec() Spec {
	c := common()
	c.Plan = nil
	return TaskCreate(TaskCreateDefaults{Common: c, Project: "site"})
}

// press feeds typed text and keys to a form without a terminal.
func press(t *testing.T, m tui.Model, input string) tui.Model {
	t.Helper()
	var parser tui.Parser
	for _, msg := range parser.Feed([]byte(input)) {
		m, _ = m.Update(msg)
	}
	return m
}

func sized(spec Spec) tui.Model {
	m, _ := tui.Model(spec.Form).Update(tui.Resize{Cols: 80, Rows: 40})
	return m
}

func golden(t *testing.T, name, frame string) {
	t.Helper()
	path := filepath.Join("testdata", "frames", name+".golden")
	shown := strings.ReplaceAll(frame, "\x1b", "␛") + "\n"
	if *update {
		if err := os.WriteFile(path, []byte(shown), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if shown != string(want) {
		t.Errorf("frame %s differs\ngot:\n%s\nwant:\n%s", name, shown, want)
	}
}

var cases = []struct {
	name string
	spec func() Spec
	keys string // reaches the plan screen, or ends a form without one
	args []string
}{
	{"up", upSpec, "ship-it\tland the fix\t\t\t\t \r", []string{"up", "ship-it", "-p", "land the fix", "-o", "codex", "-a", "claude", "--web", "--no-tui"}},
	{"go", goSpec, "\x1b[B\r", []string{"go", "beta"}},
	{"teardown", teardownSpec, "\t5\t \r", []string{"teardown", "alpha", "--grace", "5", "--include", "associated", "--approve-plan", "ID"}},
	{"clean", cleanSpec, "\x1b[B\t \t \r", []string{"clean", "beta", "--keep-branch", "--force", "--approve-plan", "ID"}},
	{"down", downSpec, "\x1b[B\x1b[B\t\t \t\t\r", []string{"down", "--merged", "--keep-branch", "--approve-plan", "ID"}},
	{"task-create", taskSpec, "Fix it\t\tweb, api\tone\ntwo\tbug,  p1\r", []string{"task", "create", "Fix it", "--project", "site", "--repo", "web", "--repo", "api", "--body", "one\ntwo", "--label", "bug", "--label", "p1"}},
}

func TestGoldenFrames(t *testing.T) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := sized(c.spec())
			golden(t, c.name+"-start", m.View())
			if c.name == "go" {
				return
			}
			golden(t, c.name+"-plan", press(t, m, c.keys).View())
		})
	}
}

func TestEachFormBuildsTheVerbsArguments(t *testing.T) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := c.spec()
			m := press(t, sized(spec), c.keys)
			if c.name != "go" {
				m, _ = m.Update(tui.Key{Type: tui.KeyEnter})
			}
			form := m.(components.Form)
			if form.Outcome() != components.Submitted {
				t.Fatalf("outcome %v:\n%s", form.Outcome(), form.View())
			}
			if got := spec.Args(form.Values()); !reflect.DeepEqual(got, c.args) {
				t.Errorf("got %q\nwant %q", got, c.args)
			}
		})
	}
}

func TestDestructivePlansNeverCarryConsent(t *testing.T) {
	var seen []string
	c := common()
	c.Plan = func(argv []string) string { seen = argv; return "x" }
	for _, spec := range []Spec{
		Teardown(TargetDefaults{Common: c, Targets: targets}),
		Clean(TargetDefaults{Common: c, Targets: targets}),
		Down(TargetDefaults{Common: c, Targets: targets}),
	} {
		press(t, sized(spec), "\r")
		for _, arg := range seen {
			if arg == "-y" || arg == "--approve-plan" {
				t.Errorf("the plan run carries -y: %q", seen)
			}
		}
	}
}

func TestUpPlanIsTheDryRun(t *testing.T) {
	m := press(t, sized(upSpec()), "r1\tgo\t\r")
	if !strings.Contains(m.View(), "--dry-run") {
		t.Errorf("no dry run in the plan:\n%s", m.View())
	}
}

func TestUpValidatesLikeItsFlags(t *testing.T) {
	config := filepath.Join(t.TempDir(), "absent.toml")
	spec := Up(UpDefaults{Common: common(), Config: config})
	m := press(t, sized(spec), "bad name\t\t\t\t\r")
	view := m.View()
	for _, want := range []string{"letters, digits", "a prompt is required", "a command is required", "no such file"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in:\n%s", want, view)
		}
	}
}

func TestUpRefusesAShellLineOutsideTheGrammar(t *testing.T) {
	m := press(t, sized(upSpec()), "r\tp\t\x7f\x7f\x7f\x7f\x7f\x7f\x7fa | b\t\r")
	if strings.Contains(m.View(), "PLAN") {
		t.Errorf("a pipe was accepted:\n%s", m.View())
	}
}

func TestUpNoneDropsTheAdjutant(t *testing.T) {
	spec := upSpec()
	m := press(t, sized(spec), "r\tp\t\x1b[A\t\t\r\r") // Up from claude wraps to none.
	got := spec.Args(m.(components.Form).Values())
	want := []string{"up", "r", "-p", "p", "-o", "codex", "--no-adjutant", "--no-tui"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestUpPrefillsTheConfigAndPassesIt(t *testing.T) {
	config := filepath.Join(t.TempDir(), "herdsman.config.toml")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	spec := Up(UpDefaults{Common: common(), Harnesses: []string{"codex"}, Config: config})
	m := press(t, sized(spec), "r\tp\t\r\r")
	got := spec.Args(m.(components.Form).Values())
	want := []string{"up", "r", "-p", "p", "-o", "codex", "-a", "codex", "-c", config, "--no-tui"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestGraceMustBeSeconds(t *testing.T) {
	for _, spec := range []Spec{teardownSpec(), downSpec()} {
		if m := press(t, sized(spec), "\t-1\r"); !strings.Contains(m.View(), "a number of seconds") {
			t.Errorf("no message:\n%s", m.View())
		}
	}
}

func TestTaskCreateNeedsATitle(t *testing.T) {
	if m := press(t, sized(taskSpec()), "\r"); !strings.Contains(m.View(), "a title is required") {
		t.Errorf("no message:\n%s", m.View())
	}
}

func TestPlanWithoutARunnerShowsTheCommand(t *testing.T) {
	m := press(t, sized(taskSpec()), "it's\r")
	if !strings.Contains(m.View(), `herdsman task create "it's" --project site`) {
		t.Errorf("command not shown:\n%s", m.View())
	}
}

func TestPlainAsksTheSameForm(t *testing.T) {
	input := strings.NewReader("r1\nthe prompt\n.\nnone\n\n\ny\ny\n")
	var out bytes.Buffer
	spec := upSpec()
	form, err := spec.Form.RunPlain(tui.NewPrompter(input, &out))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	want := []string{"up", "r1", "-p", "the prompt", "-o", "codex", "--no-adjutant", "--web", "--no-tui"}
	if got := spec.Args(form.Values()); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if !strings.Contains(out.String(), "Found: claude, codex, none") {
		t.Errorf("choices not offered:\n%s", out.String())
	}
}

func TestOpenPlainAsksAgainAfterABadAnswer(t *testing.T) {
	in, out := pipeFiles(t, "\ny\n")
	args, ok, err := Open(context.Background(), teardownSpec(), in, out, true)
	if err == nil && ok {
		t.Fatalf("a refused grace answer ran: %q", args)
	}
}

// pipeFiles returns a reader holding text and a writer that discards.
func pipeFiles(t *testing.T, text string) (in, out *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = out.Close() })
	return in, out
}

func TestOpenPlainSubmitsAndCancels(t *testing.T) {
	in, out := pipeFiles(t, "2\n")
	args, ok, err := Open(context.Background(), goSpec(), in, out, true)
	if err != nil || !ok || !reflect.DeepEqual(args, []string{"go", "beta"}) {
		t.Errorf("%q %v %v", args, ok, err)
	}
	in, out = pipeFiles(t, "")
	if _, ok, err := Open(context.Background(), goSpec(), in, out, true); ok || err != nil {
		t.Errorf("end of input: %v %v", ok, err)
	}
}

func TestOpenReportsARunFailure(t *testing.T) {
	in, out := pipeFiles(t, "")
	if _, _, err := Open(context.Background(), goSpec(), in, out, false); err == nil {
		t.Error("raw mode on a plain file succeeded")
	}
}

type screen struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (s *screen) read(master *os.File) {
	buf := make([]byte, 4096)
	for {
		n, err := master.Read(buf)
		s.mu.Lock()
		s.data.Write(buf[:n])
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (s *screen) waitFor(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		found := strings.Contains(s.data.String(), text)
		shown := s.data.String()
		s.mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q never appeared in %q", text, shown)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func ioctl(t *testing.T, f *os.File, request uintptr, arg unsafe.Pointer) {
	t.Helper()
	raw, err := f.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg))
	}); err != nil {
		t.Fatal(err)
	}
	if errno != 0 {
		t.Fatalf("ioctl %#x: %v", request, errno)
	}
}

type winsize struct{ rows, cols, x, y uint16 }

type opened struct {
	args []string
	ok   bool
	err  error
}

func startOnPTY(t *testing.T, spec Spec) (*os.File, *screen, chan opened) {
	t.Helper()
	master, slave := openPTY(t)
	t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })
	size := winsize{rows: 40, cols: 100}
	ioctl(t, master, syscall.TIOCSWINSZ, unsafe.Pointer(&size))
	s := &screen{}
	go s.read(master)
	done := make(chan opened, 1)
	go func() {
		args, ok, err := Open(context.Background(), spec, slave, slave, false)
		done <- opened{args, ok, err}
	}()
	s.waitFor(t, "\x1b[?25l")
	return master, s, done
}

func TestEachFormEndToEndOnAPty(t *testing.T) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			master, s, done := startOnPTY(t, c.spec())
			// A newline in the keys is what Enter sends inside a text area.
			if _, err := master.WriteString(strings.ReplaceAll(c.keys, "\n", "\r")); err != nil {
				t.Fatal(err)
			}
			if c.name != "go" {
				s.waitFor(t, "Enter runs")
				if _, err := master.WriteString("\r"); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case got := <-done:
				if got.err != nil || !got.ok {
					t.Fatalf("%v %v", got.ok, got.err)
				}
				if !reflect.DeepEqual(got.args, c.args) {
					t.Errorf("got %q\nwant %q", got.args, c.args)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the form did not finish")
			}
		})
	}
}

func TestEscapeRunsNothingOnAPty(t *testing.T) {
	master, _, done := startOnPTY(t, downSpec())
	if _, err := master.WriteString("\x1b"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.ok || got.err != nil || got.args != nil {
			t.Errorf("%+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Escape did not end the form")
	}
}

func TestDownPassesEveryChoice(t *testing.T) {
	spec := downSpec()
	m := press(t, sized(spec), "\t3\t \t \t \r\r")
	got := spec.Args(m.(components.Form).Values())
	want := []string{"down", "alpha", "--grace", "3", "--keep-branch", "--include", "associated", "--force", "--approve-plan", "ID"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestOpenPlainReportsAWriteFailure(t *testing.T) {
	in, out := pipeFiles(t, "1\n")
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Open(context.Background(), goSpec(), in, out, true); ok || err == nil {
		t.Errorf("%v %v", ok, err)
	}
}

func TestCommandFieldReportsPromptFailures(t *testing.T) {
	field := newCommandField("Command", []string{"codex"}, 0)
	in, out := pipeFiles(t, "")
	if _, err := field.Ask(tui.NewPrompter(in, out)); err == nil {
		t.Error("the end of input was not reported")
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := field.Ask(tui.NewPrompter(in, out)); err == nil {
		t.Error("the write failure was not reported")
	}
}

func TestWithoutAnIDTheVerbAsksForItself(t *testing.T) {
	c := common()
	c.Consent = nil
	spec := Teardown(TargetDefaults{Common: c, Targets: targets})
	m := press(t, sized(spec), "\r\r")
	got := spec.Args(m.(components.Form).Values())
	if want := []string{"teardown", "alpha"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
