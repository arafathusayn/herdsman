package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	// Registers the SQLite driver for the table tests.
	_ "github.com/arafathusayn/herdsman/internal/daemon/store"
)

const testExec = "exec-1"

var testStart = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

// machine is a fake host: signals change what the next read shows.
type machine struct {
	mu        sync.Mutex
	snapshot  Snapshot
	calls     []string
	ignoreTER map[int]bool
	readFail  error
	onRead    func(*machine, int)
	reads     int
}

func newMachine(processes ...Process) *machine {
	m := &machine{ignoreTER: map[int]bool{}}
	m.snapshot = Snapshot{
		Scope:    Scope{Task: "a", Execution: testExec, Worktree: "/w"},
		Commands: map[int]string{},
	}
	for _, process := range processes {
		m.add(process)
	}
	return m
}

func (m *machine) add(process Process) {
	m.snapshot.Processes = append(m.snapshot.Processes, process)
	m.snapshot.Commands[process.Pid] = process.Command
	m.snapshot.Cwds = append(m.snapshot.Cwds, CwdEntry{Pid: process.Pid, Path: "/w"})
	m.snapshot.Records = append(m.snapshot.Records, Record{Task: "a", Execution: testExec, Kind: KindProcess, ID: fmt.Sprint(process.Pid), Start: process.Start, Command: process.Command})
}

func (m *machine) read(context.Context) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.onRead != nil {
		m.onRead(m, m.reads)
	}
	if m.readFail != nil {
		return Snapshot{}, m.readFail
	}
	copied := m.snapshot
	copied.Processes = slices.Clone(m.snapshot.Processes)
	copied.Cwds = slices.Clone(m.snapshot.Cwds)
	return copied, nil
}

func (m *machine) remove(pid int) {
	m.snapshot.Processes = slices.DeleteFunc(m.snapshot.Processes, func(p Process) bool { return p.Pid == pid })
	m.snapshot.Cwds = slices.DeleteFunc(m.snapshot.Cwds, func(c CwdEntry) bool { return c.Pid == pid })
}

func (m *machine) kill(pid int, signal syscall.Signal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if signal == 0 {
		if !slices.ContainsFunc(m.snapshot.Processes, func(p Process) bool { return p.Pid == pid }) {
			return syscall.ESRCH
		}
		return nil
	}
	m.calls = append(m.calls, fmt.Sprintf("%d:%d", pid, signal))
	if pid < 0 {
		var members []int
		for _, process := range m.snapshot.Processes {
			if process.Group == -pid && (signal == syscall.SIGKILL || !m.ignoreTER[process.Pid]) {
				members = append(members, process.Pid)
			}
		}
		for _, member := range members {
			m.remove(member)
		}
		return nil
	}
	if signal == syscall.SIGKILL || !m.ignoreTER[pid] {
		m.remove(pid)
	}
	return nil
}

func proc(pid, parent, group int) Process {
	return Process{Pid: pid, Parent: parent, Group: group, Start: testStart, Command: fmt.Sprintf("server --n %d", pid)}
}

func execution(m *machine) *Execution {
	return &Execution{Plan: BuildPlan(m.snapshot), Read: m.read, Kill: m.kill, Grace: 20 * time.Millisecond, Poll: time.Millisecond}
}

func TestChildrenAreSignaledBeforeParents(t *testing.T) {
	m := newMachine(proc(10, 1, 10), proc(20, 10, 20), proc(30, 20, 30))
	e := execution(m)
	outcomes, err := e.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"30:15", "20:15", "10:15"}
	if !slices.Equal(m.calls[:3], want) && !slices.Equal(m.calls, []string{"-30:15", "-20:15", "-10:15"}) {
		t.Fatalf("calls = %v", m.calls)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %v", outcomes)
	}
	for _, outcome := range outcomes {
		if outcome.Result != ResultStopped {
			t.Fatalf("outcomes = %v", outcomes)
		}
	}
}

func TestLeavesFirstIgnoresACycle(t *testing.T) {
	ordered := leavesFirst([]Line{{ID: "1", Parent: 2}, {ID: "2", Parent: 1}})
	if len(ordered) != 2 {
		t.Fatalf("ordered = %v", ordered)
	}
}

func TestAWholeGroupIsSignaledOnce(t *testing.T) {
	m := newMachine(proc(10, 1, 7), proc(11, 10, 7))
	if _, err := execution(m).Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.calls, []string{"-7:15"}) {
		t.Fatalf("calls = %v", m.calls)
	}
}

