package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func runTraceArgs(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := runTrace(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func tracedRoute(t *testing.T) string {
	t.Helper()
	t.Setenv("HERDSMAN_STATE_DIR", t.TempDir())
	route := t.TempDir()
	code, _, errOut := runTraceArgs("add", route, "--kind", "check", "--name", "make check", "--task", "a", "--duration-ms", "12.5", "--exit", "1", "--tokens", "3", "--cost-micros", "4", "--start", "2026-01-01T00:00:00Z")
	if code != 0 {
		t.Fatalf("add: %d %s", code, errOut)
	}
	return route
}

func TestTraceJSON(t *testing.T) {
	route := tracedRoute(t)
	code, out, _ := runTraceArgs(route, "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var parsed struct {
		Schema struct{ Version string }
		Spans  []struct {
			Kind, Name string
			Exit       *int
			Tokens     *int64
		}
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Schema.Version != "1.0.0" || len(parsed.Spans) != 2 || parsed.Spans[1].Name != "make check" || *parsed.Spans[1].Exit != 1 || *parsed.Spans[1].Tokens != 3 {
		t.Fatalf("%+v", parsed)
	}
}

func TestTraceText(t *testing.T) {
	route := tracedRoute(t)
	code, out, _ := runTraceArgs(route)
	if code != 0 || !strings.Contains(out, "check") || !strings.Contains(out, "make check") || !strings.Contains(out, "12.5") {
		t.Fatalf("%d %q", code, out)
	}
	code, out, _ = runTraceArgs(t.TempDir())
	if code != 0 || !strings.Contains(out, "no spans") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestTraceOTLP(t *testing.T) {
	route := tracedRoute(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		hits.Add(1)
	}))
	defer server.Close()
	code, out, errOut := runTraceArgs(route, "--otlp", server.URL)
	if code != 0 || hits.Load() != 1 || !strings.Contains(out, "exported 2 spans") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	server.Close()
	if code, _, _ := runTraceArgs(route, "--otlp", server.URL); code != 1 {
		t.Fatalf("an unreachable collector exits %d", code)
	}
}

func TestTraceReportsSkippedLinesAndHookFolder(t *testing.T) {
	route := tracedRoute(t)
	writeTraceFile(t, filepath.Join(route, "logs", "commands.jsonl"), "garbage\n")
	hooksDir := t.TempDir()
	writeTraceFile(t, filepath.Join(hooksDir, "t/h/20260101T000000.000000000/result.json"), `{"hook":"h","task":"t","commands":[{"name":"n","exit":0,"duration_ms":1}]}`)
	code, out, errOut := runTraceArgs(route, "--hook-logs", hooksDir)
	if code != 0 || !strings.Contains(errOut, "skipped 1") || !strings.Contains(out, "h/n") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
}

func TestTraceUsageErrors(t *testing.T) {
	route := t.TempDir()
	for _, args := range [][]string{
		{},
		{"--bogus"},
		{route, "extra"},
		{route, "--json", "--otlp", "http://localhost:1"},
		{"add"},
		{"add", route, "--bogus"},
		{"add", route, "extra", "--kind", "check", "--name", "n"},
		{"add", route, "--kind", "check", "--name", "n", "--start", "yesterday"},
	} {
		if code, _, _ := runTraceArgs(args...); code != 2 {
			t.Errorf("%v exits %d", args, code)
		}
	}
	if code, _, _ := runTraceArgs(filepath.Join(route, "gone")); code != 1 {
		t.Error("a missing route must exit 1")
	}
	if code, _, _ := runTraceArgs("add", route, "--kind", "other", "--name", "n"); code != 1 {
		t.Error("an unknown kind must exit 1")
	}
}

func TestTraceAddLeavesUnsetFlagsOut(t *testing.T) {
	t.Setenv("HERDSMAN_STATE_DIR", t.TempDir())
	route := t.TempDir()
	if code, _, errOut := runTraceArgs("add", route, "--kind", "turn", "--name", "n"); code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	_, out, _ := runTraceArgs(route, "--json")
	if strings.Contains(out, `"exit"`) || strings.Contains(out, `"tokens"`) || strings.Contains(out, `"cost_micros"`) {
		t.Fatalf("flags left out must stay unset: %s", out)
	}
}

func TestTraceFailsWithoutAUsagePath(t *testing.T) {
	route := t.TempDir()
	t.Setenv("HERDSMAN_STATE_DIR", "")
	t.Setenv("HOME", "")
	if code, _, _ := runTraceArgs(route); code != 1 {
		t.Fatal("no usage path must exit 1")
	}
}
