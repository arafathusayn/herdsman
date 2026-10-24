package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/route"
)

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The tab lists HTML pages from the session's folders, skipping dependency,
// build and hidden folders except the ones agents keep pages in.
func TestArtifactsListAndServeInASandbox(t *testing.T) {
	f := newFixture(t)
	proj := filepath.Dir(filepath.Dir(f.routeDir))
	writeFile(t, filepath.Join(proj, "reports", "summary.html"), `<link rel="stylesheet" href="style.css"><p>hi</p>`)
	writeFile(t, filepath.Join(proj, "reports", "style.css"), `p{color:red}`)
	writeFile(t, filepath.Join(proj, "reports", ".env"), `SECRET=1`)
	writeFile(t, filepath.Join(proj, "reports", "credentials.json"), `{"key":"x"}`)
	writeFile(t, filepath.Join(proj, "reports", "logo.PNG"), `png`)
	writeFile(t, filepath.Join(proj, ".lavish", "plan.html"), `<p>plan</p>`)
	writeFile(t, filepath.Join(proj, "node_modules", "pkg", "index.html"), `x`)
	writeFile(t, filepath.Join(proj, ".git", "x.html"), `x`)
	writeFile(t, filepath.Join(proj, "notes.md"), `x`)

	res := f.request(t, http.MethodGet, "/api/artifacts", "", f.authed)
	var list struct {
		Roots     []string   `json:"roots"`
		Artifacts []Artifact `json:"artifacts"`
	}
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range list.Artifacts {
		got = append(got, a.Path)
	}
	if strings.Join(got, ",") != ".lavish/plan.html,reports/summary.html" && strings.Join(got, ",") != "reports/summary.html,.lavish/plan.html" {
		t.Fatalf("artifacts = %v (roots %v)", got, list.Roots)
	}
	if res := f.request(t, http.MethodGet, "/api/artifacts", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired listing: %d", res.StatusCode)
	}

	root := list.Artifacts[0].Root
	res = f.request(t, http.MethodPost, "/api/artifacts/open", `{"root":"`+root+`","path":"reports/summary.html"}`, f.authed)
	var opened struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&opened); err != nil || !strings.HasPrefix(opened.URL, "/artifact/") {
		t.Fatalf("open: %d %+v %v", res.StatusCode, opened, err)
	}
	if res := f.request(t, http.MethodPost, "/api/artifacts/open", `{"root":"/etc","path":"passwd"}`, f.authed); res.StatusCode != http.StatusNotFound {
		t.Fatalf("open outside the listing: %d", res.StatusCode)
	}

	page := f.request(t, http.MethodGet, opened.URL, "", map[string]string{"Origin": "null"})
	if page.StatusCode != 200 || !strings.Contains(readBody(t, page), "<p>hi</p>") {
		t.Fatalf("page: %d", page.StatusCode)
	}
	h := page.Header
	// The frame's opaque origin may load module scripts; any real origin gets no CORS grant.
	if !strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox allow-scripts") || h.Get("X-Frame-Options") != "" || h.Get("Access-Control-Allow-Origin") != "null" {
		t.Fatalf("page headers: %v", h)
	}
	if other := f.request(t, http.MethodGet, opened.URL, "", map[string]string{"Origin": "https://elsewhere.example"}); other.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("a real origin got a CORS grant: %v", other.Header)
	}
	dir := strings.TrimSuffix(opened.URL, "summary.html")
	for path, want := range map[string]int{
		dir + "style.css":          200,
		dir + "logo.PNG":           200,
		dir + "credentials.json":   404,
		dir + ".env":               404,
		dir + "..%2fnotes.md":      404,
		dir + "../notes.md":        404,
		"/artifact/nope/style.css": 404,
	} {
		if res := f.request(t, http.MethodGet, path, "", nil); res.StatusCode != want {
			t.Errorf("%s: status %d, want %d", path, res.StatusCode, want)
		}
	}

	// A page can send its own URL anywhere; another machine still gets nothing.
	req := httptest.NewRequest(http.MethodGet, opened.URL, nil)
	req.Host = "127.0.0.1"
	req.RemoteAddr = "192.0.2.9:4000"
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("link reused from another address: %d", rec.Code)
	}
}

func TestArtifactCapsAreBounded(t *testing.T) {
	a := newArtifacts()
	first := a.grant("/a", "127.0.0.1")
	if a.grant("/a", "127.0.0.1") != first {
		t.Fatal("a live capability was not reused")
	}
	if a.grant("/a", "192.0.2.9") == first {
		t.Fatal("another browser got the same capability")
	}
	if _, ok := a.dir(first, "192.0.2.9"); ok {
		t.Fatal("a capability opened its folder to another browser")
	}
	for i := range maxCaps + 5 {
		a.grant("/d"+string(rune('a'+i%26))+strings.Repeat("x", i), "127.0.0.1")
	}
	if len(a.caps) > maxCaps {
		t.Fatalf("%d capabilities kept", len(a.caps))
	}
}

// After the first walk, a stale listing is served at once and replaced by a
// walk in the background.
func TestArtifactListingRefreshesInTheBackground(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.html"), "a")
	a := newArtifacts()
	now := time.Now()
	a.now = func() time.Time { return now }
	if got := a.find([]string{root}); len(got) != 1 {
		t.Fatalf("first walk: %v", got)
	}
	writeFile(t, filepath.Join(root, "b.html"), "b")
	if got := a.find([]string{root}); len(got) != 1 {
		t.Fatalf("fresh listing walked again: %v", got)
	}
	now = now.Add(artifactsTTL + time.Second)
	if got := a.find([]string{root}); len(got) != 1 {
		t.Fatalf("stale listing was not served at once: %v", got)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		a.mu.Lock()
		done := !a.refreshing
		a.mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
	}
	if got := a.find([]string{root}); len(got) != 2 {
		t.Fatalf("after the background walk: %v", got)
	}
}

// Task worktrees count in every form the task table allows, not only
// absolute paths, so a stopped task's pages still show.
func TestRouteFoldersResolveEveryWorktreeForm(t *testing.T) {
	home, _ := os.UserHomeDir()
	r := &route.Route{Dir: "/p/.herdsman/r-2026-10-24", Tasks: []route.Task{
		{Worktree: "/abs/wt"}, {Worktree: "wt/rel"}, {Worktree: "<repo>/.herdsman/worktrees/a"}, {Worktree: "~/wt/home"}, {Worktree: "-"}, {Worktree: ""},
	}}
	want := []string{"/p/.herdsman/r-2026-10-24", "/abs/wt", "/p/wt/rel", "/p/.herdsman/worktrees/a", filepath.Join(home, "wt/home")}
	if got := routeFolders(r); !slices.Equal(got, want) {
		t.Fatalf("folders = %v, want %v", got, want)
	}
}
