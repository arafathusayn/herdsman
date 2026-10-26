package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/afk"
	"github.com/arafathusayn/herdsman/internal/choices"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// lockedBuffer is a bytes.Buffer safe for the server goroutine to write
// while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func waitFor(t *testing.T, out *lockedBuffer, re *regexp.Regexp) []string {
	t.Helper()
	for range 500 {
		if m := re.FindStringSubmatch(out.String()); m != nil {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s in %q", re, out.String())
	return nil
}

// herdsman web's startup wiring: the saved picks (Choices), the GitHub
// account probe and the activity file reach the server, against a fake
// herdr, temporary config and an ephemeral port; cancelling the context
// shuts it down.
func TestWebStartupWiring(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	fake := herdrtest.Start(t)
	t.Setenv("HERDR_SOCKET_PATH", fake.Socket)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin) // no gh, no harness: every probe answers fast
	p := tempChoices(t)
	st := &choices.Store{Path: p}
	if err := st.Save(choices.Pick{Role: "reviewer", Runner: "herdr", Harness: "codex", Model: "model-a"}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://user:secret@github.com/o/r.git"}} {
		if out, err := exec.Command(git, append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	webContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { webContext = signalContext })
	var out, errOut lockedBuffer
	done := make(chan error, 1)
	go func() { done <- runWeb([]string{"--port", "0", "--no-qr", "--client", "none"}, &out, &errOut) }()

	m := waitFor(t, &out, regexp.MustCompile(`(http://127\.0\.0\.1:\d+)/#t=([0-9A-Za-z_-]+)`))
	base, token := m[1], m[2]
	do := func(method, path, body string, hdr map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}
	var pair struct {
		Pair   string `json:"pair"`
		Device string `json:"device"`
	}
	if err := json.NewDecoder(do("POST", "/api/session", "", map[string]string{"X-Herdsman-Token": token}).Body).Decode(&pair); err != nil || pair.Pair == "" {
		t.Fatalf("session %+v %v", pair, err)
	}
	c := waitFor(t, &out, regexp.MustCompile("Code: \x1b\\[1m(\\S+) (\\S+)\x1b"))
	if err := json.NewDecoder(do("POST", "/api/pair", `{"code":"`+c[1]+c[2]+`"}`, map[string]string{"X-Herdsman-Pair": pair.Pair}).Body).Decode(&pair); err != nil || pair.Device == "" {
		t.Fatalf("pair %+v %v", pair, err)
	}
	authed := map[string]string{"Authorization": "Bearer " + pair.Device}

	// Choices: the catalog carries the saved pick.
	var cat struct {
		Previous []choices.Pick `json:"previous"`
	}
	if err := json.NewDecoder(do("GET", "/api/catalog", "", authed).Body).Decode(&cat); err != nil || len(cat.Previous) != 1 || cat.Previous[0].Model != "model-a" {
		t.Fatalf("catalog previous %+v %v", cat.Previous, err)
	}
	// GitHub: the real probe reads the repository's origin, never echoing
	// its credentials, and reports the missing gh.
	b := new(bytes.Buffer)
	_, _ = b.ReadFrom(do("GET", "/api/routes/github?folder="+repo, "", authed).Body)
	if !strings.Contains(b.String(), `"repo":"o/r"`) || !strings.Contains(b.String(), "gh not found") || strings.Contains(b.String(), "secret") {
		t.Fatalf("github %s", b)
	}
	// ActivityFile: the paired browser's navigation was recorded.
	if _, err := os.Stat(afk.DefaultWebFile()); err != nil {
		t.Fatalf("activity file: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v (%s)", err, errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not stop")
	}
}