func TestAPersonInTheGroupKeepsEachPidAlone(t *testing.T) {
	m := newMachine(proc(10, 1, 7))
	human := proc(11, 1, 7)
	human.Command = "vim notes"
	m.snapshot.Processes = append(m.snapshot.Processes, human)
	m.snapshot.Commands[11] = human.Command
	if _, err := execution(m).Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.calls, []string{"10:15"}) {
		t.Fatalf("calls = %v, want only the recorded pid", m.calls)
	}
	if !slices.ContainsFunc(m.snapshot.Processes, func(p Process) bool { return p.Pid == 11 }) {
		t.Fatal("the person's process was signaled")
	}
}

func TestAGroupWithAMemberWithoutEvidenceIsNotOwned(t *testing.T) {
	m := newMachine(proc(10, 1, 7))
	m.onRead = func(m *machine, _ int) {
		if len(m.snapshot.Processes) == 1 {
			m.snapshot.Processes = append(m.snapshot.Processes, Process{Pid: 12, Parent: 1, Group: 7, Start: testStart, Command: "unlisted"})
		}
	}
	e := execution(m)
	if _, err := e.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.calls, "10:15") || slices.Contains(m.calls, "-7:15") {
		t.Fatalf("calls = %v", m.calls)
	}
}

func TestGroupOneIsNeverOwned(t *testing.T) {
	if groupOwned(&revalidation{}, nil, 1) {
		t.Fatal("group 1 owned")
	}
}

