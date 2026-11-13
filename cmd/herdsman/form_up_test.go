package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/tui/forms"
	"github.com/arafathusayn/herdsman/sdk/config"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// stubForms makes every stream a terminal and records what the form would run.
func stubForms(t *testing.T, terminal bool, answer []string, ok bool, openErr error) (ran *[]string, plain *bool, opens *int) {
	t.Helper()
	ran, plain, opens = new([]string), new(bool), new(int)
	oldTerm, oldOpen, oldExec, oldDial := formTerminals, openForm, execVerb, dialRuntime
	t.Cleanup(func() { formTerminals, openForm, execVerb, dialRuntime = oldTerm, oldOpen, oldExec, oldDial })
	// Never the user's own daemon: the up form's backend field asks none.
	dialRuntime = func() (daemonv1.RuntimeService, func(), error) { return nil, nil, errors.New("no daemon in tests") }
	formTerminals = func(io.Reader, io.Writer) (*os.File, *os.File, bool) { return nil, nil, terminal }
	openForm = func(_ context.Context, _ forms.Spec, _, _ *os.File, p bool) ([]string, bool, error) {
		*opens++
		*plain = p
		return answer, ok, openErr
	}
	execVerb = func(argv []string) error { *ran = argv; return nil }
	return ran, plain, opens
}

func build() (forms.Spec, bool) { return forms.Spec{}, true }

func TestFormOpensWithNoArgumentsOnATerminal(t *testing.T) {
	t.Setenv("TERM", "xterm")
	ran, plain, opens := stubForms(t, true, []string{"up", "r"}, true, nil)
	args := []string{}
	code, opened := presentForm(&args, nil, io.Discard, io.Discard, build)
	if !opened || code != 0 || *opens != 1 || *plain {
		t.Fatalf("%d %v %d %v", code, opened, *opens, *plain)
	}
	if !reflect.DeepEqual(*ran, []string{"up", "r"}) {
		t.Errorf("ran %q", *ran)
	}
}

func TestAnyArgumentTakesTheScriptedPath(t *testing.T) {
	_, _, opens := stubForms(t, true, nil, true, nil)
	args := []string{"task-a"}
	if _, opened := presentForm(&args, nil, io.Discard, io.Discard, build); opened || *opens != 0 {
		t.Errorf("opened %v", opened)
	}
	if !reflect.DeepEqual(args, []string{"task-a"}) {
		t.Errorf("args changed: %q", args)
	}
}

func TestNoTUISkipsTheFormAndIsRemoved(t *testing.T) {
	_, _, opens := stubForms(t, true, nil, true, nil)
	for _, flagName := range []string{"--no-tui", "-no-tui"} {
		args := []string{flagName}
		if _, opened := presentForm(&args, nil, io.Discard, io.Discard, build); opened || *opens != 0 || len(args) != 0 {
			t.Errorf("%s: opened %v, args %q", flagName, opened, args)
		}
	}
}

func TestPlainFlagOpensLinePrompts(t *testing.T) {
	_, plain, _ := stubForms(t, true, []string{"go", "a"}, true, nil)
	args := []string{"--plain"}
	if _, opened := presentForm(&args, nil, io.Discard, io.Discard, build); !opened || !*plain {
		t.Errorf("opened %v plain %v", opened, *plain)
	}
}

func TestDumbTerminalGetsLinePrompts(t *testing.T) {
	t.Setenv("TERM", "dumb")
	_, plain, _ := stubForms(t, true, []string{"go", "a"}, true, nil)
	args := []string{}
	if _, opened := presentForm(&args, nil, io.Discard, io.Discard, build); !opened || !*plain {
		t.Errorf("opened %v plain %v", opened, *plain)
	}
}

func TestANonTerminalNeverOpensAForm(t *testing.T) {
	_, _, opens := stubForms(t, false, nil, true, nil)
	args := []string{"--plain"}
	if _, opened := presentForm(&args, nil, io.Discard, io.Discard, build); opened || *opens != 0 || len(args) != 0 {
		t.Errorf("opened %v, args %q", opened, args)
	}
}

func TestFormThatCannotBeBuiltFallsThrough(t *testing.T) {
	_, _, opens := stubForms(t, true, nil, true, nil)
	args := []string{}
	cannot := func() (forms.Spec, bool) { return forms.Spec{}, false }
	if _, opened := presentForm(&args, nil, io.Discard, io.Discard, cannot); opened || *opens != 0 {
		t.Errorf("opened %v", opened)
	}
}

func TestCancelRunsNothing(t *testing.T) {
	ran, _, _ := stubForms(t, true, nil, false, nil)
	var out bytes.Buffer
	args := []string{}
	code, opened := presentForm(&args, nil, &out, io.Discard, build)
	if !opened || code != 0 || *ran != nil || !strings.Contains(out.String(), "nothing ran") {
		t.Errorf("%d %v %q %q", code, opened, *ran, out.String())
	}
}

