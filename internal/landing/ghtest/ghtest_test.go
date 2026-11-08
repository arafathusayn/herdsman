package ghtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	Main()
	os.Exit(m.Run())
}

func gh(t *testing.T, f *Fake, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(f.Bin(), args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	return out.String(), errb.String(), cmd.ProcessState.ExitCode()
}

func body(t *testing.T, stdout string) string {
	t.Helper()
	_, b, ok := strings.Cut(stdout, "\r\n\r\n")
	if !ok {
		t.Fatalf("no body in %q", stdout)
	}
	return b
}

func newFake(t *testing.T) *Fake {
	f := New(t, "o/r")
	f.AddPR(PR{Number: 1, HeadRef: "h", BaseRef: "main", HeadSHA: "aa"})
	return f
}

func TestMainReturnsWithoutTheAddress(t *testing.T) {
	t.Setenv(envAddr, "")
	Main()
}

func TestMainExitsWithTheRelayCode(t *testing.T) {
	var code int
	defer func(old func(int)) { exit = old }(exit)
	exit = func(c int) { code = c }
	t.Setenv(envAddr, "127.0.0.1:1")
	Main()
	if code != 1 {
		t.Errorf("exit = %d", code)
	}
}

func TestUnsupportedArguments(t *testing.T) {
	f := newFake(t)
	for _, args := range [][]string{{"pr", "view", "1"}, {"api", "--jq", ".x", "repos/o/r/pulls/1"}, {"api"}, {"api", "a", "b"}, {"api", "%zz"}, {"api", "-X"}, {}} {
		_, stderr, code := gh(t, f, "", args...)
		if code != 2 || !strings.Contains(stderr, "unsupported") {
			t.Errorf("%v: code %d, %q", args, code, stderr)
		}
	}
}

func TestAPIFlagForms(t *testing.T) {
	f := newFake(t)
	for _, args := range [][]string{{"api", "-i", "repos/o/r/pulls/1"}, {"api", "--include", "--input", "-", "-X", "GET", "repos/o/r/pulls/1"}} {
		if out, _, code := gh(t, f, "", args...); code != 0 || !strings.Contains(out, `"number":1`) {
			t.Errorf("%v: %d %q", args, code, out)
		}
	}
}

func TestAuthToken(t *testing.T) {
	f := newFake(t)
	if out, _, code := gh(t, f, "", "auth", "token"); code != 0 || out != "fake-token\n" {
		t.Errorf("%d %q", code, out)
	}
	f.Configure(func() { f.Token = "" })
	if _, _, code := gh(t, f, "", "auth", "token"); code != 1 {
		t.Errorf("exit = %d", code)
	}
}

func TestNotFoundRoutes(t *testing.T) {
	f := newFake(t)
	for _, path := range []string{"repos/x/y/pulls/1", "repos", "repos/o/r/nothing", "repos/o/r/pulls/9", "repos/o/r/pulls/abc", "repos/o/r/pulls/1/merge-async/none"} {
		if out, _, code := gh(t, f, "", "api", "-i", path); code != 1 || !strings.Contains(out, " 404 ") {
			t.Errorf("%s: %d %q", path, code, out)
		}
	}
	for _, path := range []string{"repos/o/r/pulls/9/merge-async", "repos/o/r/pulls/abc/merge-async"} {
		if out, _, code := gh(t, f, "{}", "api", "-i", "-X", "PUT", "--input", "-", path); code != 1 || !strings.Contains(out, " 404 ") {
			t.Errorf("%s: %d %q", path, code, out)
		}
	}
}

func TestUnescapableSegmentStaysAsIs(t *testing.T) {
	f := newFake(t)
	call := Call{Method: "GET", Path: "repos/o/r/%zz"}
	f.mu.Lock()
	res := f.respond(call)
	f.mu.Unlock()
	if !strings.Contains(res.stdout, "%zz") {
		t.Errorf("%q", res.stdout)
	}
}

func TestPagingDefaults(t *testing.T) {
	items := make([]map[string]any, 35)
	for i := range items {
		items[i] = map[string]any{}
	}
	if got := len(page(items, nil)); got != 30 {
		t.Errorf("first page = %d", got)
	}
}

func TestCreateValidation(t *testing.T) {
	f := newFake(t)
	if out, _, _ := gh(t, f, "nope", "api", "-i", "-X", "POST", "--input", "-", "repos/o/r/pulls"); !strings.Contains(out, " 422 ") {
		t.Errorf("%q", out)
	}
	dup := `{"title":"t","head":"h","base":"main"}`
	if out, _, _ := gh(t, f, dup, "api", "-i", "-X", "POST", "--input", "-", "repos/o/r/pulls"); !strings.Contains(out, "already exists") {
		t.Errorf("%q", out)
	}
}

func TestListSkipsOtherPullRequests(t *testing.T) {
	f := newFake(t)
	f.AddPR(PR{Number: 2, HeadRef: "other", BaseRef: "main", HeadSHA: "bb"})
	out, _, _ := gh(t, f, "", "api", "-i", "repos/o/r/pulls?state=open&head=o:other&base=main")
	var list []map[string]any
	if err := json.Unmarshal([]byte(body(t, out)), &list); err != nil || len(list) != 1 || list[0]["number"].(float64) != 2 {
		t.Errorf("%v %v", list, err)
	}
}

func merge(t *testing.T, f *Fake, payload string) string {
	t.Helper()
	out, _, _ := gh(t, f, payload, "api", "-i", "-X", "PUT", "--input", "-", "repos/o/r/pulls/1/merge-async")
	return out
}

func TestMergeRequestStates(t *testing.T) {
	f := newFake(t)
	if out := merge(t, f, "nope"); !strings.Contains(out, " 422 ") {
		t.Errorf("bad payload: %q", out)
	}
	f.AddPR(PR{Number: 1, HeadRef: "h", BaseRef: "main", HeadSHA: "aa", Draft: true})
	if out := merge(t, f, `{"sha":"aa"}`); !strings.Contains(out, " 400 ") {
		t.Errorf("draft: %q", out)
	}
	f.AddPR(PR{Number: 1, HeadRef: "h", BaseRef: "main", HeadSHA: "aa"})
	f.MergeNow(1)
	if out := merge(t, f, `{"sha":"aa"}`); !strings.Contains(out, " 200 ") || !strings.Contains(out, `"merged"`) {
		t.Errorf("merged: %q", out)
	}
}

func TestAcceptedRequestLifecycle(t *testing.T) {
	f := newFake(t)
	f.Configure(func() { f.Ticks = 1 })
	out := merge(t, f, `{"sha":"aa","merge_method":"squash","merge_action":"direct_merge"}`)
	if !strings.Contains(out, " 202 ") {
		t.Fatalf("%q", out)
	}
	if again := merge(t, f, `{"sha":"aa"}`); !strings.Contains(again, " 409 ") {
		t.Errorf("second request: %q", again)
	}
	var accepted struct{ Details struct{ UUID string } }
	if err := json.Unmarshal([]byte(body(t, out)), &accepted); err != nil {
		t.Fatal(err)
	}
	path := "repos/o/r/pulls/1/merge-async/" + accepted.Details.UUID
	if first, _, _ := gh(t, f, "", "api", "-i", path); !strings.Contains(first, `"pending"`) {
		t.Errorf("first read: %q", first)
	}
	if second, _, _ := gh(t, f, "", "api", "-i", path); !strings.Contains(second, `"merged"`) || !strings.Contains(second, `"sha"`) {
		t.Errorf("second read: %q", second)
	}
	if f.Merges(1) != 1 {
		t.Errorf("merges = %d", f.Merges(1))
	}
	gh(t, f, "", "api", "-i", "repos/o/r/pulls/1")
	if f.Merges(1) != 1 {
		t.Errorf("merges after more reads = %d", f.Merges(1))
	}
}

func TestPendingRequestEndsFailed(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*Fake)
		want    string
	}{
		{"head moved", func(f *Fake) { f.PushHead(1, "bb") }, "head commit changed"},
		{"closed", func(f *Fake) { f.Close(1) }, "pull request is closed"},
		{"configured", func(f *Fake) { f.Configure(func() { f.FailMerge = "nope" }) }, "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.Configure(func() { f.Hold = true })
			merge(t, f, `{"sha":"aa"}`)
			tt.prepare(f)
			f.Release()
			out, _, _ := gh(t, f, "", "api", "-i", "repos/o/r/pulls/1/merge-async/uuid-101")
			if !strings.Contains(out, `"failed"`) || !strings.Contains(out, tt.want) {
				t.Errorf("%q", out)
			}
		})
	}
}