func TestAnIgnoredTermGetsKillAfterGrace(t *testing.T) {
	m := newMachine(proc(10, 1, 10))
	m.ignoreTER[10] = true
	e := execution(m)
	outcomes, err := e.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.calls, []string{"-10:15", "-10:9"}) && !slices.Equal(m.calls, []string{"10:15", "10:9"}) {
		t.Fatalf("calls = %v", m.calls)
	}
	if len(outcomes) != 1 || outcomes[0].Result != ResultForced {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestARevalidationIsMadeBeforeEverySignal(t *testing.T) {
	m := newMachine(proc(10, 1, 10), proc(20, 1, 20))
	e := execution(m)
	if _, err := e.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.reads < 2 {
		t.Fatalf("reads = %d, want one for each of two signals", m.reads)
	}
}

func TestAChangedLevelStopsThePlanBeforeAnySignal(t *testing.T) {
	m := newMachine(proc(10, 1, 10))
	e := execution(m)
	m.snapshot.Others = []Scope{{Task: "b", Worktree: "/w"}}
	_, err := e.Execute(context.Background())
	var stop *StopError
	if !errors.As(err, &stop) {
		t.Fatalf("err = %v", err)
	}
	if len(m.calls) != 0 {
		t.Fatalf("calls = %v", m.calls)
	}
}

func TestAChangeAfterTheFirstSignalStopsTheNext(t *testing.T) {
	m := newMachine(proc(10, 1, 10), proc(20, 1, 20))
	e := execution(m)
	m.onRead = func(m *machine, read int) {
		if read == 2 {
			m.snapshot.Others = []Scope{{Task: "b", Worktree: "/w"}}
		}
	}
	outcomes, err := e.Execute(context.Background())
	var stop *StopError
	if !errors.As(err, &stop) {
		t.Fatalf("err = %v", err)
	}
	if len(m.calls) != 1 || len(outcomes) != 0 {
		t.Fatalf("calls = %v, outcomes = %v", m.calls, outcomes)
	}
}

func TestAReusedPidStopsThePlan(t *testing.T) {
	m := newMachine(proc(10, 1, 10))
	e := execution(m)
	m.snapshot.Processes[0].Start = testStart.Add(time.Hour)
	_, err := e.Execute(context.Background())
	var stop *StopError
	if !errors.As(err, &stop) || len(m.calls) != 0 {
		t.Fatalf("err = %v, calls = %v", err, m.calls)
	}
}

func TestAProcessThatTookARoleIsNotSignaled(t *testing.T) {
	m := newMachine(proc(10, 1, 10))
	e := execution(m)
	m.snapshot.Excluded = []Excluded{{Identity: ProcessIdentity(10, testStart), Role: RoleAgent}}
	_, err := e.Execute(context.Background())
	var stop *StopError
	if !errors.As(err, &stop) || len(m.calls) != 0 {
		t.Fatalf("err = %v, calls = %v", err, m.calls)
	}
}

func TestAFailedReadStopsThePlan(t *testing.T) {
	m := newMachine(proc(10, 1, 10))
	e := execution(m)
	m.readFail = errors.New("ps died")
	if _, err := e.Execute(context.Background()); err == nil || !strings.Contains(err.Error(), "ps died") || len(m.calls) != 0 {
		t.Fatalf("err = %v, calls = %v", err, m.calls)
	}
}

func TestAProcessGoneBeforeItsSignalIsReported(t *testing.T) {
	m := newMachine(proc(10, 1, 10), proc(20, 1, 20))
	e := execution(m)
	m.onRead = func(m *machine, read int) {
		if read == 1 {
			m.remove(10)
		}
	}
	outcomes, err := e.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(outcomes, func(o Outcome) bool { return o.ID == "10" && o.Result == ResultGone }) {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestAProcessGoneBeforeKillCountsAsStopped(t *testing.T) {
	m := newMachine(proc(10, 1, 10))
	m.ignoreTER[10] = true
	e := execution(m)
	// A real listing always has other rows; an empty one proves nothing.
	m.snapshot.Processes = append(m.snapshot.Processes, Process{Pid: 99, Parent: 1, Group: 99, Start: testStart, Command: "other"})
	m.onRead = func(m *machine, read int) {
		if read == 2 {
			m.remove(10)
		}
	}
	outcomes, err := e.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Result != ResultStopped || m.reads != 1 && len(m.calls) != 1 {
		t.Fatalf("outcomes = %v, calls = %v", outcomes, m.calls)
	}
}

func TestAChangeBeforeKillStops(t *testing.T) {
	m := newMachine(proc(10, 1, 10))
	m.ignoreTER[10] = true
	e := execution(m)
	m.onRead = func(m *machine, read int) {
		if read == 2 {
			m.snapshot.Others = []Scope{{Task: "b", Worktree: "/w"}}
		}
	}
	_, err := e.Execute(context.Background())
	var stop *StopError
	if !errors.As(err, &stop) || slices.ContainsFunc(m.calls, func(c string) bool { return strings.HasSuffix(c, ":9") }) {
		t.Fatalf("err = %v, calls = %v", err, m.calls)
	}
}

func TestSignalErrorStopsAndGoneIsNoError(t *testing.T) {
	m := newMachine(proc(10, 1, 10))
	e := execution(m)
	e.Kill = func(int, syscall.Signal) error { return syscall.EPERM }
	if _, err := e.Execute(context.Background()); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("err = %v", err)
	}
	m = newMachine(proc(10, 1, 10))
	e = execution(m)
	e.Kill = func(_ int, signal syscall.Signal) error {
		if signal == 0 {
			return syscall.ESRCH
		}
		return syscall.ESRCH
	}
	outcomes, err := e.Execute(context.Background())
	if err != nil || len(outcomes) != 1 || outcomes[0].Result != ResultStopped {
		t.Fatalf("outcomes = %v, err = %v", outcomes, err)
	}
}

func TestACanceledWaitStops(t *testing.T) {
	m := newMachine(proc(10, 1, 10))
	m.ignoreTER[10] = true
	e := execution(m)
	e.Grace = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	m.onRead = func(*machine, int) { cancel() }
	if _, err := e.Execute(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaultsAreFilledIn(t *testing.T) {
	m := newMachine()
	e := &Execution{Plan: BuildPlan(m.snapshot), Read: m.read, Kill: m.kill}
	if _, err := e.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.Grace != DefaultGrace || e.Poll != defaultPoll {
		t.Fatalf("grace = %v, poll = %v", e.Grace, e.Poll)
	}
}

// --- containers ---

func container(id, state, project, dir string) Container {
	return Container{ID: id, Name: id, State: state, LabelsExact: true, Created: testStart,
		Labels: map[string]string{LabelComposeProject: project, LabelComposeDir: dir}}
}

func recordContainer(s *Snapshot, id string) {
	s.Records = append(s.Records, Record{Task: "a", Execution: testExec, Kind: KindContainer, ID: id})
}

type engine struct {
	calls []string
	fail  bool
}

func (x *engine) run(_ context.Context, name string, args ...string) ([]byte, error) {
	x.calls = append(x.calls, name+" "+strings.Join(args, " "))
	if x.fail {
		return nil, errors.New("engine failed")
	}
	return nil, nil
}

func containerExecution(m *machine, x *engine, compose string) *Execution {
	m.snapshot.Compose = compose
	e := execution(m)
	e.Run, e.Engine, e.Compose = x.run, "docker", compose
	return e
}

func TestAWhollyOwnProjectStopsByName(t *testing.T) {
	m := newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "p", "/w"), container("c2", "exited", "p", "/w")}
	recordContainer(&m.snapshot, "c1")
	x := &engine{}
	outcomes, err := containerExecution(m, x, "p").Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(x.calls, []string{"docker stop c1"}) || len(outcomes) != 1 {
		t.Fatalf("calls = %v, outcomes = %v", x.calls, outcomes)
	}
}

func TestAMixedProjectStopsItsOwnContainersOnly(t *testing.T) {
	m := newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "p", "/w"), container("c2", "running", "p", "/elsewhere")}
	recordContainer(&m.snapshot, "c1")
	x := &engine{}
	if _, err := containerExecution(m, x, "p").Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(x.calls, []string{"docker stop c1"}) {
		t.Fatalf("calls = %v", x.calls)
	}
}