func TestFormAndExecFailuresReport(t *testing.T) {
	stubForms(t, true, nil, false, errors.New("broken terminal"))
	var errOut bytes.Buffer
	args := []string{}
	if code, opened := presentForm(&args, nil, io.Discard, &errOut, build); !opened || code != 1 || !strings.Contains(errOut.String(), "broken terminal") {
		t.Errorf("%d %v %q", code, opened, errOut.String())
	}
	stubForms(t, true, []string{"go"}, true, nil)
	execVerb = func([]string) error { return errors.New("no binary") }
	errOut.Reset()
	if code, _ := presentForm(&args, nil, io.Discard, &errOut, build); code != 1 || !strings.Contains(errOut.String(), "no binary") {
		t.Errorf("%d %q", code, errOut.String())
	}
}

func TestEveryVerbOpensItsForm(t *testing.T) {
	oldTargets := formTargets
	t.Cleanup(func() { formTargets = oldTargets })
	formTargets = func() []string { return []string{"alpha"} }
	for name, open := range map[string]func(*[]string, io.Reader, io.Writer, io.Writer) (int, bool){
		"up": openUpForm, "go": openGoForm, "teardown": openTeardownForm,
		"clean": openCleanForm, "down": openDownForm, "task create": openTaskCreateForm,
	} {
		_, _, opens := stubForms(t, true, []string{"x"}, true, nil)
		args := []string{}
		if _, opened := open(&args, nil, io.Discard, io.Discard); !opened || *opens != 1 {
			t.Errorf("%s: opened %v, opens %d", name, opened, *opens)
		}
	}
}

func TestPickersFallThroughWithNoWorktrees(t *testing.T) {
	oldTargets := formTargets
	t.Cleanup(func() { formTargets = oldTargets })
	formTargets = func() []string { return nil }
	for name, open := range map[string]func(*[]string, io.Reader, io.Writer, io.Writer) (int, bool){
		"go": openGoForm, "teardown": openTeardownForm, "clean": openCleanForm, "down": openDownForm,
	} {
		stubForms(t, true, nil, true, nil)
		args := []string{}
		if _, opened := open(&args, nil, io.Discard, io.Discard); opened {
			t.Errorf("%s opened with nothing to pick", name)
		}
	}
}