func TestFaultsMatchMethodPageAndRepeat(t *testing.T) {
	f := newFake(t)
	f.Inject(Fault{Method: "POST", Path: "pulls/1", Status: 500})
	f.Inject(Fault{Path: "pulls/1", Page: 2, Status: 501})
	f.Inject(Fault{Path: "pulls/1", Always: true, Status: 503, Body: "{}"})
	for range 2 {
		if out, _, _ := gh(t, f, "", "api", "-i", "repos/o/r/pulls/1"); !strings.Contains(out, " 503 ") {
			t.Errorf("%q", out)
		}
	}
	if out, _, _ := gh(t, f, "", "api", "-i", "repos/o/r/pulls/1?page=2"); !strings.Contains(out, " 501 ") {
		t.Errorf("%q", out)
	}
}

func TestLostFaultAppliesNothingOrEverything(t *testing.T) {
	f := newFake(t)
	f.Inject(Fault{Method: "PUT", Path: "merge-async", Lost: true})
	if out := merge(t, f, `{"sha":"aa"}`); out != "" {
		t.Errorf("%q", out)
	}
	if f.Count("PUT", "merge-async") != 1 {
		t.Errorf("count = %d", f.Count("PUT", "merge-async"))
	}
	if f.Count("GET", "merge-async") != 0 {
		t.Error("counted a method that never ran")
	}
}