func TestNothingIsEverRemoved(t *testing.T) {
	m := newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "p", "/w")}
	recordContainer(&m.snapshot, "c1")
	x := &engine{}
	if _, err := containerExecution(m, x, "p").Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range x.calls {
		for _, word := range []string{" down", " rm", " kill", " prune"} {
			if strings.Contains(call, word) {
				t.Fatalf("call %q removes something", call)
			}
		}
	}
}

func TestAContainerGoneBeforeStopIsReported(t *testing.T) {
	m := newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "other", "/w")}
	recordContainer(&m.snapshot, "c1")
	e := containerExecution(m, &engine{}, "")
	m.onRead = func(m *machine, read int) {
		if read == 2 {
			m.snapshot.Containers[0].State = "exited"
		}
	}
	x := &engine{}
	e.Run = x.run
	outcomes, err := e.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(x.calls) != 0 || outcomes[0].Result != ResultGone {
		t.Fatalf("calls = %v, outcomes = %v", x.calls, outcomes)
	}
}

func TestAnEngineFailureIsReported(t *testing.T) {
	for _, compose := range []string{"", "p"} {
		m := newMachine()
		m.snapshot.Containers = []Container{container("c1", "running", "p", "/w")}
		recordContainer(&m.snapshot, "c1")
		e := containerExecution(m, &engine{fail: true}, compose)
		if _, err := e.Execute(context.Background()); err == nil {
			t.Fatalf("compose %q: no error", compose)
		}
	}
}

func TestAssociatedContainersNeedTheFlag(t *testing.T) {
	make := func() (*machine, *engine, *Execution) {
		m := newMachine()
		m.snapshot.Containers = []Container{container("c1", "running", "x", "/w")}
		x := &engine{}
		return m, x, containerExecution(m, x, "")
	}
	_, x, e := make()
	if _, err := e.Execute(context.Background()); err != nil || len(x.calls) != 0 {
		t.Fatalf("err = %v, calls = %v", err, x.calls)
	}
	_, x, e = make()
	e.IncludeAssociated = true
	if _, err := e.Execute(context.Background()); err != nil || !slices.Equal(x.calls, []string{"docker stop c1"}) {
		t.Fatalf("err = %v, calls = %v", err, x.calls)
	}
}

func TestAComposeRevalidationFailureStops(t *testing.T) {
	m := newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "p", "/w")}
	recordContainer(&m.snapshot, "c1")
	e := containerExecution(m, &engine{}, "p")
	m.readFail = errors.New("gone")
	if _, err := e.Execute(context.Background()); err == nil {
		t.Fatal("no error")
	}
	m = newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "other", "/w"), container("c2", "running", "other", "/w")}
	recordContainer(&m.snapshot, "c1")
	recordContainer(&m.snapshot, "c2")
	e = containerExecution(m, &engine{}, "")
	m.onRead = func(m *machine, read int) {
		if read == 2 {
			m.readFail = errors.New("gone")
		}
	}
	if _, err := e.Execute(context.Background()); err == nil {
		t.Fatal("no error on the per-container read")
	}
}

func TestComposeOwnedNeedsAProject(t *testing.T) {
	e := &Execution{}
	if e.composeOwned(&revalidation{}, nil) {
		t.Fatal("owned without a project")
	}
	e.Compose = "p"
	if e.composeOwned(&revalidation{}, nil) {
		t.Fatal("owned with no members")
	}
}

