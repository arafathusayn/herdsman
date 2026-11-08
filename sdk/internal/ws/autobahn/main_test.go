package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/sdk/internal/ws"
)

type result struct {
	Behavior      string `json:"behavior"`
	BehaviorClose string `json:"behaviorClose"`
}

func writeReport(t *testing.T, cases map[string]result) string {
	t.Helper()
	return writeAgentReport(t, "herdsman", cases)
}

func passingCases(n int) map[string]result {
	cases := make(map[string]result, n)
	for i := range n {
		cases[fmt.Sprintf("1.1.%d", i)] = result{"OK", "OK"}
	}
	return cases
}

func writeAgentReport(t *testing.T, agent string, cases map[string]result) string {
	t.Helper()
	data, err := json.Marshal(map[string]map[string]result{agent: cases})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVerifyPassesAcceptableOutcomes(t *testing.T) {
	path := writeReport(t, map[string]result{
		"1.1.1": {"OK", "OK"},
		"6.4.3": {"NON-STRICT", "OK"},
		"7.1.6": {"INFORMATIONAL", "INFORMATIONAL"},
	})
	var out bytes.Buffer
	if err := run(context.Background(), []string{"verify", "-cases", "3", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "3 cases passed") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestVerifyFailsOnABadCase(t *testing.T) {
	for name, r := range map[string]result{
		"behavior":      {"FAILED", "OK"},
		"close":         {"OK", "FAILED"},
		"unimplemented": {"UNIMPLEMENTED", "OK"},
		"wrong code":    {"WRONG CODE", "OK"},
		"unclean close": {"OK", "UNCLEAN"},
		"not a verdict": {"NON-STRATEGIC", "OK"},
		"non-strict close is not a close verdict": {"OK", "NON-STRICT"},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeReport(t, map[string]result{"2.1": r})
			err := run(context.Background(), []string{"verify", "-cases", "1", path}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "2.1") {
				t.Fatalf("verify = %v, want a failure naming the case", err)
			}
		})
	}
}

func TestVerifyDemandsEverySelectedCase(t *testing.T) {
	if err := run(context.Background(), []string{"verify", writeReport(t, passingCases(301))}, io.Discard); err != nil {
		t.Fatalf("the full selection must pass: %v", err)
	}
	// The suite writes a partial report and stops normally when a connection
	// fails, so a short report is a real outcome, and must not read as a pass.
	for _, n := range []int{0, 250, 300, 302} {
		if err := run(context.Background(), []string{"verify", writeReport(t, passingCases(n))}, io.Discard); err == nil {
			t.Fatalf("%d cases must not satisfy a selection of 301", n)
		}
	}
}

func TestVerifyJudgesOnlyTheIntendedAgent(t *testing.T) {
	other := writeAgentReport(t, "someone-else", passingCases(301))
	err := run(context.Background(), []string{"verify", other}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "herdsman") {
		t.Fatalf("verify = %v, want a failure naming the missing agent", err)
	}
	if err := run(context.Background(), []string{"verify", "-agent", "someone-else", other}, io.Discard); err != nil {
		t.Fatalf("verify -agent = %v", err)
	}
}

func TestVerifyBadInput(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"verify"},
		{"verify", "-nope"},
		{"verify", filepath.Join(t.TempDir(), "missing.json")},
		{"verify", bad},
	} {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("run(%v) must fail", args)
		}
	}
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"nope"},
		{"server", "-nope"},
		{"client", "-nope"},
		{"client"},
	} {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("run(%v) must fail", args)
		}
	}
}

func TestServerEchoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"server", "-addr", "127.0.0.1:0"}, pw) }()

	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(strings.TrimPrefix(line, "listening "))

	c, err := ws.Dial(ctx, "ws://"+addr+"/", ws.ClientOptions{Header: http.Header{"Origin": {"https://other.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(ws.OpBinary, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := c.ReadMessage(); err != nil || string(got) != "ping" {
		t.Fatalf("echo = %q %v", got, err)
	}
	c.Close(ws.CloseNormal, "")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("server = %v", err)
	}
}

func TestServerRefusesPlainHTTP(t *testing.T) {
	w := httptest.NewRecorder()
	handleEcho(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestServerBadAddress(t *testing.T) {
	if err := run(context.Background(), []string{"server", "-addr", "256.0.0.1:bad"}, io.Discard); err == nil {
		t.Fatal("a bad address must fail")
	}
}

// fuzzingServer stands in for the suite's server mode.
type fuzzingServer struct {
	mu      sync.Mutex
	echoed  []string
	updated string
	count   string
}

func (s *fuzzingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := ws.Upgrade(w, r, ws.ServerOptions{})
	if err != nil {
		return
	}
	defer c.Close(ws.CloseNormal, "")
	switch r.URL.Path {
	case "/getCaseCount":
		c.WriteMessage(ws.OpText, []byte(s.count))
	case "/runCase":
		id := r.URL.Query().Get("case")
		c.WriteMessage(ws.OpText, []byte("case "+id))
		if _, got, err := c.ReadMessage(); err == nil {
			s.mu.Lock()
			s.echoed = append(s.echoed, string(got))
			s.mu.Unlock()
		}
	case "/updateReports":
		s.mu.Lock()
		s.updated = r.URL.Query().Get("agent")
		s.mu.Unlock()
	}
}

func TestClientRunsEveryCaseThenUpdatesReports(t *testing.T) {
	fs := &fuzzingServer{count: "3"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	var out bytes.Buffer
	if err := run(context.Background(), []string{"client", "-url", url, "-agent", "herdsman"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fs.echoed, ","); got != "case 1,case 2,case 3" {
		t.Fatalf("echoed = %q", got)
	}
	if fs.updated != "herdsman" {
		t.Fatalf("updated agent = %q", fs.updated)
	}
	if !strings.Contains(out.String(), strconv.Itoa(3)) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestClientFailures(t *testing.T) {
	t.Run("server down", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := "ws" + strings.TrimPrefix(srv.URL, "http")
		srv.Close()
		if err := run(context.Background(), []string{"client", "-url", url}, io.Discard); err == nil {
			t.Fatal("want a dial failure")
		}
	})
	t.Run("count is not a number", func(t *testing.T) {
		srv := httptest.NewServer(&fuzzingServer{count: "many"})
		defer srv.Close()
		url := "ws" + strings.TrimPrefix(srv.URL, "http")
		if err := run(context.Background(), []string{"client", "-url", url}, io.Discard); err == nil {
			t.Fatal("want a parse failure")
		}
	})
	t.Run("no count frame", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := ws.Upgrade(w, r, ws.ServerOptions{})
			if err == nil {
				c.Close(ws.CloseNormal, "")
			}
		}))
		defer srv.Close()
		url := "ws" + strings.TrimPrefix(srv.URL, "http")
		if err := run(context.Background(), []string{"client", "-url", url}, io.Discard); err == nil {
			t.Fatal("want a read failure")
		}
	})
	t.Run("run case refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/getCaseCount" {
				http.Error(w, "no", http.StatusNotFound)
				return
			}
			(&fuzzingServer{count: "1"}).ServeHTTP(w, r)
		}))
		defer srv.Close()
		url := "ws" + strings.TrimPrefix(srv.URL, "http")
		if err := run(context.Background(), []string{"client", "-url", url}, io.Discard); err == nil {
			t.Fatal("want a dial failure on the case")
		}
	})
	t.Run("update refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/updateReports" {
				http.Error(w, "no", http.StatusNotFound)
				return
			}
			(&fuzzingServer{count: "0"}).ServeHTTP(w, r)
		}))
		defer srv.Close()
		url := "ws" + strings.TrimPrefix(srv.URL, "http")
		if err := run(context.Background(), []string{"client", "-url", url}, io.Discard); err == nil {
			t.Fatal("want a dial failure on the update")
		}
	})
}

func TestMainReportsFailureThroughExit(t *testing.T) {
	code := -1
	oldExit, oldArgs, oldStderr := exit, os.Args, stderr
	defer func() { exit, os.Args, stderr = oldExit, oldArgs, oldStderr }()
	var errOut bytes.Buffer
	exit, stderr = func(c int) { code = c }, &errOut

	os.Args = []string{"autobahn", "nope"}
	main()
	if code != 1 || !strings.Contains(errOut.String(), "nope") {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}

	code = -1
	path := writeReport(t, map[string]result{"1.1.1": {"OK", "OK"}})
	os.Args = []string{"autobahn", "verify", "-cases", "1", path}
	main()
	if code != -1 {
		t.Fatalf("exit called with %d on success", code)
	}
}

func TestEchoStopsWhenThePeerCloses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Upgrade(w, r, ws.ServerOptions{})
		if err != nil {
			return
		}
		c.WriteMessage(ws.OpText, []byte("go"))
		time.Sleep(20 * time.Millisecond)
		c.Close(ws.CloseNormal, "")
	}))
	defer srv.Close()
	c, err := ws.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), ws.ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		echo(c)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal(fmt.Errorf("echo did not stop"))
	}
}
