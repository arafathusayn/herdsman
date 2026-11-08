package ledger

import (
	"bytes"
	"errors"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/plan.golden")

const (
	execAPI = "01JEXECAPI"
	execWeb = "01JEXECWEB"
)

func at(clock string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04:05", "2026-11-08 "+clock)
	if err != nil {
		panic(err)
	}
	return parsed
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func must[T any](t *testing.T) func(T, error) T {
	return func(value T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
}

// fixture is the snapshot every fixture file describes; connections names the
// lsof file so that a test can compare two moments.
func fixture(t *testing.T, connections string) Snapshot {
	t.Helper()
	processes := must[[]Process](t)(ParsePS(read(t, "ps/ps.txt"), time.UTC))
	return Snapshot{
		Scope: Scope{
			Task: "api", Execution: execAPI, Worktree: "/work/wt/api", Compose: "api",
			Ports:   PortRange{First: 4100, Last: 4109},
			Scratch: []string{"/tmp/herdsman/api", "/shared/cache"}, Databases: []string{"api_dev", "shared_db"},
		},
		Started: at("12:00:00").Add(500 * time.Millisecond),
		Earlier: []string{"01JEXECOLD"},
		Others: []Scope{{
			Task: "web", Execution: execWeb, Worktree: "/work/wt/web", Compose: "web",
			Ports: PortRange{First: 4200, Last: 4209}, Scratch: []string{"/shared/cache"}, Databases: []string{"web_dev"},
		}},
		Excluded: []Excluded{
			{Identity: ProcessIdentity(100, at("12:00:05")), Role: RolePaneShell},
			{Identity: ProcessIdentity(101, at("12:00:06")), Role: RoleAgent},
		},
		Records: []Record{
			{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "110", Start: at("12:00:10"), Command: "node server.js --port=4100"},
			{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "111", Start: at("12:00:11"), Command: "node worker.js"},
			{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "170", Start: at("12:00:50"), Command: "postgres -D /data"},
			{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "120", Start: at("12:59:59")},
			{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "4242", Start: at("12:00:01")},
			{Task: "api", Execution: execAPI, Kind: KindCompose, ID: "api"},
			{Task: "api", Execution: execAPI, Kind: KindVolume, ID: "api_data", Start: at("12:01:00")},
			{Task: "api", Execution: execAPI, Kind: KindDatabase, ID: "api_dev"},
			{Task: "api", Execution: execAPI, Kind: KindScratch, ID: "/tmp/herdsman/recorded"},
			{Task: "web", Execution: execWeb, Kind: KindDatabase, ID: "shared_db"},
			{Task: "api", Execution: "01JEXECOLD", Kind: KindProcess, ID: "111", Start: at("12:00:11")},
		},
		Processes:   processes,
		Commands:    must[map[int]string](t)(ParseCommands(read(t, "ps/commands.txt"))),
		Cwds:        must[[]CwdEntry](t)(ParseLsofCwd(read(t, "lsof/cwd.txt"))),
		Listeners:   must[[]Listener](t)(ParseLsofListeners(read(t, "lsof/listen.txt"))),
		Connections: must[[]Connection](t)(ParseLsofConnections(read(t, "lsof/"+connections))),
		Containers:  must[[]Container](t)(ParseContainerInspect(read(t, "docker/inspect.json"))),
		Volumes:     must[[]Resource](t)(ParseResources(read(t, "docker/volumes.json"))),
		Networks:    must[[]Resource](t)(ParseResources(read(t, "docker/networks.json"))),
	}
}

func line(t *testing.T, plan Plan, kind, id string) Line {
	t.Helper()
	for _, candidate := range plan.Lines {
		if candidate.Kind == kind && candidate.ID == id {
			return candidate
		}
	}
	t.Fatalf("no line %s %s in %+v", kind, id, plan.Lines)
	return Line{}
}

func TestPlanMatchesGolden(t *testing.T) {
	var got bytes.Buffer
	if err := BuildPlan(fixture(t, "connections.txt")).Encode(&got); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "plan.golden")
	if *update {
		if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("plan differs from golden:\n%s", got.Bytes())
	}
}

func TestPlanKeepsSecretsOut(t *testing.T) {
	var got bytes.Buffer
	if err := BuildPlan(fixture(t, "connections.txt")).Encode(&got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.String(), "do-not-keep") {
		t.Fatal("an environment value other than HERDSMAN_* reached the plan")
	}
}

func TestEvidenceLevelsFromFixtures(t *testing.T) {
	plan := BuildPlan(fixture(t, "connections.txt"))
	cases := []struct {
		kind, id string
		level    Level
		action   Action
	}{
		{KindProcess, "100", LevelAssociated, ActionExclude},
		{KindProcess, "101", LevelAssociated, ActionExclude},
		{KindProcess, "110", LevelExclusive, ActionStop},
		{KindProcess, "111", LevelExclusive, ActionStop},
		{KindProcess, "120", LevelShared, ActionReport},
		{KindProcess, "130", LevelShared, ActionReport},
		{KindProcess, "140", LevelShared, ActionReport},
		{KindProcess, "150", LevelShared, ActionReport},
		{KindProcess, "170", LevelShared, ActionReport},
		{KindProcess, "9999", LevelUnknown, ActionReport},
		{KindPort, "4100/110", LevelExclusive, ActionReport},
		{KindPort, "4101/160", LevelAssociated, ActionReport},
		{KindPort, "4102/8888", LevelUnknown, ActionReport},
		{KindPort, "5432/170", LevelShared, ActionReport},
		{KindPort, "4201/150", LevelShared, ActionReport},
		{KindContainer, "c1", LevelExclusive, ActionStop},
		{KindContainer, "c2", LevelAssociated, ActionAsk},
		{KindContainer, "c3", LevelExclusive, ActionReport},
		{KindContainer, "c4", LevelShared, ActionReport},
		{KindContainer, "c6", LevelShared, ActionReport},
		{KindContainer, "c7", LevelAssociated, ActionAsk},
		{KindContainer, "c8", LevelShared, ActionReport},
		{KindVolume, "api_data", LevelExclusive, ActionReport},
		{KindVolume, "api_cache", LevelExclusive, ActionReport},
		{KindNetwork, "api_default", LevelExclusive, ActionReport},
		{KindScratch, "/tmp/herdsman/api", LevelUnknown, ActionReport},
		{KindScratch, "/tmp/herdsman/recorded", LevelExclusive, ActionReport},
		{KindScratch, "/shared/cache", LevelShared, ActionReport},
		{KindDatabase, "api_dev", LevelExclusive, ActionReport},
		{KindDatabase, "shared_db", LevelShared, ActionReport},
		{KindRecord, "process:120", LevelUnknown, ActionReport},
		{KindRecord, "process:4242", LevelUnknown, ActionReport},
	}
	for _, tc := range cases {
		got := line(t, plan, tc.kind, tc.id)
		if got.Level != tc.level || got.Action != tc.action || got.Class != ClassCompensatable {
			t.Errorf("%s %s = %s/%s/%s, want %s/%s", tc.kind, tc.id, got.Level, got.Action, got.Class, tc.level, tc.action)
		}
	}
	for _, absent := range [][2]string{{KindProcess, "200"}, {KindProcess, "160"}, {KindPort, "9000/200"}, {KindContainer, "c5"}, {KindVolume, "web_data"}, {KindNetwork, "bridge"}} {
		for _, got := range plan.Lines {
			if got.Kind == absent[0] && got.ID == absent[1] {
				t.Errorf("%v must not be a candidate", absent)
			}
		}
	}
}

func TestOnlyExclusiveLinesStop(t *testing.T) {
	for _, got := range BuildPlan(fixture(t, "connections.txt")).Lines {
		if got.Action == ActionStop && got.Level != LevelExclusive {
			t.Errorf("%s %s stops at level %s", got.Kind, got.ID, got.Level)
		}
	}
}

func TestProcessLineCarriesRestartAndGroup(t *testing.T) {
	got := line(t, BuildPlan(fixture(t, "connections.txt")), KindProcess, "110")
	if got.Parent != 100 || got.Group != 110 || got.Identity != "process:110@2026-11-08T12:00:10Z" {
		t.Fatalf("line = %+v", got)
	}
	want := Restart{Command: "node server.js --port=4100", Folder: "/work/wt/api/server", Env: []string{"HERDSMAN_EXEC=" + execAPI, "HERDSMAN_TASK=api"}}
	if got.Restart == nil || got.Restart.Command != want.Command || got.Restart.Folder != want.Folder || !slices.Equal(got.Restart.Env, want.Env) {
		t.Fatalf("restart = %+v", got.Restart)
	}
}

func TestChangedLevelStopsPlan(t *testing.T) {
	approved := BuildPlan(fixture(t, "connections.txt"))
	err := CheckUnchanged(approved, fixture(t, "connections-after.txt"))
	var stop *StopError
	if !errors.As(err, &stop) {
		t.Fatalf("err = %v, want a StopError", err)
	}
	var process, port bool
	for _, change := range stop.Changes {
		if change.Kind == KindProcess && change.ID == "110" && change.FromLevel == LevelExclusive && change.ToLevel == LevelShared {
			process = true
		}
		if change.Kind == KindPort && change.ID == "4100/110" && change.ToLevel == LevelShared {
			port = true
		}
	}
	if !process || !port {
		t.Fatalf("changes = %+v", stop.Changes)
	}
	if !strings.Contains(err.Error(), "process 110: exclusive -> shared") {
		t.Fatalf("message = %q", err)
	}
}

func TestUnchangedPlanPasses(t *testing.T) {
	approved := BuildPlan(fixture(t, "connections.txt"))
	if err := CheckUnchanged(approved, fixture(t, "connections.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestVanishedLineDoesNotStopPlan(t *testing.T) {
	approved := BuildPlan(fixture(t, "connections.txt"))
	fresh := fixture(t, "connections.txt")
	fresh.Containers = slices.DeleteFunc(fresh.Containers, func(c Container) bool { return c.ID == "c2" })
	if err := CheckUnchanged(approved, fresh); err != nil {
		t.Fatal(err)
	}
}

func TestReusedPidStopsPlan(t *testing.T) {
	approved := BuildPlan(fixture(t, "connections.txt"))
	fresh := fixture(t, "connections.txt")
	for i := range fresh.Processes {
		if fresh.Processes[i].Pid == 110 {
			fresh.Processes[i].Start = at("13:00:00")
		}
	}
	var stop *StopError
	if err := CheckUnchanged(approved, fresh); !errors.As(err, &stop) || !slices.ContainsFunc(stop.Changes, func(c Change) bool { return c.Kind == KindProcess && c.ID == "110" }) {
		t.Fatalf("err = %v", err)
	}

	// The pid now belongs to an unrelated process with no evidence at all.
	unrelated := base()
	unrelated.Processes = []Process{{Pid: 5, Start: at("13:00:00"), Command: "unrelated"}}
	unrelated.Cwds, unrelated.Listeners = nil, nil
	err := CheckUnchanged(BuildPlan(base()), unrelated)
	if !errors.As(err, &stop) || !slices.ContainsFunc(stop.Changes, func(c Change) bool {
		return c.Kind == KindProcess && c.ID == "5" && c.ToLevel == LevelNoEvidence
	}) {
		t.Fatalf("err = %v", err)
	}
}

func TestAdoptedResourceMayPredateExecution(t *testing.T) {
	snapshot := fixture(t, "connections.txt")
	snapshot.Records = append(snapshot.Records, Record{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "140", Start: at("11:00:00"), Adopted: true, Command: "make dev"})
	snapshot.Processes = slices.Clone(snapshot.Processes)
	if got := line(t, BuildPlan(snapshot), KindProcess, "140"); got.Level != LevelExclusive {
		t.Fatalf("level = %s", got.Level)
	}
}

func TestStartWithinTheExecutionSecondIsNotOlder(t *testing.T) {
	snapshot := Snapshot{
		Scope: Scope{Task: "api", Execution: execAPI, Worktree: "/w"}, Started: at("12:00:00").Add(900 * time.Millisecond),
		Processes: []Process{{Pid: 5, Start: at("12:00:00"), Command: "x"}},
		Cwds:      []CwdEntry{{Pid: 5, Path: "/w"}},
	}
	if got := line(t, BuildPlan(snapshot), KindProcess, "5"); slices.Contains(got.SharedWith, SharedOlder) {
		t.Fatalf("level = %s, signals %v, shared %v", got.Level, got.Signals, got.SharedWith)
	}
}

func TestRemoteClientMakesListenerShared(t *testing.T) {
	snapshot := Snapshot{
		Scope:       Scope{Task: "api", Execution: execAPI, Worktree: "/w", Ports: PortRange{First: 4100, Last: 4101}},
		Processes:   []Process{{Pid: 5, Start: at("12:00:00"), Command: "db"}},
		Cwds:        []CwdEntry{{Pid: 5, Path: "/w"}},
		Listeners:   []Listener{{Pid: 5, Host: "*", Port: 4100}},
		Connections: []Connection{{Pid: 5, LocalHost: "10.0.0.1", LocalPort: 4100, RemoteHost: "10.0.0.9", RemotePort: 60000}},
	}
	plan := BuildPlan(snapshot)
	if got := line(t, plan, KindProcess, "5"); got.Level != LevelShared || !slices.Contains(got.SharedWith, "remote:10.0.0.9") {
		t.Fatalf("process = %+v", got)
	}
	if got := line(t, plan, KindPort, "4100/5"); got.Level != LevelShared {
		t.Fatalf("port = %+v", got)
	}
}

func TestExcludedClientStaysInside(t *testing.T) {
	agent := ProcessIdentity(6, at("12:00:00"))
	snapshot := Snapshot{
		Scope:       Scope{Task: "api", Execution: execAPI, Worktree: "/w"},
		Excluded:    []Excluded{{Identity: agent, Role: RoleAgent}},
		Processes:   []Process{{Pid: 5, Start: at("12:00:00"), Command: "db"}, {Pid: 6, Start: at("12:00:00"), Command: "agent"}},
		Cwds:        []CwdEntry{{Pid: 5, Path: "/w"}},
		Listeners:   []Listener{{Pid: 5, Host: "*", Port: 4100}},
		Connections: []Connection{{Pid: 6, LocalHost: "127.0.0.1", LocalPort: 50000, RemoteHost: "127.0.0.1", RemotePort: 4100}},
	}
	if got := line(t, BuildPlan(snapshot), KindProcess, "5"); slices.Contains(got.SharedWith, "pid:6") {
		t.Fatalf("process = %+v", got)
	}
}

func TestSharedPortBlockAndOlderListener(t *testing.T) {
	snapshot := Snapshot{
		Scope:     Scope{Task: "api", Execution: execAPI, Worktree: "/w", Ports: PortRange{First: 4100, Last: 4109}},
		Started:   at("12:00:00"),
		Others:    []Scope{{Task: "web", Ports: PortRange{First: 4105, Last: 4109}}},
		Processes: []Process{{Pid: 5, Start: at("12:30:00"), Command: "a"}, {Pid: 6, Start: at("11:00:00"), Command: "b"}},
		Listeners: []Listener{{Pid: 5, Host: "*", Port: 4105}, {Pid: 6, Host: "*", Port: 4101}},
	}
	plan := BuildPlan(snapshot)
	if got := line(t, plan, KindPort, "4105/5"); got.Level != LevelShared || !slices.Contains(got.SharedWith, "task:web") {
		t.Fatalf("overlap = %+v", got)
	}
	if got := line(t, plan, KindPort, "4101/6"); got.Level != LevelShared || !slices.Contains(got.SharedWith, SharedOlder) {
		t.Fatalf("older = %+v", got)
	}
}

func TestOtherTaskRecordAndTheirExecutionShareAProcess(t *testing.T) {
	snapshot := Snapshot{
		Scope:     Scope{Task: "api", Execution: execAPI, Worktree: "/w"},
		Records:   []Record{{Task: "web", Execution: execWeb, Kind: KindProcess, ID: "5", Start: at("12:00:00")}},
		Processes: []Process{{Pid: 5, Start: at("12:00:00"), Command: "x"}, {Pid: 7, Start: at("12:00:00"), Command: "only-web"}},
		Cwds:      []CwdEntry{{Pid: 5, Path: "/w"}},
	}
	plan := BuildPlan(snapshot)
	if got := line(t, plan, KindProcess, "5"); got.Level != LevelShared || !slices.Contains(got.SharedWith, "task:web") {
		t.Fatalf("process = %+v", got)
	}
	for _, got := range plan.Lines {
		if got.ID == "7" {
			t.Fatalf("a process with no evidence of this task is listed: %+v", got)
		}
	}
}

func TestRecordKindsMatchIdentity(t *testing.T) {
	start := at("12:00:00")
	cases := []struct {
		name   string
		record Record
		id     Identity
		want   bool
	}{
		{"process same start", Record{Kind: KindProcess, ID: "1", Start: start}, ProcessIdentity(1, start), true},
		{"process other start", Record{Kind: KindProcess, ID: "1", Start: start}, ProcessIdentity(1, start.Add(time.Second)), false},
		{"process no start", Record{Kind: KindProcess, ID: "1"}, ProcessIdentity(1, time.Time{}), false},
		{"other id", Record{Kind: KindProcess, ID: "2", Start: start}, ProcessIdentity(1, start), false},
		{"container id alone", Record{Kind: KindContainer, ID: "c"}, Identity{Kind: KindContainer, ID: "c", Start: start}, true},
		{"volume needs creation", Record{Kind: KindVolume, ID: "v"}, Identity{Kind: KindVolume, ID: "v", Start: start}, false},
		{"network with creation", Record{Kind: KindNetwork, ID: "n", Start: start}, Identity{Kind: KindNetwork, ID: "n", Start: start}, true},
	}
	for _, tc := range cases {
		if got := tc.record.matches(tc.id); got != tc.want {
			t.Errorf("%s = %v", tc.name, got)
		}
	}
}

func TestLevelFromEvidence(t *testing.T) {
	cases := []struct {
		name string
		ev   Evidence
		want Level
	}{
		{"nothing", Evidence{}, LevelUnknown},
		{"weak only", Evidence{Signals: []Signal{SignalConfigVar}}, LevelUnknown},
		{"associated", Evidence{Signals: []Signal{SignalConfigVar, SignalCwd}}, LevelAssociated},
		{"recorded", Evidence{Recorded: true}, LevelExclusive},
		{"shared overrides recorded", Evidence{Recorded: true, SharedWith: []string{"x"}}, LevelShared},
		{"unverified caps recorded", Evidence{Recorded: true, Unverified: true}, LevelUnknown},
		{"unverified caps signal", Evidence{Signals: []Signal{SignalCwd}, Unverified: true}, LevelUnknown},
	}
	for _, tc := range cases {
		if got := tc.ev.Level(); got != tc.want {
			t.Errorf("%s = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestUnderPath(t *testing.T) {
	cases := []struct {
		root, path string
		want       bool
	}{
		{"/w/a", "/w/a", true},
		{"/w/a", "/w/a/b", true},
		{"/w/a/", "/w/a/b/", true},
		{"/w/a", "/w/ab", false},
		{"", "/w", false},
		{"/w", "", false},
		{"/", "/x", true},
	}
	for _, tc := range cases {
		if got := underPath(tc.root, tc.path); got != tc.want {
			t.Errorf("underPath(%q, %q) = %v", tc.root, tc.path, got)
		}
	}
}

func TestIdentityString(t *testing.T) {
	if got := ProcessIdentity(7, time.Time{}).String(); got != "process:7" {
		t.Fatal(got)
	}
	if got := (Identity{Kind: KindVolume, ID: "v", Start: at("12:00:00")}).String(); got != "volume:v@2026-11-08T12:00:00Z" {
		t.Fatal(got)
	}
}

func TestPodmanOutput(t *testing.T) {
	containers := must[[]Container](t)(ParseContainers(read(t, "docker/podman.json")))
	if len(containers) != 2 || containers[0].ID != "p1" || containers[0].Name != "pod-a" || containers[0].Labels[LabelComposeProject] != "api" {
		t.Fatalf("containers = %+v", containers)
	}
	if !containers[0].Created.Equal(time.Unix(1794139200, 0)) || !containers[1].Created.IsZero() {
		t.Fatalf("created = %v / %v", containers[0].Created, containers[1].Created)
	}
	if !slices.Equal(containers[0].Mounts, []string{"/work/wt/api/m"}) || containers[1].Mounts != nil {
		t.Fatalf("mounts = %v / %v", containers[0].Mounts, containers[1].Mounts)
	}
}

func TestLabelValueMayHoldCommas(t *testing.T) {
	got := parseLabelList("orphan,a=1,b=x,y,c=")
	if len(got) != 3 || got["a"] != "1" || got["b"] != "x,y" || got["c"] != "" {
		t.Fatalf("labels = %v", got)
	}
}

func TestEmptyOutputsParseToNothing(t *testing.T) {
	if got, err := ParseContainers([]byte("\n")); err != nil || got != nil {
		t.Fatalf("containers = %v, %v", got, err)
	}
	if got, err := ParseResources(nil); err != nil || got != nil {
		t.Fatalf("resources = %v, %v", got, err)
	}
	if got, err := ParsePS([]byte("\n"), time.UTC); err != nil || got != nil {
		t.Fatalf("ps = %v, %v", got, err)
	}
	if got, err := ParseLsofCwd([]byte("\nz-unknown-field\n")); err != nil || got != nil {
		t.Fatalf("lsof = %v, %v", got, err)
	}
}

func TestCwdWithoutFdFieldIsAccepted(t *testing.T) {
	got := must[[]CwdEntry](t)(ParseLsofCwd([]byte("p5\nn/w\n")))
	if len(got) != 1 || got[0] != (CwdEntry{Pid: 5, Path: "/w"}) {
		t.Fatalf("got = %+v", got)
	}
}

func TestMalformedOutputIsAnError(t *testing.T) {
	big := bytes.Repeat([]byte("x"), maxInput+1)
	lsofBad := map[string][]byte{"bad pid": []byte("pabc\n"), "name before pid": []byte("n/x\n"), "too big": big}
	for name, raw := range lsofBad {
		if _, err := ParseLsofCwd(raw); err == nil {
			t.Errorf("cwd %s: no error", name)
		}
	}
	for name, raw := range map[string][]byte{"no port": []byte("p1\nnoops\n"), "bad port": []byte("p1\nn*:http\n"), "bad pid": []byte("pabc\n")} {
		if _, err := ParseLsofListeners(raw); err == nil {
			t.Errorf("listeners %s: no error", name)
		}
	}
	for name, raw := range map[string][]byte{
		"no arrow":   []byte("p1\nn127.0.0.1:1\n"),
		"bad local":  []byte("p1\nnx->127.0.0.1:1\n"),
		"bad remote": []byte("p1\nn127.0.0.1:1->y\n"),
		"bad pid":    []byte("pabc\n"),
	} {
		if _, err := ParseLsofConnections(raw); err == nil {
			t.Errorf("connections %s: no error", name)
		}
	}
	for name, raw := range map[string][]byte{
		"short":     []byte("1 2 3 Sun Nov\n"),
		"bad pid":   []byte("x 2 3 Sun Nov  8 12:00:05 2026 cmd\n"),
		"bad start": []byte("1 2 3 Sun Nov 99 12:00:05 2026 cmd\n"),
		"too big":   big,
	} {
		if _, err := ParsePS(raw, time.UTC); err == nil {
			t.Errorf("ps %s: no error", name)
		}
	}
	for name, raw := range map[string][]byte{
		"bad line":   []byte("{nope\n"),
		"bad array":  []byte("[{nope]"),
		"bad labels": []byte(`{"ID":"a","Labels":5}` + "\n"),
		"bad mounts": []byte(`{"ID":"a","Mounts":5}` + "\n"),
		"bad names":  []byte(`{"ID":"a","Names":5}` + "\n"),
		"no id":      []byte(`{"Names":"a"}` + "\n"),
		"too big":    big,
	} {
		if _, err := ParseContainers(raw); err == nil {
			t.Errorf("containers %s: no error", name)
		}
	}
	for name, raw := range map[string][]byte{
		"bad line":   []byte("{nope\n"),
		"bad labels": []byte(`{"Name":"a","Labels":5}` + "\n"),
		"no name":    []byte(`{"Labels":""}` + "\n"),
		"too big":    big,
	} {
		if _, err := ParseResources(raw); err == nil {
			t.Errorf("resources %s: no error", name)
		}
	}
}

func TestUnparsableCreationIsZero(t *testing.T) {
	if got := parseCreated("2 hours ago", nil); !got.IsZero() {
		t.Fatal(got)
	}
	if got := parseCreated("", []byte(`"text"`)); !got.IsZero() {
		t.Fatal(got)
	}
}

// signalCalls lists what the guard forbids in one source file.
func signalCalls(t *testing.T, name string, src any) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		if slices.Contains([]string{"os/exec", "os/signal", "syscall", "golang.org/x/sys/unix"}, path) {
			found = append(found, "import "+path)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if slices.Contains([]string{"Kill", "Signal", "FindProcess"}, selector.Sel.Name) {
				found = append(found, "use of "+selector.Sel.Name)
			}
		}
		return true
	})
	return found
}

func TestPackageSendsNoSignals(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("files = %v, err = %v", files, err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		if found := signalCalls(t, file, nil); len(found) > 0 {
			t.Errorf("%s: %v", file, found)
		}
	}
}

func TestSignalGuardCatchesPlantedCalls(t *testing.T) {
	cases := map[string]string{
		"import syscall":     `package p; import "syscall"; var _ = syscall.Getpid`,
		"import os/exec":     `package p; import _ "os/exec"`,
		"use of Kill":        `package p; func f(p interface{ Kill() error }) { _ = p.Kill() }`,
		"use of Signal":      `package p; func f(p interface{ Signal(int) error }) { _ = p.Signal(1) }`,
		"use of FindProcess": `package p; import "os"; var _ = os.FindProcess`,
	}
	for want, src := range cases {
		if found := signalCalls(t, "planted.go", src); !slices.Contains(found, want) {
			t.Errorf("planted %q: found %v", want, found)
		}
	}
}

func TestUnlistedRecordKindPassesThrough(t *testing.T) {
	snapshot := Snapshot{
		Scope:   Scope{Task: "api", Execution: execAPI, Worktree: "/w"},
		Records: []Record{{Task: "api", Execution: execAPI, Kind: "lock", ID: "/w/.lock"}},
	}
	if got := line(t, BuildPlan(snapshot), "lock", "/w/.lock"); got.Level != LevelExclusive || got.Action != ActionReport {
		t.Fatalf("line = %+v", got)
	}
}

func TestEnvTokenShape(t *testing.T) {
	for token, want := range map[string]bool{"A_1=x": true, "a=": true, "1A=x": false, "=x": false, "--flag=x": false, "plain": false} {
		if got := isEnvToken(token); got != want {
			t.Errorf("isEnvToken(%q) = %v", token, got)
		}
	}
}

func TestStopMessageNamesTheIdentityChange(t *testing.T) {
	change := Change{Kind: KindProcess, ID: "1", FromLevel: LevelExclusive, ToLevel: LevelExclusive, FromIdentity: "a", ToIdentity: "b"}
	if got := (&StopError{Changes: []Change{change}}).Error(); !strings.Contains(got, "(identity a -> b)") {
		t.Fatal(got)
	}
}

func TestVolumeOfAnotherTasksProjectIsShared(t *testing.T) {
	snapshot := Snapshot{
		Scope:   Scope{Task: "api", Execution: execAPI, Worktree: "/w", Compose: "api"},
		Others:  []Scope{{Task: "web", Compose: "web"}},
		Records: []Record{{Task: "api", Execution: execAPI, Kind: KindVolume, ID: "web_data", Start: at("12:30:00")}},
		Volumes: []Resource{{Name: "web_data", Created: at("12:30:00"), LabelsExact: true, Labels: map[string]string{LabelComposeProject: "web"}}},
	}
	if got := line(t, BuildPlan(snapshot), KindVolume, "web_data"); got.Level != LevelShared || !slices.Contains(got.SharedWith, "task:web") {
		t.Fatalf("line = %+v", got)
	}
}

func TestUnlistedKindHasNoVariables(t *testing.T) {
	if got := scopeVars(Scope{Scratch: []string{"a"}, Databases: []string{"b"}}, "lock"); got != nil {
		t.Fatalf("vars = %v", got)
	}
}

// base is one recorded process in the worktree, listening inside the block.
func base() Snapshot {
	start := at("12:00:01")
	return Snapshot{
		Scope:     Scope{Task: "api", Execution: execAPI, Worktree: "/w/api", Compose: "api", Ports: PortRange{First: 4100, Last: 4109}},
		Started:   at("12:00:00"),
		Processes: []Process{{Pid: 5, Start: start, Command: "server"}},
		Cwds:      []CwdEntry{{Pid: 5, Path: "/w/api"}},
		Records:   []Record{{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "5", Start: start, Command: "server"}},
		Commands:  map[int]string{5: "server"},
		Listeners: []Listener{{Pid: 5, Host: "127.0.0.1", Port: 4100}},
	}
}

func composeRecord() Record {
	return Record{Task: "api", Execution: execAPI, Kind: KindCompose, ID: "api"}
}

func TestComposeRecordNeverGrantsByNameAlone(t *testing.T) {
	label := func(dir string) map[string]string {
		return map[string]string{LabelComposeProject: "api", LabelComposeDir: dir}
	}
	created := at("12:30:00")
	cases := []struct {
		name  string
		dir   string
		extra []Record
		want  Level
	}{
		{"working dir in the worktree", "/w/api/deploy", nil, LevelExclusive},
		{"foreign working dir", "/other/api", nil, LevelShared},
		{"no working dir", "", nil, LevelShared},
		{"verified member id", "/other/api", []Record{{Task: "api", Execution: execAPI, Kind: KindContainer, ID: "c"}}, LevelExclusive},
	}
	for _, tc := range cases {
		s := base()
		s.Records = append([]Record{composeRecord()}, tc.extra...)
		s.Containers = []Container{{ID: "c", State: "running", Created: created, Labels: label(tc.dir), LabelsExact: true}}
		if got := line(t, BuildPlan(s), KindContainer, "c"); got.Level != tc.want {
			t.Errorf("%s = %s (%v), want %s", tc.name, got.Level, got.SharedWith, tc.want)
		}
	}
	volume := Resource{Name: "v", Created: created, Labels: map[string]string{LabelComposeProject: "api"}, LabelsExact: true}
	for name, tc := range map[string]struct {
		containers []Container
		want       Level
	}{
		"no member proves the project": {nil, LevelShared},
		"member proves the project":    {[]Container{{ID: "c", State: "running", Created: created, Labels: label("/w/api"), LabelsExact: true}}, LevelExclusive},
		"joined member proves nothing": {[]Container{{ID: "c", State: "running", Created: created, Labels: label("/w/api")}}, LevelShared},
		"foreign member conflicts": {[]Container{
			{ID: "c", State: "running", Created: created, Labels: label("/w/api"), LabelsExact: true},
			{ID: "d", State: "running", Created: created, Labels: label("/other/api"), LabelsExact: true},
		}, LevelShared},
	} {
		s := base()
		s.Records = []Record{composeRecord()}
		s.Containers, s.Volumes = tc.containers, []Resource{volume}
		if got := line(t, BuildPlan(s), KindVolume, "v"); got.Level != tc.want {
			t.Errorf("volume, %s = %s (%v), want %s", name, got.Level, got.SharedWith, tc.want)
		}
	}
}

func TestJoinedLabelsNeverGrantAuthority(t *testing.T) {
	s := base()
	s.Records = []Record{composeRecord()}
	s.Containers = must[[]Container](t)(ParseContainers([]byte(`{"ID":"f","State":"running","Labels":"note=hello,com.docker.compose.project=api,com.docker.compose.project.working_dir=/w/api","CreatedAt":"2026-11-08T13:00:00Z"}` + "\n")))
	s.Volumes = must[[]Resource](t)(ParseResources([]byte(`{"Name":"v","Labels":"com.docker.compose.project=api","CreatedAt":"2026-11-08T13:00:00Z"}` + "\n")))
	plan := BuildPlan(s)
	for _, got := range []Line{line(t, plan, KindContainer, "f"), line(t, plan, KindVolume, "v")} {
		if got.Level == LevelExclusive || got.Level == LevelAssociated || got.Action == ActionStop || got.Action == ActionAsk {
			t.Errorf("%s from a joined label = %s/%s", got.ID, got.Level, got.Action)
		}
	}
	noRecord := base()
	noRecord.Records = nil
	noRecord.Containers = must[[]Container](t)(ParseContainers([]byte(`{"ID":"g","State":"running","Labels":"herdsman.exec=` + execAPI + `","CreatedAt":"2026-11-08T13:00:00Z"}` + "\n")))
	if got := line(t, BuildPlan(noRecord), KindContainer, "g"); got.Level != LevelUnknown {
		t.Errorf("joined exec label = %s", got.Level)
	}
	member := base()
	member.Records = []Record{{Task: "api", Execution: execAPI, Kind: KindContainer, ID: "g"}}
	member.Containers = noRecord.Containers
	if got := line(t, BuildPlan(member), KindContainer, "g"); got.Level != LevelExclusive {
		t.Errorf("a direct id record does not depend on labels: %s", got.Level)
	}
}

func TestContainerInspectIsExact(t *testing.T) {
	raw := `[{"Id":"abc","Name":"/db","Created":"2026-11-08T12:01:00.123456789Z","State":{"Status":"running"},
	"Config":{"Labels":{"note":"x,com.docker.compose.project=api","com.docker.compose.project":"web"}},
	"Mounts":[{"Type":"bind","Source":"/w/api/data"},{"Type":"volume","Name":"v","Source":"/var/lib/docker/volumes/v/_data"}]}]`
	got := must[[]Container](t)(ParseContainerInspect([]byte(raw)))
	if len(got) != 1 || got[0].ID != "abc" || got[0].Name != "db" || got[0].State != "running" || !got[0].LabelsExact {
		t.Fatalf("got = %+v", got)
	}
	if got[0].Labels[LabelComposeProject] != "web" || !slices.Equal(got[0].Mounts, []string{"/w/api/data"}) || got[0].Created.IsZero() {
		t.Fatalf("got = %+v", got[0])
	}
	if got, err := ParseContainerInspect([]byte("\n")); err != nil || got != nil {
		t.Fatalf("empty = %v, %v", got, err)
	}
	for name, bad := range map[string][]byte{"not json": []byte("[{nope"), "no id": []byte(`[{"Name":"/a"}]`), "too big": bytes.Repeat([]byte("x"), maxInput+1)} {
		if _, err := ParseContainerInspect(bad); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestResourceInspectArrayIsExact(t *testing.T) {
	got := must[[]Resource](t)(ParseResources([]byte(`[{"Name":"n","Created":"2026-11-08T12:00:59Z","Labels":{"a":"1"}}]`)))
	if len(got) != 1 || !got[0].LabelsExact || got[0].Labels["a"] != "1" || got[0].Created.IsZero() {
		t.Fatalf("got = %+v", got)
	}
	if _, err := ParseResources([]byte("[{nope")); err == nil {
		t.Error("bad array: no error")
	}
	if got := must[[]Resource](t)(ParseResources([]byte(`{"Name":"n","Labels":"a=1"}` + "\n"))); got[0].LabelsExact {
		t.Error("a joined label string is not exact")
	}
}

func TestServerSideLoopbackWithoutClientIsOutsider(t *testing.T) {
	s := base()
	s.Connections = []Connection{{Pid: 5, LocalHost: "127.0.0.1", LocalPort: 4100, RemoteHost: "127.0.0.1", RemotePort: 52000}}
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport {
		t.Fatalf("no client row: %+v", got)
	}
	s.Connections = append(s.Connections, Connection{Pid: 5, LocalHost: "127.0.0.1", LocalPort: 52000, RemoteHost: "127.0.0.1", RemotePort: 4100})
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelExclusive {
		t.Fatalf("own client row: %+v", got)
	}
}

func TestListenerSharesReachTheOwner(t *testing.T) {
	s := base()
	s.Others = []Scope{{Task: "web", Execution: execWeb, Ports: PortRange{First: 4100, Last: 4109}}}
	plan := BuildPlan(s)
	if got := line(t, plan, KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport || !slices.Contains(got.SharedWith, "task:web") {
		t.Fatalf("owner = %+v", got)
	}
	other := base()
	other.Records, other.Cwds = nil, nil
	other.Processes[0].Env = []string{"HERDSMAN_EXEC=" + execWeb}
	if got := line(t, BuildPlan(other), KindPort, "4100/5"); got.Level != LevelShared || !slices.Contains(got.SharedWith, "execution:"+execWeb) {
		t.Fatalf("another execution in the block = %+v", got)
	}
}

func TestZeroCreationIsCappedAtUnknown(t *testing.T) {
	s := base()
	s.Records = []Record{composeRecord(), {Task: "api", Execution: execAPI, Kind: KindContainer, ID: "z"}}
	s.Containers = []Container{
		{ID: "ok", State: "running", Created: at("12:30:00"), Labels: map[string]string{LabelComposeProject: "api", LabelComposeDir: "/w/api"}, LabelsExact: true},
		{ID: "z", State: "running", LabelsExact: true},
	}
	s.Volumes = []Resource{{Name: "v", Labels: map[string]string{LabelComposeProject: "api"}, LabelsExact: true}}
	plan := BuildPlan(s)
	if got := line(t, plan, KindVolume, "v"); got.Level != LevelUnknown {
		t.Errorf("volume = %s", got.Level)
	}
	if got := line(t, plan, KindContainer, "z"); got.Level != LevelUnknown || got.Action != ActionReport {
		t.Errorf("container = %s/%s", got.Level, got.Action)
	}
	s.Records[1].Adopted = true
	if got := line(t, BuildPlan(s), KindContainer, "z"); got.Level != LevelExclusive {
		t.Errorf("adopted container = %s", got.Level)
	}
}

func TestLiveResourceThatLostItsEvidenceStopsPlan(t *testing.T) {
	s := base()
	approved := BuildPlan(s)
	fresh := base()
	fresh.Records, fresh.Cwds, fresh.Listeners = nil, nil, nil
	var stop *StopError
	if err := CheckUnchanged(approved, fresh); !errors.As(err, &stop) || !slices.ContainsFunc(stop.Changes, func(c Change) bool { return c.Kind == KindProcess && c.ID == "5" && c.ToLevel == LevelNoEvidence }) {
		t.Fatalf("err = %v", err)
	}
	exited := base()
	exited.Records, exited.Cwds, exited.Listeners = nil, nil, nil
	exited.Processes = []Process{{Pid: 99, Start: at("12:00:01"), Command: "other"}}
	if err := CheckUnchanged(approved, exited); err != nil {
		t.Fatalf("an exited process is gone: %v", err)
	}
	listening := base()
	listening.Connections = nil
	withoutPort := BuildPlan(listening)
	closed := base()
	closed.Listeners = nil
	if err := CheckUnchanged(withoutPort, closed); err != nil {
		t.Fatalf("a closed listener is gone: %v", err)
	}
	registered := base()
	registered.Records = append(registered.Records, Record{Task: "api", Execution: execAPI, Kind: KindScratch, ID: "/s"})
	dropped := base()
	if err := CheckUnchanged(BuildPlan(registered), dropped); !errors.As(err, &stop) {
		t.Fatalf("a dropped registration is a change: %v", err)
	}
}

func TestAnotherExecutionsRecordIsShared(t *testing.T) {
	s := base()
	s.Records = append(s.Records, Record{Task: "api", Execution: "01JOTHERPROJECT", Kind: KindProcess, ID: "5", Start: at("12:00:01")})
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelShared || !slices.Contains(got.SharedWith, "execution:01JOTHERPROJECT") {
		t.Fatalf("got = %+v", got)
	}
	s.Earlier = []string{"01JOTHERPROJECT"}
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelExclusive {
		t.Fatalf("an earlier execution of this task: %+v", got)
	}
}

func TestCutCommandIsMarked(t *testing.T) {
	got := must[[]Process](t)(ParsePS([]byte("1 1 1 Sun Nov  8 12:00:05 2026 make CC=clang build HOME=/h\n2 1 1 Sun Nov  8 12:00:05 2026 node x HOME=/h\n"), time.UTC))
	if got[0].Command != "make" || !got[0].Cut {
		t.Fatalf("cut = %+v", got[0])
	}
	if got[1].Cut {
		t.Fatalf("whole = %+v", got[1])
	}
	s := base()
	s.Processes[0].Cut = true
	if l := line(t, BuildPlan(s), KindProcess, "5"); l.Restart == nil || !l.Restart.Cut {
		t.Fatalf("line = %+v", l)
	}
}

func TestLastExecTokenWins(t *testing.T) {
	got := must[[]Process](t)(ParsePS([]byte("1 1 1 Sun Nov  8 12:00:05 2026 x HERDSMAN_EXEC=fake HOME=/h HERDSMAN_EXEC=real\n"), time.UTC))
	if execution, ok := got[0].execution(); !ok || execution != "real" {
		t.Fatalf("execution = %q %v", execution, ok)
	}
}

func TestCommandNeverKeepsASecret(t *testing.T) {
	for _, column := range []string{
		"node x npm.config/token=SECRET HOME=/h",
		"tool --token=SECRET --port=1",
		"tool --password SECRET --port=1",
		"tool --api-key=SECRET",
	} {
		got := must[[]Process](t)(ParsePS([]byte("1 1 1 Sun Nov  8 12:00:05 2026 "+column+"\n"), time.UTC))
		if strings.Contains(got[0].Command, "SECRET") || slices.ContainsFunc(got[0].Env, func(e string) bool { return strings.Contains(e, "SECRET") }) {
			t.Errorf("%q kept a secret: %+v", column, got[0])
		}
	}
}

func TestOnlyRunningContainersStop(t *testing.T) {
	for state, want := range map[string]Action{"running": ActionStop, "paused": ActionStop, "restarting": ActionStop, "stopped": ActionReport, "configured": ActionReport, "initialized": ActionReport, "stopping": ActionReport, "exited": ActionReport, "": ActionReport} {
		s := base()
		s.Records = []Record{{Task: "api", Execution: execAPI, Kind: KindContainer, ID: "c"}}
		s.Containers = []Container{{ID: "c", State: state, Created: at("12:30:00"), LabelsExact: true}}
		if got := line(t, BuildPlan(s), KindContainer, "c"); got.Action != want {
			t.Errorf("state %q = %s, want %s", state, got.Action, want)
		}
	}
}

func TestRegisteredIdsAreNotDuplicated(t *testing.T) {
	s := base()
	s.Scratch = []string{"/s", "/s"}
	s.Records = append(s.Records, Record{Task: "api", Execution: execAPI, Kind: "lock", ID: "l"}, Record{Task: "api", Execution: execAPI, Kind: "lock", ID: "l"})
	count := map[string]int{}
	for _, got := range BuildPlan(s).Lines {
		count[got.Kind+got.ID]++
	}
	if count[KindScratch+"/s"] != 1 || count["lockl"] != 1 {
		t.Fatalf("count = %v", count)
	}
}

func TestDockerPSRowsAreNotExact(t *testing.T) {
	got := must[[]Container](t)(ParseContainers(read(t, "docker/ps.jsonl")))
	if len(got) != 2 || got[0].LabelsExact {
		t.Fatalf("got = %+v", got)
	}
	empty := must[[]Container](t)(ParseContainers([]byte(`{"ID":"x","Mounts":""}` + "\n")))
	if empty[0].Mounts != nil {
		t.Fatalf("mounts = %v", empty[0].Mounts)
	}
}

func TestPodmanLabelsAreExact(t *testing.T) {
	got := must[[]Container](t)(ParseContainers(read(t, "docker/podman.json")))
	if !got[0].LabelsExact {
		t.Fatalf("got = %+v", got[0])
	}
}

func TestUnrelatedSocketCannotVerifyALoopbackClient(t *testing.T) {
	approved := BuildPlan(base())
	s := base()
	s.Processes = append(s.Processes, Process{Pid: 9, Start: at("12:00:01"), Command: "external-client"})
	s.Cwds = append(s.Cwds, CwdEntry{Pid: 9, Path: "/other/project"})
	s.Connections = []Connection{
		{Pid: 5, LocalHost: "127.0.0.1", LocalPort: 4100, RemoteHost: "127.0.0.1", RemotePort: 52000},
		{Pid: 9, LocalHost: "192.168.1.5", LocalPort: 52000, RemoteHost: "203.0.113.1", RemotePort: 4100},
	}
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport {
		t.Errorf("an unrelated socket verified the client: %+v", got)
	}
	if err := CheckUnchanged(approved, s); err == nil {
		t.Error("revalidation passed after an unattributed client appeared")
	}

	// A self-connection of a process that is not a task process proves nothing.
	self := base()
	self.Records, self.Cwds = nil, nil
	self.Connections = []Connection{
		{Pid: 5, LocalHost: "127.0.0.1", LocalPort: 4100, RemoteHost: "127.0.0.1", RemotePort: 52000},
		{Pid: 5, LocalHost: "127.0.0.1", LocalPort: 52000, RemoteHost: "127.0.0.1", RemotePort: 4100},
	}
	if got := line(t, BuildPlan(self), KindPort, "4100/5"); got.Level != LevelShared || !slices.Contains(got.SharedWith, "unattributed:52000") {
		t.Errorf("self-connection outside the task: %+v", got)
	}
}

func TestOnlyTheRecordedCommandIsActionable(t *testing.T) {
	const npm = "/bin/sh -c npm run dev"
	probes := []string{
		"nvim main.go", "-zsh", "/bin/bash --login -i", npm,
		"bash --rcfile /a b/rc", "bash --rcfile=/a b/rc", "bash --init-file /Volumes/me/Application Support/x.sh -i",
		"/bin/bash --rcfile /w/my api/bashrc -i", "/bin/bash --init-file /w/my api/x -i", "bash -i --rcfile /My Work/rc", "/bin/bash --init-file /Applications/Visual Studio Code - Insiders.app/x",
		"bash -rcfile /etc/x", "bash -restricted", "fish -f no-stderr-nocaret", "fish -p /tmp/prof",
		"dash", "dash -i", "ksh -o vi", "mksh", "ash", "rbash", "csh", "tcsh", "nu", "pwsh", "xonsh", "elvish", "bash-5.2", "zsh-5.9",
		"Vim f", "/Applications/MacVim.app/Contents/MacOS/Vim f", "Emacs f", "gvim f", "emacsclient -nw f", "micro f", "kak f",
		"some-unknown-program --flag",
	}
	for _, command := range probes {
		for name, recorded := range map[string]string{"no record": "-", "record without a command": "", "another command": "other"} {
			s := base()
			s.Listeners = nil
			s.Processes[0].Command = command
			s.Commands[5] = command
			s.Processes[0].Env = []string{"HERDSMAN_EXEC=" + execAPI}
			if recorded == "-" {
				s.Records = nil
			} else {
				s.Records[0].Command = recorded
			}
			if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport {
				t.Errorf("%q, %s = %s/%s", command, name, got.Level, got.Action)
			}
		}
	}
	proven := base()
	proven.Listeners = nil
	proven.Processes[0].Command, proven.Records[0].Command = npm, npm
	proven.Commands[5] = npm
	if got := line(t, BuildPlan(proven), KindProcess, "5"); got.Level != LevelExclusive || got.Action != ActionStop {
		t.Errorf("recorded command = %s/%s", got.Level, got.Action)
	}
	delete(proven.Commands, 5)
	if got := line(t, BuildPlan(proven), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport {
		t.Errorf("missing argv read = %s/%s", got.Level, got.Action)
	}
	proven.Commands[5] = "other"
	if got := line(t, BuildPlan(proven), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport {
		t.Errorf("argv read disagrees = %s/%s", got.Level, got.Action)
	}
	empty := base()
	empty.Listeners = nil
	empty.Processes[0].Command, empty.Records[0].Command = "", ""
	empty.Commands[5] = ""
	if got := line(t, BuildPlan(empty), KindProcess, "5"); got.Level != LevelShared {
		t.Errorf("empty command = %s", got.Level)
	}
	role := base()
	role.Listeners, role.Records = nil, nil
	role.Processes[0].Command = "-zsh"
	role.Excluded = []Excluded{{Identity: ProcessIdentity(5, at("12:00:01")), Role: RolePaneShell}}
	if got := line(t, BuildPlan(role), KindProcess, "5"); got.Action != ActionExclude || got.Level == LevelShared {
		t.Errorf("role = %s/%s", got.Level, got.Action)
	}
}

func TestClientProvenOnlyByAFolderIsNotInside(t *testing.T) {
	client := func(env []string) Snapshot {
		s := base()
		s.Processes = append(s.Processes, Process{Pid: 6, Start: at("12:00:02"), Command: "psql -h 127.0.0.1", Env: env})
		s.Cwds = append(s.Cwds, CwdEntry{Pid: 6, Path: "/w/api"})
		s.Connections = []Connection{
			{Pid: 6, LocalHost: "127.0.0.1", LocalPort: 52000, RemoteHost: "127.0.0.1", RemotePort: 4100},
			{Pid: 5, LocalHost: "127.0.0.1", LocalPort: 4100, RemoteHost: "127.0.0.1", RemotePort: 52000},
		}
		return s
	}
	if got := line(t, BuildPlan(client(nil)), KindProcess, "5"); got.Level != LevelShared || !slices.Contains(got.SharedWith, "pid:6") {
		t.Errorf("folder-only client: %+v", got)
	}
	if got := line(t, BuildPlan(client([]string{"HERDSMAN_EXEC=" + execAPI})), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport || !slices.Contains(got.SharedWith, "pid:6") {
		t.Errorf("the task's own marker: %+v", got)
	}
}

func TestPidLsofStillShowsIsLive(t *testing.T) {
	gone := func() Snapshot {
		s := base()
		s.Processes = []Process{{Pid: 99, Start: at("12:00:01"), Command: "other"}}
		s.Records, s.Cwds, s.Listeners = nil, nil, nil
		return s
	}
	approved := BuildPlan(base())
	for name, fresh := range map[string]func(*Snapshot){
		"cwd":      func(s *Snapshot) { s.Cwds = []CwdEntry{{Pid: 5, Path: "/other"}} },
		"listener": func(s *Snapshot) { s.Listeners = []Listener{{Pid: 5, Host: "*", Port: 9000}} },
		"connection": func(s *Snapshot) {
			s.Connections = []Connection{{Pid: 5, LocalHost: "127.0.0.1", LocalPort: 1, RemoteHost: "127.0.0.1", RemotePort: 2}}
		},
	} {
		s := gone()
		fresh(&s)
		var stop *StopError
		if err := CheckUnchanged(approved, s); !errors.As(err, &stop) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := CheckUnchanged(approved, gone()); err != nil {
		t.Errorf("a pid nothing shows is gone: %v", err)
	}
}

func TestEmptyFreshSnapshotNeverPasses(t *testing.T) {
	var stop *StopError
	if err := CheckUnchanged(BuildPlan(base()), Snapshot{}); !errors.As(err, &stop) {
		t.Fatalf("no process rows: err = %v", err)
	}
	s := base()
	s.Records = []Record{{Task: "api", Execution: execAPI, Kind: KindContainer, ID: "c"}, s.Records[0]}
	s.Containers = []Container{{ID: "c", State: "running", Created: at("12:30:00"), LabelsExact: true}}
	fresh := s
	fresh.Containers = nil
	if err := CheckUnchanged(BuildPlan(s), fresh); !errors.As(err, &stop) {
		t.Fatalf("no container rows: err = %v", err)
	}
}

func TestOutsidePeerOnANonLoopbackAddressIsShared(t *testing.T) {
	s := base()
	s.Processes = append(s.Processes, Process{Pid: 9, Start: at("12:00:02"), Command: "outside"})
	s.Connections = []Connection{
		{Pid: 5, LocalHost: "192.168.1.5", LocalPort: 4100, RemoteHost: "127.0.0.1", RemotePort: 52000},
		{Pid: 9, LocalHost: "127.0.0.1", LocalPort: 52000, RemoteHost: "192.168.1.5", RemotePort: 4100},
	}
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport || !slices.Contains(got.SharedWith, "pid:9") {
		t.Errorf("outside peer: %+v", got)
	}
	if err := CheckUnchanged(BuildPlan(base()), s); err == nil {
		t.Error("revalidation passed after an outside peer appeared")
	}
}

func TestForkedHandlerSocketIsServerSide(t *testing.T) {
	handler := func(rows ...Connection) Snapshot {
		s := base()
		s.Processes = append(s.Processes, Process{Pid: 6, Parent: 5, Start: at("12:00:02"), Command: "server: handler"})
		s.Connections = rows
		return s
	}
	remote := handler(Connection{Pid: 6, LocalHost: "10.0.0.1", LocalPort: 4100, RemoteHost: "10.0.0.9", RemotePort: 50000})
	if got := line(t, BuildPlan(remote), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport || !slices.Contains(got.SharedWith, "remote:10.0.0.9") {
		t.Errorf("remote client of a handler: %+v", got)
	}
	loopback := handler(Connection{Pid: 6, LocalHost: "127.0.0.1", LocalPort: 4100, RemoteHost: "127.0.0.1", RemotePort: 50000})
	if got := line(t, BuildPlan(loopback), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport || !slices.Contains(got.SharedWith, "unattributed:50000") {
		t.Errorf("hidden loopback client of a handler: %+v", got)
	}
	proven := handler(
		Connection{Pid: 6, LocalHost: "127.0.0.1", LocalPort: 4100, RemoteHost: "127.0.0.1", RemotePort: 50000},
		Connection{Pid: 7, LocalHost: "127.0.0.1", LocalPort: 50000, RemoteHost: "127.0.0.1", RemotePort: 4100},
	)
	proven.Processes = append(proven.Processes, Process{Pid: 7, Start: at("12:00:03"), Command: "client"})
	proven.Records = append(proven.Records, Record{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "7", Start: at("12:00:03"), Command: "client"})
	proven.Commands[7] = "client"
	if got := line(t, BuildPlan(proven), KindProcess, "5"); got.Level != LevelExclusive {
		t.Errorf("proven client of a handler: %+v", got)
	}
}

func TestMarkerAloneNeverMakesAClientInside(t *testing.T) {
	marked := must[[]Process](t)(ParsePS([]byte("6 1 6 Sun Nov  8 12:00:02 2026 psql HERDSMAN_EXEC="+execAPI+"\n"), time.UTC))
	s := base()
	s.Processes = append(s.Processes, marked...)
	s.Connections = []Connection{
		{Pid: 6, LocalHost: "127.0.0.1", LocalPort: 52000, RemoteHost: "127.0.0.1", RemotePort: 4100},
		{Pid: 5, LocalHost: "127.0.0.1", LocalPort: 4100, RemoteHost: "127.0.0.1", RemotePort: 52000},
	}
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport {
		t.Fatalf("marker from argv: %+v", got)
	}
	s.Records = append(s.Records, Record{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "6", Start: at("12:00:02"), Command: "psql"})
	s.Commands[6] = "psql"
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelExclusive {
		t.Fatalf("proven client: %+v", got)
	}
}

func TestSpacedEnvironmentValueKeepsTheProof(t *testing.T) {
	row := "5 1 5 Sun Nov  8 12:00:01 2026 server SSH_CONNECTION=10.0.0.1 50000 10.0.0.2 22 HERDSMAN_EXEC=" + execAPI + "\n"
	s := base()
	s.Listeners = nil
	s.Processes = must[[]Process](t)(ParsePS([]byte(row), time.UTC))
	if s.Processes[0].Command != "server" {
		t.Fatalf("process = %+v", s.Processes[0])
	}
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelExclusive || got.Action != ActionStop {
		t.Fatalf("spaced value: %+v", got)
	}
}

func TestHiddenValueIsMarkedAndTheArgvReadStillProves(t *testing.T) {
	got := must[[]Process](t)(ParsePS([]byte("5 1 5 Sun Nov  8 12:00:01 2026 server --token=S\n"), time.UTC))
	if !got[0].Cut || strings.Contains(got[0].Command, "=S") {
		t.Fatalf("process = %+v", got[0])
	}
	s := base()
	s.Listeners, s.Processes = nil, got
	s.Commands[5], s.Records[0].Command = "server --token=S", "server --token=S"
	plan := line(t, BuildPlan(s), KindProcess, "5")
	if plan.Level != LevelExclusive || plan.Restart == nil || !plan.Restart.Cut || strings.Contains(plan.Restart.Command, "=S") {
		t.Fatalf("line = %+v", plan)
	}
}

func TestClientSocketWithTwoOwners(t *testing.T) {
	server := Connection{Pid: 5, LocalHost: "192.168.1.5", LocalPort: 4100, RemoteHost: "127.0.0.1", RemotePort: 52000}
	inside := Connection{Pid: 6, LocalHost: "127.0.0.1", LocalPort: 52000, RemoteHost: "192.168.1.5", RemotePort: 4100}
	outside := inside
	outside.Pid = 9
	for name, rows := range map[string][]Connection{"inside first": {server, inside, outside}, "outside first": {server, outside, inside}} {
		s := base()
		s.Processes = append(s.Processes, Process{Pid: 6, Start: at("12:00:02"), Command: "client"}, Process{Pid: 9, Start: at("12:00:02"), Command: "outside"})
		s.Records = append(s.Records, Record{Task: "api", Execution: execAPI, Kind: KindProcess, ID: "6", Start: at("12:00:02"), Command: "client"})
		s.Commands[6] = "client"
		s.Connections = rows
		if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport || !slices.Contains(got.SharedWith, "pid:9") {
			t.Errorf("%s: %+v", name, got)
		}
		if err := CheckUnchanged(BuildPlan(base()), s); err == nil {
			t.Errorf("%s: revalidation passed", name)
		}
	}
}

func TestClientRowToAnyAddressOnTheListenersPortIsOutside(t *testing.T) {
	s := base()
	s.Connections = []Connection{{Pid: 9, LocalHost: "192.168.1.5", LocalPort: 52000, RemoteHost: "192.168.1.5", RemotePort: 4100}}
	if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelShared || !slices.Contains(got.SharedWith, "pid:9") {
		t.Fatalf("got = %+v", got)
	}
}

func TestCommandSuffixIsNotProof(t *testing.T) {
	for _, suffix := range []string{"MODE=other", "HERDSMAN_EXEC=" + execAPI, "file=/other/project/config"} {
		column := "server " + suffix
		s := base()
		s.Listeners = nil
		s.Processes = must[[]Process](t)(ParsePS([]byte("5 1 5 Sun Nov  8 12:00:01 2026 "+column+" HERDSMAN_EXEC="+execAPI+"\n"), time.UTC))
		s.Commands = must[map[int]string](t)(ParseCommands([]byte("5 " + column + "\n")))
		if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelShared || got.Action != ActionReport {
			t.Errorf("record of a prefix, %q = %s/%s", suffix, got.Level, got.Action)
		}
		s.Records[0].Command = column
		if got := line(t, BuildPlan(s), KindProcess, "5"); got.Level != LevelExclusive {
			t.Errorf("record of the whole argv, %q = %s", suffix, got.Level)
		}
	}
}

func TestParseCommands(t *testing.T) {
	got := must[map[int]string](t)(ParseCommands([]byte("  7   /a  b c  \n\n8\n")))
	if got[7] != "/a  b c" || len(got) != 2 {
		t.Fatalf("got = %v", got)
	}
	if command, ok := got[8]; !ok || command != "" {
		t.Fatalf("no command = %q, %v", command, ok)
	}
	for name, bad := range map[string][]byte{"bad pid": []byte("x cmd\n"), "too big": bytes.Repeat([]byte("x"), maxInput+1)} {
		if _, err := ParseCommands(bad); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