func TestHangStallsUntilKilled(t *testing.T) {
	f := newFake(t)
	f.Inject(Fault{Path: "pulls/1", Apply: true, Hang: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, f.Bin(), "api", "-i", "repos/o/r/pulls/1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	n, err := stdout.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "HTTP/2.0 200") {
		t.Fatalf("%d %v %q", n, err, buf[:n])
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Error("process finished by itself")
	}
}

func TestCleanupReleasesAHang(t *testing.T) {
	var cmd *exec.Cmd
	t.Run("fake", func(t *testing.T) {
		f := newFake(t)
		f.Inject(Fault{Path: "pulls/1", Apply: true, Hang: true})
		cmd = exec.Command(f.Bin(), "api", "-i", "repos/o/r/pulls/1")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := stdout.Read(make([]byte, 16)); err != nil {
			t.Fatal(err)
		}
	})
	if err := cmd.Wait(); err == nil {
		t.Error("client finished cleanly on a dropped connection")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken stdin") }

func TestRelayFailures(t *testing.T) {
	var out, errb bytes.Buffer
	if code := relay("127.0.0.1:1", nil, "", strings.NewReader(""), &out, &errb); code != 1 {
		t.Errorf("dial: %d", code)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	if code := relay(listener.Addr().String(), nil, "", failingReader{}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "broken stdin") {
		t.Errorf("stdin: %d %q", code, errb.String())
	}
	if code := relay(listener.Addr().String(), nil, "", strings.NewReader(""), &out, &errb); code != 1 {
		t.Errorf("closed: %d", code)
	}
}

func TestServeDropsAnUnreadableRequest(t *testing.T) {
	f := newFake(t)
	client, server := net.Pipe()
	go func() {
		client.Write([]byte("nope\n"))
		client.Close()
	}()
	f.serve(server)
	if len(f.Calls()) != 0 {
		t.Errorf("calls = %v", f.Calls())
	}
}