func TestStillAuthorizedWithoutALine(t *testing.T) {
	check := &revalidation{lines: map[lineKey]Line{}}
	var stop *StopError
	if err := check.stillAuthorized(Line{Kind: KindProcess, ID: "1", Action: ActionStop}); !errors.As(err, &stop) || stop.Changes[0].ToLevel != LevelNoEvidence {
		t.Fatalf("err = %v", err)
	}
}

// --- the reader ---

type fakeHost struct {
	replies map[string]func() ([]byte, error)
	seen    []string
}

func (h *fakeHost) run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	h.seen = append(h.seen, key)
	if reply, ok := h.replies[key]; ok {
		return reply()
	}
	return nil, fmt.Errorf("unexpected command %q", key)
}

func text(body string) func() ([]byte, error) {
	return func() ([]byte, error) { return []byte(body), nil }
}

func fails(err error) func() ([]byte, error) {
	return func() ([]byte, error) { return nil, err }
}

// exitError is a real failed command, with what it printed on standard error.
func exitError(t *testing.T, code int, stderr string) error {
	t.Helper()
	cmd := exec.Command("sh", "-c", fmt.Sprintf("printf '%%s' %q >&2; exit %d", stderr, code))
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("the command succeeded")
	}
	return err
}

const (
	psCommands = "ps -ax -ww -o pid=,command="
	psEnvMac   = "ps -E -ax -ww -o " + psFormat
	psEnvLin   = "ps axeww -o " + psFormat
	psStates   = "ps -ax -o pid=,stat="
	lsofCwd    = "lsof -w -d cwd -F pn"
	lsofListen = "lsof -w -iTCP -sTCP:LISTEN -P -n -F pn"
	lsofEstab  = "lsof -w -iTCP -sTCP:ESTABLISHED -P -n -F pn"
)

func validHost() *fakeHost {
	return &fakeHost{replies: map[string]func() ([]byte, error){
		psCommands:                             text("100 /bin/sleep 300\n200 (zombie)\n"),
		psEnvMac:                               text("100 1 100 Mon Nov 2 10:00:00 2026 /bin/sleep 300 HERDSMAN_EXEC=x\n200 1 200 Mon Nov 2 10:00:00 2026 (zombie)\n"),
		psEnvLin:                               text("100 1 100 Mon Nov 2 10:00:00 2026 /bin/sleep 300 HERDSMAN_EXEC=x\n"),
		psStates:                               text("100 S\n200 Z\n"),
		lsofCwd:                                text("p100\nfcwd\nn/w\n"),
		lsofListen:                             text("p100\nf3\nn*:4100\n"),
		lsofEstab:                              text("p100\nf4\nn127.0.0.1:4100->127.0.0.1:50000\n"),
		"docker --version":                     text("Docker"),
		"docker ps -a -q --no-trunc":           text("abc\n"),
		"docker inspect abc":                   text(`[{"Id":"abc","Name":"/web","Created":"2026-11-08T09:00:00Z","State":{"Status":"running"},"Config":{"Labels":{"k":"v"}},"Mounts":[{"Type":"bind","Source":"/w"}]}]`),
		"docker volume ls -q":                  text("v1\n"),
		"docker volume inspect v1":             text(`[{"Name":"v1","CreatedAt":"2026-11-08T09:00:00Z","Labels":null}]`),
		"docker network ls --format {{.Name}}": text("n1\n"),
		"docker network inspect n1":            text(`[{"Name":"n1","Created":"2026-11-08T09:00:00Z","Labels":{}}]`),
	}}
}

func hostReader(h *fakeHost, goos string) *Reader {
	return &Reader{Run: h.run, Location: time.UTC, GOOS: goos}
}