func TestPlanOutput(t *testing.T) {
	for _, c := range []struct{ name, script, want string }{
		{"a plan that waits for consent hides the usage line", "echo the plan; echo no terminal >&2; exit 2", "the plan\n"},
		{"a refusal shows why", "echo the plan; echo still runs >&2; exit 1", "the plan\nstill runs\n"},
		{"a success is the plan", "echo the plan", "the plan\n"},
		{"usage with no plan shows the error", "echo bad flag >&2; exit 2", "bad flag\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := planOutput("/bin/sh", []string{"-c", c.script}); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
	if got := planOutput("/nonexistent/program", nil); !strings.Contains(got, "could not build the plan") {
		t.Errorf("got %q", got)
	}
}

func TestFoundHarnessesKeepsOnlyInstalledOnes(t *testing.T) {
	old := lookHarness
	t.Cleanup(func() { lookHarness = old })
	lookHarness = func(name string) (string, error) {
		if name == "codex" {
			return "/bin/codex", nil
		}
		return "", errors.New("absent")
	}
	if got := foundHarnesses(); !reflect.DeepEqual(got, []string{"codex"}) {
		t.Errorf("got %q", got)
	}
}

func TestFormPlanRunsThisProgram(t *testing.T) {
	// The test binary with no tests selected stands in for herdsman.
	if got := formCommon().Plan([]string{"-test.run=^$"}); !strings.Contains(got, "PASS") {
		t.Errorf("got %q", got)
	}
}

func changedStopPlan() string {
	return `{"plans":[{"task":"a","lines":[{"kind":"process","id":"5","level":"exclusive","class":"compensatable","action":"stop"},{"kind":"process","id":"6","level":"exclusive","class":"compensatable","action":"stop"}]}]}`
}

// approvedID previews stopPlan the way a form does and returns its ID.
func approvedID(t *testing.T) string {
	t.Helper()
	calls := stubWorktree(t, map[string]string{"TeardownPlan": stopPlan})
	code, out, _ := runVerb(runTeardown, "a", "--show-plan-id")
	if code != 0 || len(*calls) != 1 {
		t.Fatalf("code = %d, calls = %+v", code, *calls)
	}
	_, id := splitPlanID(out)
	if id == "" {
		t.Fatalf("no id in %q", out)
	}
	return id
}

func TestTeardownRefusesAPlanThatChangedAfterApproval(t *testing.T) {
	id := approvedID(t)
	calls := stubWorktree(t, map[string]string{"TeardownPlan": changedStopPlan(), "Teardown": `{"reports":[]}`})
	code, _, stderr := runVerb(runTeardown, "a", "--approve-plan", id)
	if code != 1 || !strings.Contains(stderr, "changed") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	for _, call := range *calls {
		if call.method == "Teardown" {
			t.Fatal("a plan nobody saw was executed")
		}
	}
}

func TestTeardownRunsTheApprovedPlanWithoutAsking(t *testing.T) {
	id := approvedID(t)
	calls := stubWorktree(t, map[string]string{"TeardownPlan": stopPlan, "Teardown": `{"reports":[]}`})
	if code, _, stderr := runVerb(runTeardown, "a", "--approve-plan", id); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if last := (*calls)[len(*calls)-1]; last.method != "Teardown" {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestPlanIDIgnoresLineOrderAndState(t *testing.T) {
	one := []json.RawMessage{json.RawMessage(`{"task":"a","lines":[{"kind":"p","id":"1","state":"run"},{"kind":"p","id":"2"}]}`)}
	two := []json.RawMessage{json.RawMessage(`{"task":"a","lines":[{"kind":"p","id":"2"},{"kind":"p","id":"1","state":"sleep"}]}`)}
	three := []json.RawMessage{json.RawMessage(`{"task":"a","lines":[{"kind":"p","id":"2"},{"kind":"p","id":"1","action":"stop"}]}`)}
	if teardownPlanID(one) != teardownPlanID(two) {
		t.Error("order or state changed the id")
	}
	if teardownPlanID(one) == teardownPlanID(three) {
		t.Error("a changed action kept the id")
	}
}

func TestConsentCarriesTheIDOfTheLastPlanShown(t *testing.T) {
	old := planProgram
	t.Cleanup(func() { planProgram = old })
	var asked []string
	reply := "table\n" + planIDPrefix + "abc\n"
	planProgram = func(argv []string) string { asked = argv; return reply }
	common := consentCommon()
	if got := common.Consent(); got != nil {
		t.Errorf("consent before any plan: %q", got)
	}
	if got := common.Plan([]string{"teardown", "a"}); got != "table" {
		t.Errorf("plan text %q", got)
	}
	if !reflect.DeepEqual(asked, []string{"teardown", "a", "--show-plan-id"}) {
		t.Errorf("asked %q", asked)
	}
	if got := common.Consent(); !reflect.DeepEqual(got, []string{"--approve-plan", "abc"}) {
		t.Errorf("consent %q", got)
	}
	reply = "herdsman: refused\n"
	if got := common.Plan([]string{"teardown", "a"}); got != "herdsman: refused" {
		t.Errorf("plan text %q", got)
	}
	if got := common.Consent(); got != nil {
		t.Errorf("a failed plan kept the earlier id: %q", got)
	}
}

func TestVerbEntryPointsOpenTheirForms(t *testing.T) {
	oldTargets := formTargets
	t.Cleanup(func() { formTargets = oldTargets })
	formTargets = func() []string { return []string{"alpha"} }
	for name, run := range map[string]func([]string, io.Reader, io.Writer, io.Writer) int{"up": runUp, "go": runGo, "teardown": runTeardown} {
		_, _, opens := stubForms(t, true, []string{"x"}, true, nil)
		if code, _, stderr := runVerb(run); code != 0 || *opens != 1 {
			t.Errorf("%s: code %d, opens %d, %q", name, code, *opens, stderr)
		}
	}
}

func TestFormFlagsParseBesideOtherArguments(t *testing.T) {
	stubWorktree(t, map[string]string{"TeardownPlan": `{"plans":[]}`, "Go": `{"path":"/x"}`})
	for name, c := range map[string]struct {
		run  func([]string, io.Reader, io.Writer, io.Writer) int
		args []string
	}{
		"teardown after":  {runTeardown, []string{"a", "--no-tui"}},
		"teardown before": {runTeardown, []string{"--plain", "a"}},
		"go after":        {runGo, []string{"a", "--no-tui", "--no-cd"}},
		"go before":       {runGo, []string{"--plain", "--no-cd", "a"}},
	} {
		if code, _, stderr := runVerb(c.run, c.args...); code != 0 {
			t.Errorf("%s: code %d, %q", name, code, stderr)
		}
	}
}

func TestFormTargetsNameTasksThenBranches(t *testing.T) {
	old := formList
	t.Cleanup(func() { formList = old })
	formList = func(listRequest) (listResult, []byte, error) {
		return listResult{Rows: []listRow{{Task: "a", Branch: "feat/a"}, {Branch: "main"}, {}}}, nil, nil
	}
	if got := formTargets(); !reflect.DeepEqual(got, []string{"a", "main"}) {
		t.Errorf("got %q", got)
	}
	formList = func(listRequest) (listResult, []byte, error) { return listResult{}, nil, errors.New("no") }
	if got := formTargets(); got != nil {
		t.Errorf("got %q", got)
	}
}

func TestFormExecutableFailures(t *testing.T) {
	old, oldPlan := selfPath, planProgram
	t.Cleanup(func() { selfPath, planProgram = old, oldPlan })
	selfPath = func() (string, error) { return "", errors.New("no self") }
	planProgram = defaultPlanProgram
	if err := defaultExecVerb([]string{"go"}); err == nil {
		t.Error("exec went on with no program")
	}
	if got := formCommon().Plan([]string{"x"}); !strings.Contains(got, "no self") {
		t.Errorf("got %q", got)
	}
	selfPath = func() (string, error) { return "/nonexistent/program", nil }
	if err := defaultExecVerb([]string{"go"}); err == nil {
		t.Error("exec of a missing program succeeded")
	}
}

func TestFormTerminalsRefuseAnythingElse(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, c := range []struct {
		in  io.Reader
		out io.Writer
	}{{strings.NewReader(""), io.Discard}, {file, io.Discard}, {file, os.Stderr}} {
		if _, _, ok := defaultFormTerminals(c.in, c.out); ok {
			t.Errorf("%T %T read as a terminal", c.in, c.out)
		}
	}
}

func TestUpFormSurvivesAnUnreadableConfig(t *testing.T) {
	old := projectFile
	t.Cleanup(func() { projectFile = old })
	projectFile = func(config.Options) (string, error) { return "", errors.New("bad") }
	stubForms(t, true, []string{"x"}, true, nil)
	args := []string{}
	if _, opened := openUpForm(&args, nil, io.Discard, io.Discard); !opened {
		t.Error("the form did not open")
	}
}

// The up form's backend field takes what the doctor resolves, offers the
// other backend first when the doctor says the resolved one cannot run, and
// says why in its label.
func TestUpFormBackendComesFromTheDoctor(t *testing.T) {
	f := &fakeRuntime{backend: "herdr-tui", source: "existing-store"}
	withFakeRuntime(t, f)
	if b, note := formBackend("/srv/repo", "/srv/c.toml"); b != "herdr-tui" || note != "from installation" {
		t.Fatalf("%q %q", b, note)
	}
	if f.doctor.GetRepo() != "/srv/repo" || f.doctor.GetConfigPath() != "/srv/c.toml" {
		t.Fatalf("doctor asked %+v", f.doctor)
	}
	// The fake says core is not available in this build.
	f.backend = "herdr-core"
	if b, note := formBackend("/srv/repo", ""); b != "herdr-tui" || !strings.Contains(note, "herdr-core from installation cannot run here") {
		t.Fatalf("%q %q", b, note)
	}
	b, note := backendFromDoctor(&daemonv1.RuntimeServiceDoctorResponse{Checks: []*daemonv1.RuntimeCheck{{Name: "selection", State: "fail",
		Detail: "backend_experimental: herdr-core (from config) ships as an experiment in this release; set experimental = true"}}})
	if b != "herdr-tui" || !strings.Contains(note, "experimental = true") {
		t.Fatalf("%q %q", b, note)
	}
	b, note = backendFromDoctor(&daemonv1.RuntimeServiceDoctorResponse{
		Selection: &daemonv1.RuntimeSelection{Backend: "herdr-core", Source: "config", ConfigPath: "/c.toml", Experiment: "x"},
		Backends:  []*daemonv1.BackendReport{{Backend: "herdr-core", Available: true}}})
	if b != "herdr-core" || note != "from config /c.toml, an experiment" {
		t.Fatalf("%q %q", b, note)
	}
	// No daemon: the config file's own line, as it stands.
	dialRuntime = func() (daemonv1.RuntimeService, func(), error) { return nil, nil, errors.New("down") }
	file := filepath.Join(t.TempDir(), "herdsman.config.toml")
	if err := os.WriteFile(file, []byte("[runtime]\nbackend = \"herdr-core\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, note := formBackend("", file); b != "herdr-core" || !strings.Contains(note, "did not answer") {
		t.Fatalf("%q %q", b, note)
	}
	if b, note := formBackend("", ""); b != "" || !strings.Contains(note, "down") {
		t.Fatalf("%q %q", b, note)
	}
}

func TestPlanIDCoversAnUnreadablePlan(t *testing.T) {
	one := teardownPlanID([]json.RawMessage{json.RawMessage(`not json`)})
	two := teardownPlanID([]json.RawMessage{json.RawMessage(`also not json`)})
	if one == two {
		t.Error("two unreadable plans share an id")
	}
}
