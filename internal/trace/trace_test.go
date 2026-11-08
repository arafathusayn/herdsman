package trace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/internal/hooks"
)

const fixture = "testdata/fixture-route"

func collectFixture(t *testing.T) Trace {
	t.Helper()
	got, err := Collect(Sources{Route: fixture, UsageFile: "testdata/usage.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func byKind(tr Trace, kind string) []Span {
	var found []Span
	for _, s := range tr.Spans {
		if s.Kind == kind {
			found = append(found, s)
		}
	}
	return found
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The span names and fields are a compatibility surface: the committed fixture
// must replay to the committed output.
func TestFixtureReplaysToGolden(t *testing.T) {
	data, err := json.MarshalIndent(collectFixture(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	golden := "testdata/trace.golden.json"
	if os.Getenv("HERDSMAN_WRITE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(data) {
		t.Fatalf("output differs from %s:\n%s", golden, data)
	}
}

func TestCollectCoversEveryKind(t *testing.T) {
	tr := collectFixture(t)
	for _, kind := range []string{KindRoute, KindTurn, KindCommand, KindCheck, KindReview, KindHook} {
		if len(byKind(tr, kind)) == 0 {
			t.Errorf("no %s span", kind)
		}
	}
	if tr.Skipped != 2 {
		t.Errorf("skipped %d, want the bad command line and the unknown kind", tr.Skipped)
	}
	root := byKind(tr, KindRoute)[0]
	if root.Tokens == nil || *root.Tokens != 1000 || *root.CostMicros != 15000 {
		t.Errorf("the root must carry this route's measured usage only: %+v", root)
	}
	if root.DurationMS != 5000 {
		t.Errorf("root duration %v", root.DurationMS)
	}
	for _, s := range tr.Spans {
		if s.Kind != KindRoute && s.ParentID != root.SpanID {
			t.Errorf("%s has parent %q", s.Name, s.ParentID)
		}
	}
	hook := byKind(tr, KindHook)[0]
	if hook.Name != "post-start/deps" || !hook.StartApproximate || hook.Task != "impl" {
		t.Errorf("hook span %+v", hook)
	}
}

func TestCollectKeepsSecretsOut(t *testing.T) {
	tr := collectFixture(t)
	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret", "bearer abc", "TOKEN=abc", "/srv/repo"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("trace holds %q: %s", leak, data)
		}
	}
	commands := byKind(tr, KindCommand)
	if commands[0].Name != "git fetch" || commands[1].Name != "git status" {
		t.Errorf("names %q %q", commands[0].Name, commands[1].Name)
	}
}

func TestGitNameSkipsGlobalOptions(t *testing.T) {
	cases := map[string][]string{
		"git log":  {"-C", "/x", "--no-pager", "log", "-1"},
		"git":      {"--version"},
		"git push": {"-c", "a=b", "push"},
	}
	for want, args := range cases {
		if got := gitName(args); got != want {
			t.Errorf("%v: got %q want %q", args, got, want)
		}
	}
}

// The CLI may not reach the writers' packages, so the readers keep their own
// copy of each format; these tests pin those copies to the real writers.
func TestReadsWhatTheCommandTraceWrites(t *testing.T) {
	route := t.TempDir()
	if CommandsFile != gitrun.TraceFile {
		t.Fatalf("file name %q, the writer uses %q", CommandsFile, gitrun.TraceFile)
	}
	gitrun.NewFileTrace(filepath.Join(route, "logs")).Trace(gitrun.Entry{
		Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Dir: "/d", Args: []string{"status"}, Exit: 1, DurationMS: 2.5,
	})
	tr, err := Collect(Sources{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	spans := byKind(tr, KindCommand)
	if len(spans) != 1 || *spans[0].Exit != 1 || spans[0].DurationMS != 2.5 || spans[0].Args != "status" {
		t.Fatalf("spans %+v", spans)
	}
}

func TestReadsWhatTheHookRunnerWrites(t *testing.T) {
	logs := t.TempDir()
	runner := &hooks.Runner{LogDir: logs, Dir: t.TempDir(), Env: hooks.Env{Task: "t1"}}
	plan := hooks.Plan{{{Command: hooks.Command{Name: "ok"}, Rendered: "exit 0"}}}
	if _, err := runner.Run(context.Background(), "post-create", plan); err != nil {
		t.Fatal(err)
	}
	tr, err := Collect(Sources{Route: t.TempDir(), HookLogs: []string{logs}})
	if err != nil {
		t.Fatal(err)
	}
	spans := byKind(tr, KindHook)
	if len(spans) != 1 || spans[0].Name != "post-create/ok" || spans[0].Task != "t1" || *spans[0].Exit != 0 {
		t.Fatalf("spans %+v", spans)
	}
}

func TestCollectFailures(t *testing.T) {
	if _, err := Collect(Sources{Route: filepath.Join(t.TempDir(), "gone")}); err == nil {
		t.Error("a missing route was accepted")
	}
	file := filepath.Join(t.TempDir(), "f")
	writeFile(t, file, "x")
	if _, err := Collect(Sources{Route: file}); err == nil {
		t.Error("a file was accepted as a route")
	}
	if _, err := Collect(Sources{Route: t.TempDir(), HookLogs: []string{filepath.Join(t.TempDir(), "gone")}}); err == nil {
		t.Error("a missing hook folder was accepted")
	}
	if _, err := Collect(Sources{Route: t.TempDir(), UsageFile: t.TempDir()}); err == nil {
		t.Error("a folder was accepted as the usage file")
	}
	for _, rel := range []string{"logs/commands.jsonl", "trace/spans.jsonl"} {
		route := t.TempDir()
		if err := os.MkdirAll(filepath.Join(route, rel), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Collect(Sources{Route: route}); err == nil {
			t.Errorf("an unreadable %s was accepted", rel)
		}
	}
	long := t.TempDir()
	writeFile(t, filepath.Join(long, "logs/commands.jsonl"), strings.Repeat("x", maxLine+1))
	if _, err := Collect(Sources{Route: long}); err == nil {
		t.Error("an oversized line was accepted")
	}
	folder := t.TempDir()
	writeFile(t, filepath.Join(folder, "logs/hooks/t/h/20260101T000000.000000000/result.json/x"), "")
	if tr, err := Collect(Sources{Route: folder}); err != nil || len(tr.Spans) != 0 {
		t.Errorf("a folder called result.json must be ignored: %v %+v", err, tr)
	}
}

func TestCollectSkipsBadRecords(t *testing.T) {
	route := t.TempDir()
	writeFile(t, filepath.Join(route, "logs/commands.jsonl"), `{"time":"2026-01-01T00:00:00Z","args":["status"],"exit":0,"duration_ms":-1}`+"\n\n")
	writeFile(t, filepath.Join(route, "trace/spans.jsonl"), `{"kind":"turn","name":"","start":"2026-01-01T00:00:00Z","duration_ms":1}`+"\n")
	stamp := filepath.Join(route, "logs/hooks/t/h")
	writeFile(t, filepath.Join(stamp, "not-a-stamp/result.json"), `{"hook":"h","commands":[]}`)
	writeFile(t, filepath.Join(stamp, "20260101T000000.000000000/result.json"), `{`)
	writeFile(t, filepath.Join(stamp, "20260101T000001.000000000/result.json"), `{"hook":"h","commands":[{"name":"n","exit":0,"duration_ms":-5}]}`)
	usage := filepath.Join(t.TempDir(), "usage.jsonl")
	writeFile(t, usage, `{"route":"`+filepath.Base(route)+`","tokens":-1}`+"\n")
	tr, err := Collect(Sources{Route: route, UsageFile: usage})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Skipped != 6 || len(tr.Spans) != 0 {
		t.Fatalf("skipped %d spans %+v", tr.Skipped, tr.Spans)
	}
}

func TestRootSumsSpansWithoutUsage(t *testing.T) {
	route := t.TempDir()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tokens, cost := int64(4), int64(9)
	for range 2 {
		if err := Append(route, Span{Kind: KindTurn, Name: "same", Start: start, DurationMS: 1, Tokens: &tokens, CostMicros: &cost}); err != nil {
			t.Fatal(err)
		}
	}
	tr, err := Collect(Sources{Route: route, UsageFile: filepath.Join(route, "absent")})
	if err != nil {
		t.Fatal(err)
	}
	root := byKind(tr, KindRoute)[0]
	if *root.Tokens != 8 || *root.CostMicros != 18 {
		t.Fatalf("root %+v", root)
	}
	turns := byKind(tr, KindTurn)
	if turns[0].SpanID == turns[1].SpanID {
		t.Fatal("identical records need distinct span ids")
	}
	again, err := Collect(Sources{Route: route})
	if err != nil || again.Spans[1].SpanID != tr.Spans[1].SpanID || again.TraceID != tr.TraceID {
		t.Fatalf("ids must be stable between runs: %v", err)
	}
}

func recordSame(t *testing.T, route string) Trace {
	t.Helper()
	span := Span{Kind: KindTurn, Name: "same", Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 1}
	if err := Append(route, span); err != nil {
		t.Fatal(err)
	}
	tr, err := Collect(Sources{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestRoutesWithTheSameNameHaveTheirOwnIdentity(t *testing.T) {
	first := filepath.Join(t.TempDir(), "route")
	second := filepath.Join(t.TempDir(), "route")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	a, b := recordSame(t, first), recordSame(t, second)
	if a.TraceID == b.TraceID || a.Spans[0].SpanID == b.Spans[0].SpanID || a.Spans[1].SpanID == b.Spans[1].SpanID {
		t.Fatalf("independent routes share ids: %+v %+v", a, b)
	}
	if len(a.TraceID) != 32 {
		t.Fatalf("trace id %q", a.TraceID)
	}
}

func TestRouteIdentitySurvivesAMove(t *testing.T) {
	parent := t.TempDir()
	before := filepath.Join(parent, "before")
	if err := os.Mkdir(before, 0o700); err != nil {
		t.Fatal(err)
	}
	first := recordSame(t, before)
	after := filepath.Join(parent, "after")
	if err := os.Rename(before, after); err != nil {
		t.Fatal(err)
	}
	moved, err := Collect(Sources{Route: after})
	if err != nil || moved.TraceID != first.TraceID {
		t.Fatalf("%v: %q then %q", err, first.TraceID, moved.TraceID)
	}
}

func TestEmptyRouteGetsNoIdentityFile(t *testing.T) {
	route := t.TempDir()
	if _, err := Collect(Sources{Route: route}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(route, traceDir, routeIDFile)); !os.IsNotExist(err) {
		t.Fatalf("reading an empty route wrote to it: %v", err)
	}
}

func TestLosingTheRaceToCreateAnIdentityKeepsTheWinner(t *testing.T) {
	route := t.TempDir()
	path := filepath.Join(route, traceDir, routeIDFile)
	winner := "abcdefabcdefabcdefabcdefabcdefab"
	writeFile(t, path, winner+"\n")
	got, err := createRouteID(route, path)
	if err != nil || got != winner {
		t.Fatalf("%q %v", got, err)
	}
	blocked := t.TempDir()
	writeFile(t, filepath.Join(blocked, traceDir), "a file where the folder goes")
	got, err = createRouteID(blocked, filepath.Join(blocked, traceDir, routeIDFile))
	if err != nil || len(got) != 32 {
		t.Fatalf("no folder falls back: %q %v", got, err)
	}
}

func TestUnwritableTraceFolderFallsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes every folder")
	}
	route := t.TempDir()
	dir := filepath.Join(route, traceDir)
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	got, err := createRouteID(route, filepath.Join(dir, routeIDFile))
	if err != nil || got != digest("path", route)[:32] {
		t.Fatalf("%q %v", got, err)
	}
}

func TestDamagedRouteIdentityIsAnError(t *testing.T) {
	route := t.TempDir()
	writeFile(t, filepath.Join(route, traceDir, routeIDFile), "short")
	writeFile(t, filepath.Join(route, traceDir, spansFile), `{"kind":"turn","name":"n","start":"2026-01-01T00:00:00Z","duration_ms":1}`+"\n")
	if _, err := Collect(Sources{Route: route}); err == nil {
		t.Fatal("a damaged identity file was accepted")
	}
}

func TestLinkedRouteIdentityIsRefused(t *testing.T) {
	route := t.TempDir()
	writeFile(t, filepath.Join(route, traceDir, spansFile), `{"kind":"turn","name":"n","start":"2026-01-01T00:00:00Z","duration_ms":1}`+"\n")
	target := filepath.Join(t.TempDir(), "id")
	writeFile(t, target, "0123456789abcdef0123456789abcdef\n")
	if err := os.Symlink(target, filepath.Join(route, traceDir, routeIDFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(Sources{Route: route}); err == nil {
		t.Fatal("a linked identity file was accepted")
	}
}

func TestReadOnlyRouteFallsBackToItsPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes every folder")
	}
	parent := t.TempDir()
	var ids []string
	for _, name := range []string{"one", "two"} {
		route := filepath.Join(parent, name, "route")
		writeFile(t, filepath.Join(route, logsDir, CommandsFile), `{"time":"2026-01-01T00:00:00Z","args":["status"],"exit":0,"duration_ms":1}`+"\n")
		if err := os.Chmod(route, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(route, 0o700) })
		for range 2 {
			tr, err := Collect(Sources{Route: route})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, tr.TraceID)
		}
	}
	if ids[0] != ids[1] || ids[2] != ids[3] || ids[0] == ids[2] || len(ids[0]) != 32 {
		t.Fatalf("ids %v", ids)
	}
}

func TestRootLeavesAnUnreportedMeasureUnset(t *testing.T) {
	route := t.TempDir()
	tokens := int64(4)
	if err := Append(route, Span{Kind: KindTurn, Name: "n", Start: time.Unix(1, 0), DurationMS: 1, Tokens: &tokens}); err != nil {
		t.Fatal(err)
	}
	tr, err := Collect(Sources{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	if root := tr.Spans[0]; root.Tokens == nil || *root.Tokens != 4 || root.CostMicros != nil {
		t.Fatalf("root %+v", root)
	}
	costOnly := t.TempDir()
	cost := int64(9)
	if err := Append(costOnly, Span{Kind: KindTurn, Name: "n", Start: time.Unix(1, 0), DurationMS: 1, CostMicros: &cost}); err != nil {
		t.Fatal(err)
	}
	usage := filepath.Join(t.TempDir(), "usage.jsonl")
	writeFile(t, usage, `{"route":"`+filepath.Base(costOnly)+`","cost_micros":7}`+"\n")
	tr, err = Collect(Sources{Route: costOnly, UsageFile: usage})
	if err != nil {
		t.Fatal(err)
	}
	if root := tr.Spans[0]; root.Tokens != nil || root.CostMicros == nil || *root.CostMicros != 7 {
		t.Fatalf("usage root %+v", root)
	}
}

func TestAppendRefusesBadSpans(t *testing.T) {
	route := t.TempDir()
	good := Span{Kind: KindCheck, Name: "n", Start: time.Now(), DurationMS: 1}
	bad := map[string]func(*Span){
		"kind":     func(s *Span) { s.Kind = "other" },
		"root":     func(s *Span) { s.Kind = KindRoute },
		"name":     func(s *Span) { s.Name = "" },
		"duration": func(s *Span) { s.DurationMS = -1 },
		"start":    func(s *Span) { s.Start = time.Time{} },
	}
	for name, change := range bad {
		s := good
		change(&s)
		if err := Append(route, s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := Append(filepath.Join(route, "gone"), good); err == nil {
		t.Error("a missing route must not be created")
	}
	blocked := t.TempDir()
	writeFile(t, filepath.Join(blocked, "trace"), "a file where the folder goes")
	if err := Append(blocked, good); err == nil {
		t.Error("an unwritable trace folder was accepted")
	}
	folder := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, "trace/spans.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Append(folder, good); err == nil {
		t.Error("an unopenable log was accepted")
	}
}

func TestAppendFailsOnARouteThatIsAFileOrASpanThatCannotEncode(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	writeFile(t, file, "x")
	good := Span{Kind: KindCheck, Name: "n", Start: time.Now(), DurationMS: 1}
	if err := Append(file, good); err == nil {
		t.Error("a file was accepted as a route")
	}
	good.Start = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := Append(t.TempDir(), good); err == nil {
		t.Error("a start JSON cannot hold was accepted")
	}
}

func TestUnreadableFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	route := t.TempDir()
	commands := filepath.Join(route, "logs/commands.jsonl")
	writeFile(t, commands, "")
	if err := os.Chmod(commands, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(Sources{Route: route}); err == nil {
		t.Error("an unreadable command log was accepted")
	}
	hooked := t.TempDir()
	result := filepath.Join(hooked, "logs/hooks/t/h/20260101T000000.000000000/result.json")
	writeFile(t, result, "{}")
	if err := os.Chmod(result, 0); err != nil {
		t.Fatal(err)
	}
	tr, err := Collect(Sources{Route: hooked})
	if err != nil || tr.Skipped != 1 {
		t.Errorf("an unreadable hook result is one skipped record: %v %d", err, tr.Skipped)
	}
}

func TestEmptyRouteHasAnEmptyList(t *testing.T) {
	tr, err := Collect(Sources{Route: t.TempDir()})
	if err != nil || tr.Spans == nil || len(tr.Spans) != 0 {
		t.Fatalf("%v %+v", err, tr)
	}
}

type collector struct {
	mu       sync.Mutex
	requests []map[string]any
	paths    []string
	types    []string
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests, c.paths, c.types = append(c.requests, parsed), append(c.paths, r.URL.Path), append(c.types, r.Header.Get("Content-Type"))
	_, _ = w.Write([]byte(`{}`))
}

func (c *collector) spans() []map[string]any {
	var all []map[string]any
	for _, request := range c.requests {
		for _, rs := range request["resourceSpans"].([]any) {
			for _, ss := range rs.(map[string]any)["scopeSpans"].([]any) {
				for _, s := range ss.(map[string]any)["spans"].([]any) {
					all = append(all, s.(map[string]any))
				}
			}
		}
	}
	return all
}

func attribute(span map[string]any, key string) map[string]any {
	for _, a := range span["attributes"].([]any) {
		if a.(map[string]any)["key"] == key {
			return a.(map[string]any)["value"].(map[string]any)
		}
	}
	return nil
}

func TestCollectorFixtureReceivesTheSpans(t *testing.T) {
	sink := &collector{}
	server := httptest.NewServer(sink)
	defer server.Close()
	tr := collectFixture(t)
	if err := ExportOTLP(context.Background(), server.URL, tr); err != nil {
		t.Fatal(err)
	}
	if sink.paths[0] != "/v1/traces" || sink.types[0] != "application/json" {
		t.Fatalf("request %v %v", sink.paths, sink.types)
	}
	got := sink.spans()
	if len(got) != len(tr.Spans) {
		t.Fatalf("got %d spans, want %d", len(got), len(tr.Spans))
	}
	seen := map[string]map[string]any{}
	for _, s := range got {
		if s["traceId"] != tr.TraceID || len(s["spanId"].(string)) != 16 {
			t.Errorf("ids %v", s)
		}
		seen[s["name"].(string)] = s
	}
	turn := seen["implementer"]
	if attribute(turn, "herdsman.kind")["stringValue"] != KindTurn || attribute(turn, "herdsman.tokens")["intValue"] != "900" {
		t.Errorf("turn %v", turn)
	}
	begin := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	if turn["startTimeUnixNano"] != strconv.FormatInt(begin.UnixNano(), 10) || turn["endTimeUnixNano"] != strconv.FormatInt(begin.Add(5*time.Second).UnixNano(), 10) {
		t.Errorf("times %v %v", turn["startTimeUnixNano"], turn["endTimeUnixNano"])
	}
	if code := seen["git status"]["status"].(map[string]any)["code"]; code != float64(statusError) {
		t.Errorf("a failed command has status %v", code)
	}
	if code := seen["git fetch"]["status"].(map[string]any)["code"]; code != float64(statusOK) {
		t.Errorf("a passed command has status %v", code)
	}
	if status := seen["reviewer"]["status"].(map[string]any); len(status) != 0 {
		t.Errorf("a span without an exit has status %v", status)
	}
	resource := sink.requests[0]["resourceSpans"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	if attribute(resource, "service.name")["stringValue"] != "herdsman" || attribute(resource, "herdsman.route")["stringValue"] != "fixture-route" {
		t.Errorf("resource %v", resource)
	}
}

func TestOTLPBatchesLargeTraces(t *testing.T) {
	sink := &collector{}
	server := httptest.NewServer(sink)
	defer server.Close()
	tr := Trace{Route: "r", TraceID: strings.Repeat("a", 32)}
	for range otlpBatch + 1 {
		tr.Spans = append(tr.Spans, Span{Kind: KindTurn, Name: "n", SpanID: strings.Repeat("b", 16), Start: time.Unix(1, 0)})
	}
	if err := ExportOTLP(context.Background(), server.URL+"/custom/v1/traces", tr); err != nil {
		t.Fatal(err)
	}
	if len(sink.requests) != 2 || len(sink.spans()) != otlpBatch+1 || sink.paths[0] != "/custom/v1/traces" {
		t.Fatalf("requests %d spans %d path %v", len(sink.requests), len(sink.spans()), sink.paths)
	}
}

func TestOTLPSendsNothingForAnEmptyTrace(t *testing.T) {
	sink := &collector{}
	server := httptest.NewServer(sink)
	defer server.Close()
	if err := ExportOTLP(context.Background(), server.URL, Trace{}); err != nil || len(sink.requests) != 0 {
		t.Fatalf("%v, %d requests", err, len(sink.requests))
	}
}

func TestOTLPFailures(t *testing.T) {
	tr := Trace{Route: "r", TraceID: "t", Spans: []Span{{Kind: KindTurn, Name: "n", SpanID: "s", Start: time.Unix(1, 0)}}}
	respond := func(status int, body string) string {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(server.Close)
		return server.URL
	}
	cases := map[string]struct{ endpoint, want string }{
		"status":   {respond(500, "boom"), "500"},
		"rejected": {respond(200, `{"partialSuccess":{"rejectedSpans":"1","errorMessage":"full"}}`), "rejected 1"},
		"bare":     {respond(200, `{"partialSuccess":{"rejectedSpans":2}}`), "rejected 2"},
	}
	for name, c := range cases {
		if err := ExportOTLP(context.Background(), c.endpoint, tr); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var followed atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer elsewhere.Close()
	redirecting := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusTemporaryRedirect))
	defer redirecting.Close()
	if err := ExportOTLP(context.Background(), redirecting.URL, tr); err == nil || !strings.Contains(err.Error(), "307") || followed.Load() {
		t.Errorf("a redirect must fail and not be followed: %v, followed %v", err, followed.Load())
	}
	truncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer truncated.Close()
	if err := ExportOTLP(context.Background(), truncated.URL, tr); err == nil {
		t.Error("a cut-off reply was accepted")
	}
	if err := ExportOTLP(context.Background(), respond(200, "not json"), tr); err != nil {
		t.Errorf("a success without a JSON body is a success: %v", err)
	}
	if err := ExportOTLP(context.Background(), "ftp://x", tr); err == nil {
		t.Error("a bad endpoint was accepted")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if err := ExportOTLP(context.Background(), closed.URL, tr); err == nil {
		t.Error("an unreachable collector was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ExportOTLP(ctx, respond(200, ""), tr); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestOTLPEndpointRules(t *testing.T) {
	for _, bad := range []string{"", "ftp://x", "http://example.com:4318", "https://", "https://u:p@example.com", "::"} {
		if _, err := otlpURL(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	good := map[string]string{
		"http://localhost:4318":              "http://localhost:4318/v1/traces",
		"http://127.0.0.1:4318/":             "http://127.0.0.1:4318/v1/traces",
		"http://[::1]:4318/base":             "http://[::1]:4318/base/v1/traces",
		"https://otel.example.com/v1/traces": "https://otel.example.com/v1/traces",
	}
	for in, want := range good {
		if got, err := otlpURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestMeanIntervalUsesStudentT(t *testing.T) {
	got, ok := meanInterval([]float64{10, 12, 14, 16})
	if !ok || !near(got.Low, 8.89) || !near(got.High, 17.11) {
		t.Fatalf("%+v %v", got, ok)
	}
	if _, ok := meanInterval([]float64{1}); ok {
		t.Fatal("one sample has no interval")
	}
}

func TestStudentTNeverNarrows(t *testing.T) {
	want := map[int]float64{1: 12.706, 30: 2.042, 39: 2.042, 40: 2.0211, 60: 2.0003, 120: 1.980}
	for df, value := range want {
		if studentT(df) != value {
			t.Errorf("df %d: %v", df, studentT(df))
		}
	}
}

func TestDifferenceOfMeans(t *testing.T) {
	got, ok := meanDifference([]float64{10, 12, 14, 16}, []float64{10, 12, 14, 16})
	if !ok || got.Low >= 0 || got.High <= 0 || !near(got.Low, -got.High) {
		t.Fatalf("identical samples straddle zero: %+v %v", got, ok)
	}
	far, _ := meanDifference([]float64{100, 101, 102}, []float64{1, 2, 3})
	if far.Low <= 90 {
		t.Fatalf("%+v", far)
	}
	flat, ok := meanDifference([]float64{5, 5}, []float64{3, 3})
	if !ok || flat.Low != 2 || flat.High != 2 {
		t.Fatalf("no variance: %+v %v", flat, ok)
	}
	if _, ok := meanDifference([]float64{1}, []float64{1, 2}); ok {
		t.Fatal("a side with one sample has no interval")
	}
}

func TestWilsonInterval(t *testing.T) {
	got, ok := wilson(8, 10)
	if !ok || !near(got.Low, 0.49) || !near(got.High, 0.94) {
		t.Fatalf("%+v", got)
	}
	none, _ := wilson(0, 10)
	all, _ := wilson(10, 10)
	if none.Low != 0 || all.High != 1 {
		t.Fatalf("bounds %+v %+v", none, all)
	}
	if _, ok := wilson(0, 0); ok {
		t.Fatal("no runs has no interval")
	}
}

func TestDifferenceOfRates(t *testing.T) {
	same, ok := rateDifference(5, 10, 5, 10)
	if !ok || same.Low >= 0 || same.High <= 0 {
		t.Fatalf("%+v", same)
	}
	apart, _ := rateDifference(10, 10, 0, 10)
	if apart.Low <= 0.5 || apart.High > 1 {
		t.Fatalf("%+v", apart)
	}
	if _, ok := rateDifference(0, 0, 1, 2); ok {
		t.Fatal("a side with no runs has no interval")
	}
}

func suiteOf(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, RunsFile), strings.Join(lines, "\n")+"\n")
	return dir
}

func run(arm, control string, pass bool, ms int, cost string) string {
	line := `{"schema":"0.1.0","task":"t","arm":"` + arm + `","pass":` + strconv.FormatBool(pass) + `,"duration_ms":` + strconv.Itoa(ms)
	if control != "" {
		line += `,"control":"` + control + `"`
	}
	if cost != "" {
		line += `,"cost_micros":` + cost
	}
	return line + "}"
}

func controlled() []string {
	return []string{
		run("opus", "", true, 10, "100"), run("opus", "", false, 12, "110"), run("opus", "", true, 14, ""),
		run("sol", "", true, 20, ""), run("sol", "", true, 22, ""),
		run("opus", "a", true, 10, "1"), run("opus", "a", true, 11, "2"),
		run("opus", "b", true, 10, "1"), run("opus", "b", false, 11, "2"),
		run("sol", "a", true, 20, ""), run("sol", "a", true, 21, ""),
		run("sol", "b", true, 20, ""), run("sol", "b", true, 21, ""),
	}
}

func TestEvalReportsCountsAndIntervalsWithControl(t *testing.T) {
	runs, skipped, err := LoadRuns(suiteOf(t, append(controlled(), "garbage")...))
	if err != nil || skipped != 1 {
		t.Fatalf("%v, skipped %d", err, skipped)
	}
	report, err := Evaluate("s", runs, skipped)
	if err != nil {
		t.Fatal(err)
	}
	if report.Experimental != "eval/"+EvalVersion || report.Skipped != 1 {
		t.Fatalf("%+v", report)
	}
	if len(report.Arms) != 2 || report.Arms[0].Arm != "opus" || report.Arms[0].Runs != 3 || report.Arms[0].Passed != 2 {
		t.Fatalf("control runs must stay out of the arms: %+v", report.Arms)
	}
	opus := report.Arms[0]
	if opus.PassRate == nil || opus.DurationMS == nil || opus.CostMicros == nil || opus.CostRuns != 2 {
		t.Fatalf("%+v", opus)
	}
	if report.Arms[1].CostMicros != nil || report.Arms[1].CostRuns != 0 {
		t.Fatalf("a harness that reports no cost has no cost interval: %+v", report.Arms[1])
	}
	if len(report.Controls) != 2 {
		t.Fatalf("%+v", report.Controls)
	}
	aa := report.Controls[0]
	if aa.Arm != "opus" || aa.RunsA != 2 || aa.RunsB != 2 || aa.PassRateDifference == nil || aa.DurationMSDifference == nil || aa.CostMicrosDifference == nil {
		t.Fatalf("%+v", aa)
	}
	data, _ := json.Marshal(report)
	for _, banned := range []string{"median", "mean", "percentile", "p50"} {
		if strings.Contains(strings.ToLower(string(data)), banned) {
			t.Errorf("the report states a point value: %q in %s", banned, data)
		}
	}
}

func TestEvalNeedsAControl(t *testing.T) {
	cases := map[string][]string{
		"none":                  {run("a", "", true, 1, ""), run("a", "", true, 2, "")},
		"one side":              {run("a", "", true, 1, ""), run("a", "a", true, 1, ""), run("a", "a", true, 2, "")},
		"too few":               {run("a", "", true, 1, ""), run("a", "a", true, 1, ""), run("a", "b", true, 2, "")},
		"another arm's control": {run("treated", "", true, 1, ""), run("treated", "", true, 2, ""), run("other", "", true, 1, ""), run("other", "a", true, 1, ""), run("other", "a", true, 2, ""), run("other", "b", true, 1, ""), run("other", "b", true, 2, "")},
		"no arm":                {run("a", "a", true, 1, ""), run("a", "a", true, 2, ""), run("a", "b", true, 1, ""), run("a", "b", true, 2, "")},
	}
	for name, lines := range cases {
		runs, _, err := LoadRuns(suiteOf(t, lines...))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Evaluate("s", runs, 0); !errors.Is(err, ErrNoControl) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLoadRunsRejectsBadRecords(t *testing.T) {
	bad := []string{
		`{"arm":"a","pass":true,"duration_ms":1}`,
		`{"task":"t","pass":true,"duration_ms":1}`,
		`{"task":"t","arm":"a","duration_ms":1}`,
		`{"task":"t","arm":"a","pass":true}`,
		`{"task":"t","arm":"a","pass":true,"duration_ms":-1}`,
		`{"task":"t","arm":"a","pass":true,"duration_ms":1,"cost_micros":-1}`,
		`{"task":"t","arm":"a","pass":true,"duration_ms":1,"control":"c"}`,
	}
	_, skipped, err := LoadRuns(suiteOf(t, bad...))
	if err != nil || skipped != len(bad) {
		t.Fatalf("%v, skipped %d of %d", err, skipped, len(bad))
	}
	if _, _, err := LoadRuns(t.TempDir()); err == nil {
		t.Fatal("a suite without runs was accepted")
	}
	folder := t.TempDir()
	if err := os.Mkdir(filepath.Join(folder, RunsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadRuns(folder); err == nil {
		t.Fatal("an unreadable runs file was accepted")
	}
}

func at(minute int) time.Time { return time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC) }

func TestFindingsKeepTheirLatestOutcome(t *testing.T) {
	route := t.TempDir()
	finding := Finding{Reviewer: "sol", Severity: "P1", Task: "t", Head: "abc", File: "a.go", Line: 3, Title: "bug"}
	id, err := AddFinding(route, finding, at(0))
	if err != nil {
		t.Fatal(err)
	}
	again, err := AddFinding(route, finding, at(1))
	if err != nil || again != id {
		t.Fatalf("recording twice must be idempotent: %q %q %v", id, again, err)
	}
	if err := SetOutcome(route, id, OutcomeAccepted, at(2)); err != nil {
		t.Fatal(err)
	}
	if err := SetOutcome(route, id, OutcomeFixed, at(3)); err != nil {
		t.Fatal(err)
	}
	if _, err := AddFinding(route, Finding{ID: "mine", Reviewer: "opus", Severity: "P0", Title: "open"}, at(4)); err != nil {
		t.Fatal(err)
	}
	found, skipped, err := LoadFindings(route)
	if err != nil || skipped != 0 || len(found) != 2 {
		t.Fatalf("%v %d %+v", err, skipped, found)
	}
	if found[0].ID != id || found[0].Outcome != OutcomeFixed || !found[0].Time.Equal(at(0)) || found[1].Outcome != "" || found[1].ID != "mine" {
		t.Fatalf("%+v", found)
	}
	counts := CountFindings(found)
	if counts.Recorded != 2 || counts.Labeled != 1 || counts.Open != 1 || len(counts.Reviewers) != 2 {
		t.Fatalf("%+v", counts)
	}
	if counts.Reviewers[0].Reviewer != "opus" || counts.Reviewers[1].Fixed != 1 {
		t.Fatalf("listed by name, nothing ranked: %+v", counts.Reviewers)
	}
}

func TestFindingFailures(t *testing.T) {
	route := t.TempDir()
	for name, f := range map[string]Finding{
		"reviewer": {Severity: "P1", Title: "x"},
		"severity": {Reviewer: "r", Title: "x"},
		"title":    {Reviewer: "r", Severity: "P1"},
	} {
		if _, err := AddFinding(route, f, at(0)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	valid := Finding{Reviewer: "r", Severity: "P", Title: "x"}
	if _, err := AddFinding(filepath.Join(route, "gone"), valid, at(0)); err == nil {
		t.Error("a missing route was created")
	}
	if err := SetOutcome(route, "nope", OutcomeFixed, at(0)); err == nil {
		t.Error("an outcome for an unknown finding was accepted")
	}
	id, err := AddFinding(route, valid, at(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := SetOutcome(route, id, "maybe", at(0)); err == nil {
		t.Error("an unknown outcome was accepted")
	}
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, "trace", FindingsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := AddFinding(broken, valid, at(0)); err == nil {
		t.Error("an unreadable log was accepted")
	}
	if err := SetOutcome(broken, "x", OutcomeFixed, at(0)); err == nil {
		t.Error("an unreadable log was accepted")
	}
	if _, _, err := LoadFindings(broken); err == nil {
		t.Error("an unreadable log was accepted")
	}
	mixed := t.TempDir()
	writeFile(t, filepath.Join(mixed, "trace", FindingsFile), strings.Join([]string{
		`garbage`, `{"type":"other","id":"a"}`, `{"type":"finding","id":""}`,
		`{"type":"outcome","id":"unknown","outcome":"fixed"}`, `{"type":"outcome","id":"b","outcome":"maybe"}`,
		`{"type":"finding","id":"b","reviewer":"r","severity":"P","title":"x"}`,
	}, "\n")+"\n")
	found, skipped, err := LoadFindings(mixed)
	if err != nil || skipped != 5 || len(found) != 1 {
		t.Fatalf("%v %d %+v", err, skipped, found)
	}
}

func labeled(perReviewer int, reviewers ...string) FindingCounts {
	var found []Finding
	for _, r := range reviewers {
		for i := range perReviewer {
			outcome := OutcomeFixed
			if i%2 == 0 {
				outcome = OutcomeDismissed
			}
			found = append(found, Finding{Reviewer: r, Outcome: outcome})
		}
	}
	return CountFindings(found)
}

func TestRankingIsRefusedWithoutALabeledSet(t *testing.T) {
	for name, counts := range map[string]FindingCounts{
		"nothing":      {},
		"one reviewer": labeled(MinLabeledPerReviewer, "a"),
		"too few":      labeled(MinLabeledPerReviewer-1, "a", "b"),
	} {
		decision := DecideRank(counts)
		if !decision.Refused || decision.LabeledSet || !strings.Contains(decision.Reason, "no labeled set") {
			t.Errorf("%s: %+v", name, decision)
		}
	}
}

func TestNothingIsRankedEvenWithALabeledSet(t *testing.T) {
	decision := DecideRank(labeled(MinLabeledPerReviewer, "a", "b"))
	if !decision.Refused || !decision.LabeledSet || strings.Contains(decision.Reason, "no labeled set") {
		t.Fatalf("%+v", decision)
	}
}

func TestMixedOutcomesAreCounted(t *testing.T) {
	counts := CountFindings([]Finding{{Reviewer: "r", Outcome: OutcomeAccepted}, {Reviewer: "r", Outcome: OutcomeDismissed}, {Reviewer: "r"}})
	r := counts.Reviewers[0]
	if r.Accepted != 1 || r.Dismissed != 1 || r.Open != 1 || r.Recorded != 3 {
		t.Fatalf("%+v", r)
	}
}