func TestReadFillsTheSnapshot(t *testing.T) {
	h := validHost()
	got, err := hostReader(h, "darwin").Read(context.Background(), Snapshot{Scope: Scope{Task: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Processes) != 1 || got.Processes[0].Pid != 100 {
		t.Fatalf("processes = %v (zombie must go)", got.Processes)
	}
	if got.Commands[100] != "/bin/sleep 300" || got.Commands[200] != "" {
		t.Fatalf("commands = %v", got.Commands)
	}
	if len(got.Cwds) != 1 || len(got.Listeners) != 1 || len(got.Connections) != 1 {
		t.Fatalf("sockets = %v %v %v", got.Cwds, got.Listeners, got.Connections)
	}
	if len(got.Containers) != 1 || !got.Containers[0].LabelsExact || len(got.Volumes) != 1 || len(got.Networks) != 1 {
		t.Fatalf("containers = %v %v %v", got.Containers, got.Volumes, got.Networks)
	}
	if h.seen[0] != psCommands || h.seen[1] != psEnvMac || h.seen[2] != psStates {
		t.Fatalf("read order = %v", h.seen)
	}
}

func TestLinuxUsesTheEnvironmentFlag(t *testing.T) {
	h := validHost()
	got, err := hostReader(h, "linux").Read(context.Background(), Snapshot{})
	if err != nil || len(got.Processes) != 1 || h.seen[1] != psEnvLin {
		t.Fatalf("processes = %v, seen = %v, err = %v", got.Processes, h.seen, err)
	}
}

func TestAncestorsAreExcluded(t *testing.T) {
	h := validHost()
	r := hostReader(h, "darwin")
	r.Self = 100
	got, err := r.Read(context.Background(), Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Excluded) != 1 || got.Excluded[0].Identity.ID != "100" || got.Excluded[0].Role != RolePaneShell {
		t.Fatalf("excluded = %v", got.Excluded)
	}
	r.Self = 777
	if got, _ = r.Read(context.Background(), Snapshot{}); len(got.Excluded) != 0 {
		t.Fatalf("excluded = %v", got.Excluded)
	}
}

func TestReadFailures(t *testing.T) {
	for _, tc := range []struct {
		key string
		fn  func() ([]byte, error)
	}{
		{psCommands, fails(errors.New("x"))},
		{psCommands, text("not-a-pid foo\n")},
		{psEnvMac, fails(errors.New("x"))},
		{psEnvMac, text("short\n")},
		{psEnvMac, text("")},
		{psStates, fails(errors.New("x"))},
		{psStates, text("bad S\n")},
		{lsofCwd, fails(errors.New("x"))},
		{lsofCwd, text("pnope\n")},
		{lsofCwd, text("")},
		{lsofListen, fails(errors.New("x"))},
		{lsofListen, text("p1\nnbad\n")},
		{lsofEstab, fails(errors.New("x"))},
		{"docker ps -a -q --no-trunc", fails(errors.New("x"))},
		{"docker inspect abc", text("not json")},
		{"docker volume ls -q", fails(errors.New("x"))},
		{"docker network ls --format {{.Name}}", fails(errors.New("x"))},
		{"docker volume inspect v1", text("not json")},
	} {
		h := validHost()
		h.replies[tc.key] = tc.fn
		if _, err := hostReader(h, "darwin").Read(context.Background(), Snapshot{}); err == nil {
			t.Errorf("%s: no error", tc.key)
		}
	}
}

func TestLsofExitOneWithNothingOnStderrIsNoMatch(t *testing.T) {
	h := validHost()
	h.replies[lsofListen] = fails(exitError(t, 1, ""))
	got, err := hostReader(h, "darwin").Read(context.Background(), Snapshot{})
	if err != nil || len(got.Listeners) != 0 {
		t.Fatalf("listeners = %v, err = %v", got.Listeners, err)
	}
	for _, failure := range []error{exitError(t, 1, "lsof: broken"), exitError(t, 2, "")} {
		h := validHost()
		h.replies[lsofListen] = fails(failure)
		if _, err := hostReader(h, "darwin").Read(context.Background(), Snapshot{}); err == nil {
			t.Fatalf("%v accepted", failure)
		}
	}
}

func TestContainerEngines(t *testing.T) {
	h := validHost()
	h.replies["docker --version"] = fails(exec.ErrNotFound)
	h.replies["podman --version"] = fails(exec.ErrNotFound)
	got, err := hostReader(h, "darwin").Read(context.Background(), Snapshot{})
	if err != nil || len(got.Containers) != 0 {
		t.Fatalf("no engine: %v, %v", got.Containers, err)
	}

	h = validHost()
	h.replies["docker ps -a -q --no-trunc"] = fails(exitError(t, 1, "Cannot connect to the Docker daemon"))
	got, err = hostReader(h, "darwin").Read(context.Background(), Snapshot{})
	if err != nil || len(got.Containers) != 0 {
		t.Fatalf("engine down: %v, %v", got.Containers, err)
	}

	h = validHost()
	h.replies["docker ps -a -q --no-trunc"] = fails(exitError(t, 1, "failed to connect to the docker API at unix:///x"))
	if got, err = hostReader(h, "darwin").Read(context.Background(), Snapshot{}); err != nil || len(got.Containers) != 0 {
		t.Fatalf("engine down, new wording: %v, %v", got.Containers, err)
	}

	h = validHost()
	h.replies["docker ps -a -q --no-trunc"] = text("\n")
	h.replies["docker volume ls -q"] = text("")
	h.replies["docker network ls --format {{.Name}}"] = text("")
	got, err = hostReader(h, "darwin").Read(context.Background(), Snapshot{})
	if err != nil || len(got.Containers) != 0 || len(got.Volumes) != 0 {
		t.Fatalf("empty: %v, %v", got, err)
	}

	r := hostReader(validHost(), "darwin")
	r.Engine = "podman"
	if r.EngineName(context.Background()) != "podman" {
		t.Fatal("explicit engine ignored")
	}
	if engineDown(errors.New("plain")) || engineDown(exitError(t, 1, "other")) {
		t.Fatal("not an engine-down error")
	}
}

func TestPodmanIsUsedWhenDockerIsMissing(t *testing.T) {
	h := validHost()
	h.replies["docker --version"] = fails(exec.ErrNotFound)
	h.replies["podman --version"] = text("podman")
	h.replies["podman ps -a -q --no-trunc"] = text("")
	h.replies["podman volume ls -q"] = text("")
	h.replies["podman network ls --format {{.Name}}"] = text("")
	if _, err := hostReader(h, "darwin").Read(context.Background(), Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.seen, "podman ps -a -q --no-trunc") {
		t.Fatalf("seen = %v", h.seen)
	}
}

func TestFacts(t *testing.T) {
	r := hostReader(validHost(), "darwin")
	start, command, err := r.Facts(context.Background(), 100)
	if err != nil || command != "/bin/sleep 300" || start.IsZero() {
		t.Fatalf("%v %q %v", start, command, err)
	}
	if _, _, err := r.Facts(context.Background(), 12345); err == nil {
		t.Fatal("a missing pid gave facts")
	}
	h := validHost()
	h.replies[psCommands] = text("100\n")
	if _, _, err := hostReader(h, "darwin").Facts(context.Background(), 100); err == nil {
		t.Fatal("a pid with no argv gave facts")
	}
	h = validHost()
	h.replies[psEnvMac] = fails(errors.New("x"))
	if _, _, err := hostReader(h, "darwin").Facts(context.Background(), 100); err == nil {
		t.Fatal("a failed read gave facts")
	}
}

func TestParseStatesLimitsItsInput(t *testing.T) {
	if _, err := parseStates(make([]byte, maxInput+1)); err == nil {
		t.Fatal("no size limit")
	}
}

func TestNewReaderReadsThisMachine(t *testing.T) {
	r := NewReader()
	out, err := r.Run(context.Background(), "ps", "-p", fmt.Sprint(os.Getpid()), "-o", "lstart=")
	if err != nil || len(out) == 0 || r.Self != os.Getpid() {
		t.Fatalf("out = %q, err = %v, self = %d", out, err, r.Self)
	}
}

// --- the table ---

func openTable(t *testing.T) *Table {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE resources (task TEXT NOT NULL, execution TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL,
		start TEXT NOT NULL DEFAULT '', command TEXT NOT NULL DEFAULT '', adopted INTEGER NOT NULL DEFAULT 0,
		source TEXT NOT NULL DEFAULT '', registered_at INTEGER NOT NULL, PRIMARY KEY (task, execution, kind, id, start)) STRICT`)
	if err != nil {
		t.Fatal(err)
	}
	return NewTable(db)
}

func TestTableRoundTrip(t *testing.T) {
	table := openTable(t)
	ctx := context.Background()
	want := Record{Task: "a", Execution: "01", Kind: KindProcess, ID: "5", Start: testStart, Command: "srv", Adopted: true}
	if err := table.Add(ctx, want, "hook"); err != nil {
		t.Fatal(err)
	}
	if err := table.Add(ctx, want, "hook"); err != nil {
		t.Fatal(err)
	}
	if err := table.Add(ctx, Record{Task: "a", Execution: "02", Kind: KindVolume, ID: "v"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := table.Add(ctx, Record{Task: "b", Execution: "09", Kind: KindCompose, ID: "p"}, ""); err != nil {
		t.Fatal(err)
	}
	records, err := table.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].Command != "srv" || !records[0].Adopted || !records[0].Start.Equal(testStart) || !records[1].Start.IsZero() {
		t.Fatalf("records = %+v", records)
	}
	executions, err := table.Executions(ctx, "a")
	if err != nil || !slices.Equal(executions, []string{"02", "01"}) {
		t.Fatalf("executions = %v, err = %v", executions, err)
	}
}

func TestTableRefusesAnIncompleteRecord(t *testing.T) {
	table := openTable(t)
	full := Record{Task: "a", Execution: "e", Kind: KindVolume, ID: "v"}
	for name, breakIt := range map[string]func(*Record){
		"task":      func(r *Record) { r.Task = "" },
		"execution": func(r *Record) { r.Execution = "" },
		"kind":      func(r *Record) { r.Kind = "" },
		"id":        func(r *Record) { r.ID = "" },
		"process":   func(r *Record) { r.Kind = KindProcess },
	} {
		record := full
		breakIt(&record)
		if err := table.Add(context.Background(), record, ""); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestTableReportsDatabaseErrors(t *testing.T) {
	table := openTable(t)
	ctx := context.Background()
	if err := table.Add(ctx, Record{Task: "a", Execution: "e", Kind: KindVolume, ID: "v"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := table.db.Exec(`UPDATE resources SET start = 'not-a-time'`); err != nil {
		t.Fatal(err)
	}
	if _, err := table.Records(ctx); err == nil {
		t.Fatal("a bad start time was read")
	}
	if _, err := table.db.Exec(`DROP TABLE resources`); err != nil {
		t.Fatal(err)
	}
	if err := table.Add(ctx, Record{Task: "a", Execution: "e", Kind: KindVolume, ID: "v"}, ""); err == nil {
		t.Fatal("add on no table")
	}
	if _, err := table.Records(ctx); err == nil {
		t.Fatal("records on no table")
	}
	if _, err := table.Executions(ctx, "a"); err == nil {
		t.Fatal("executions on no table")
	}
}

func TestExecutionIDs(t *testing.T) {
	now := time.UnixMilli(1762600000000)
	id := NewExecution(now)
	if len(id) != executionLength || !ExecutionTime(id).Equal(now) {
		t.Fatalf("id = %s, time = %v", id, ExecutionTime(id))
	}
	later := NewExecution(now.Add(time.Second))
	if later <= id {
		t.Fatalf("%s is not after %s", later, id)
	}
	for _, bad := range []string{"", "short", strings.Repeat("!", executionLength)} {
		if !ExecutionTime(bad).IsZero() {
			t.Errorf("%q read as a time", bad)
		}
	}
	var random [10]byte
	if encodeExecution(0, random) != strings.Repeat("0", executionLength) {
		t.Fatal(encodeExecution(0, random))
	}
}

func TestLogRecordsThePlanAndEachAction(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenLog(dir, "api")
	if err != nil {
		t.Fatal(err)
	}
	m := newMachine(proc(10, 1, 10))
	e := execution(m)
	e.Log = log
	if _, err := e.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "herdsman", "logs", "api", "teardown.log"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"event":"plan"`, `"event":"action"`, `"level":"exclusive"`, `"class":"compensatable"`} {
		if !strings.Contains(text, want) {
			t.Errorf("no %s in %s", want, text)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "herdsman", "logs", "api", "teardown.log"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", info.Mode(), err)
	}
}

func TestOpenLogRefusals(t *testing.T) {
	dir := t.TempDir()
	for _, task := range []string{"", "..", "a/b", ".hidden"} {
		if _, err := OpenLog(dir, task); err == nil {
			t.Errorf("task %q accepted", task)
		}
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(file, "api"); err == nil {
		t.Fatal("a file as a folder")
	}
	folder := filepath.Join(dir, "herdsman", "logs", "api", "teardown.log")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(dir, "api"); err == nil {
		t.Fatal("a folder as the log")
	}
}

func TestANilLogDoesNothing(t *testing.T) {
	var log *Log
	log.plan(Plan{})
	log.action(Line{}, 0, 0, nil)
	log.refused(Line{}, errors.New("x"))
	log.finish(nil, nil)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoppingOneProjectLeavesOtherTargetsToTheirOwnStop(t *testing.T) {
	m := newMachine()
	m.snapshot.Containers = []Container{container("c1", "running", "p", "/w"), container("c2", "running", "q", "/w"), container("c3", "running", "", "/w")}
	for _, id := range []string{"c1", "c2", "c3"} {
		recordContainer(&m.snapshot, id)
	}
	x := &engine{}
	outcomes, err := containerExecution(m, x, "p").Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(x.calls, []string{"docker stop c1", "docker stop c2", "docker stop c3"}) {
		t.Fatalf("calls = %v", x.calls)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %v", outcomes)
	}
}
