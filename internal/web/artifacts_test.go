package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
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
	// Links named like page assets: to a hidden file, and through a linked folder.
	if err := os.Symlink(".env", filepath.Join(proj, "reports", "public.js")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(proj, "reports", "real", "app.js"), `x`)
	if err := os.Symlink("real", filepath.Join(proj, "reports", "lib")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(proj, ".lavish", "plan.html"), `<p>plan</p>`)
	writeFile(t, filepath.Join(proj, "node_modules", "pkg", "index.html"), `x`)
	writeFile(t, filepath.Join(proj, ".git", "x.html"), `x`)
	writeFile(t, filepath.Join(proj, "notes.md"), `x`)

	id := f.hub.Current().Routes[0].ID
	res := f.request(t, http.MethodGet, "/api/artifacts?route="+id, "", f.authed)
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
	if res := f.request(t, http.MethodGet, "/api/artifacts?route="+id, "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired listing: %d", res.StatusCode)
	}

	root := list.Artifacts[0].Root
	res = f.request(t, http.MethodPost, "/api/artifacts/open", `{"route":"`+id+`","root":"`+root+`","path":"reports/summary.html"}`, f.authed)
	var opened struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&opened); err != nil || !strings.HasPrefix(opened.URL, "/artifact/") {
		t.Fatalf("open: %d %+v %v", res.StatusCode, opened, err)
	}
	if res := f.request(t, http.MethodPost, "/api/artifacts/open", `{"route":"`+id+`","root":"/etc","path":"passwd"}`, f.authed); res.StatusCode != http.StatusNotFound {
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
		dir + "real/app.js":        200,
		dir + "public.js":          404,
		dir + "lib/app.js":         404,
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
	waitRefreshed(t, a)
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

// A session folder that is itself a link is walked at its target, so its
// pages show; the same folder twice, a missing one, home and / are left out.
func TestWalkRootsResolveALinkedFolder(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real, link, home := filepath.Join(tmp, "real"), filepath.Join(tmp, "link"), filepath.Join(tmp, "home")
	writeFile(t, filepath.Join(real, "page.html"), "p")
	writeFile(t, filepath.Join(home, "x.html"), "x")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	roots := walkRoots([]string{link, real, filepath.Join(tmp, "missing"), "/", home}, home)
	if !slices.Equal(roots, []string{real}) {
		t.Fatalf("roots = %v, want [%s]", roots, real)
	}
	if got := newArtifacts().find(roots); len(got) != 1 || got[0].Path != "page.html" {
		t.Fatalf("pages = %+v", got)
	}
}

// waitRefreshed waits for every background walk to end.
func waitRefreshed(t *testing.T, a *artifacts) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		a.mu.Lock()
		busy := false
		for _, e := range a.lists {
			busy = busy || e.refreshing
		}
		a.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a background walk did not end")
		}
	}
}

// A listing is only ever served for the roots it was walked from: a route
// shown after another never gets the other's list, fresh, stale or while a
// walk runs, and the cache keeps a bounded number of listings.
func TestArtifactListingIsKeyedByRoots(t *testing.T) {
	one, two := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(one, "one.html"), "1")
	writeFile(t, filepath.Join(two, "two.html"), "2")
	a := newArtifacts()
	now := time.Now()
	a.now = func() time.Time { return now }
	names := func(l []Artifact) string {
		var s []string
		for _, x := range l {
			s = append(s, x.Path)
		}
		return strings.Join(s, ",")
	}
	if got := names(a.find([]string{one})); got != "one.html" {
		t.Fatalf("first route: %s", got)
	}
	if got := names(a.find([]string{two})); got != "two.html" {
		t.Fatalf("second route got %s", got)
	}
	now = now.Add(artifactsTTL + time.Second)
	if got := names(a.find([]string{one})); got != "one.html" {
		t.Fatalf("stale first route: %s", got)
	}
	// The first route's list is refreshing; the second still gets its own.
	if got := names(a.find([]string{two})); got != "two.html" {
		t.Fatalf("second route during a walk got %s", got)
	}
	waitRefreshed(t, a)
	for i := range artifactMaxLists + 3 {
		d := t.TempDir()
		writeFile(t, filepath.Join(d, "p.html"), "p")
		now = now.Add(time.Millisecond * time.Duration(i+1))
		if got := names(a.find([]string{d})); got != "p.html" {
			t.Fatalf("root %d: %s", i, got)
		}
	}
	waitRefreshed(t, a)
	a.mu.Lock()
	n := len(a.lists)
	a.mu.Unlock()
	if n > artifactMaxLists {
		t.Fatalf("%d listings kept", n)
	}
}

const twoWorkspaceRoute = "# Route %s\n\nOrchestrator: this session, pane %s (tab %s).\n\n" +
	"## Task table\n\n| Task | Agent | Pane | Worktree |\n| --- | --- | --- | --- |\n" +
	"| t | im-%s | %s | %s |\n"

// A route's Artifacts tab lists only its own Herdr workspace's folders and
// its own route folder and worktrees, never another workspace's pages.
func TestArtifactsAreScopedToTheRoutesWorkspace(t *testing.T) {
	f := newFixture(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projA, projB, wtA := filepath.Join(tmp, "a"), filepath.Join(tmp, "b"), filepath.Join(tmp, "wt-a")
	dirA, dirB := filepath.Join(projA, ".herdsman", "ra-2026-10-25"), filepath.Join(projB, ".herdsman", "rb-2026-10-25")
	writeFile(t, filepath.Join(dirA, route.RouteFile), fmt.Sprintf(twoWorkspaceRoute, "ra", "w1:p1", "w1:t1", "a", "w1:p2", wtA))
	writeFile(t, filepath.Join(dirB, route.RouteFile), fmt.Sprintf(twoWorkspaceRoute, "rb", "w2:p1", "w2:t1", "b", "w2:p2", "-"))
	writeFile(t, filepath.Join(projA, "reports", "a.html"), "a")
	writeFile(t, filepath.Join(dirA, "route-page.html"), "r")
	writeFile(t, filepath.Join(wtA, "wt.html"), "w")
	writeFile(t, filepath.Join(projB, "reports", "b.html"), "b")
	fg := filepath.Join(tmp, "fg-a")
	writeFile(t, filepath.Join(fg, "fg.html"), "f")
	f.herdr.Handle("session.snapshot", func(map[string]any) (any, string) {
		return map[string]any{"snapshot": map[string]any{
			"version": "0.9.4", "protocol": 22, "focused_tab_id": "w1:t1",
			"workspaces": []map[string]any{{"workspace_id": "w1", "number": 1}, {"workspace_id": "w2", "number": 2}},
			"panes": []map[string]any{
				{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "cwd": projA, "foreground_cwd": fg, "agent_status": "idle"},
				{"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1", "cwd": projA, "agent_status": "working"},
				{"pane_id": "w2:p1", "tab_id": "w2:t1", "workspace_id": "w2", "cwd": projB, "agent_status": "idle"},
				{"pane_id": "w2:p2", "tab_id": "w2:t2", "workspace_id": "w2", "cwd": projB, "agent_status": "working"},
			},
			"agents": []map[string]any{
				{"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1", "name": "im-a", "agent_status": "working"},
				{"pane_id": "w2:p2", "tab_id": "w2:t2", "workspace_id": "w2", "name": "im-b", "agent_status": "working"},
			},
		}}, ""
	})
	f.hub.refresh(context.Background())
	ids := map[string]string{}
	for _, r := range f.hub.Current().Routes {
		ids[strings.SplitN(r.Name, "-", 2)[0]] = r.ID
	}
	if ids["ra"] == "" || ids["rb"] == "" {
		t.Fatalf("routes = %v", ids)
	}

	type listing struct {
		Roots     []string   `json:"roots"`
		Artifacts []Artifact `json:"artifacts"`
	}
	list := func(id string) listing {
		t.Helper()
		res := f.request(t, http.MethodGet, "/api/artifacts?route="+url.QueryEscape(id), "", f.authed)
		var l listing
		if err := json.NewDecoder(res.Body).Decode(&l); err != nil || res.StatusCode != 200 {
			t.Fatalf("list %s: %d %v", id, res.StatusCode, err)
		}
		return l
	}
	pages := func(l listing) []string {
		var out []string
		for _, a := range l.Artifacts {
			out = append(out, filepath.Join(a.Root, filepath.FromSlash(a.Path)))
		}
		slices.Sort(out)
		return out
	}
	wantA := []string{filepath.Join(dirA, "route-page.html"), filepath.Join(fg, "fg.html"), filepath.Join(projA, "reports", "a.html"), filepath.Join(wtA, "wt.html")}
	slices.Sort(wantA)
	wantB := []string{filepath.Join(projB, "reports", "b.html")}
	// Switching back and forth never shows the other route's pages.
	for range 2 {
		if got := pages(list(ids["ra"])); !slices.Equal(got, wantA) {
			t.Fatalf("route a pages = %v, want %v", got, wantA)
		}
		if got := pages(list(ids["rb"])); !slices.Equal(got, wantB) {
			t.Fatalf("route b pages = %v, want %v", got, wantB)
		}
	}

	open := func(id, root, path string) int {
		b, _ := json.Marshal(map[string]string{"route": id, "root": root, "path": path})
		return f.request(t, http.MethodPost, "/api/artifacts/open", string(b), f.authed).StatusCode
	}
	if code := open(ids["ra"], projA, "reports/a.html"); code != 200 {
		t.Fatalf("open own page: %d", code)
	}
	if code := open(ids["ra"], projB, "reports/b.html"); code != http.StatusNotFound {
		t.Fatalf("open another workspace's page: %d", code)
	}
	if code := open("nope~000000", projA, "reports/a.html"); code != http.StatusNotFound {
		t.Fatalf("open with an unknown route: %d", code)
	}
	if code := open("", projA, "reports/a.html"); code != http.StatusBadRequest {
		t.Fatalf("open without a route: %d", code)
	}
	for path, want := range map[string]int{"/api/artifacts": 400, "/api/artifacts?route=nope~000000": 404} {
		res := f.request(t, http.MethodGet, path, "", f.authed)
		var e struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(res.Body).Decode(&e); err != nil || res.StatusCode != want || e.Error == "" {
			t.Fatalf("%s: %d %+v %v", path, res.StatusCode, e, err)
		}
	}
}

// A route no workspace holds gets only its own folders.
func TestArtifactRootsOfAnUnplacedRoute(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, pane := filepath.Join(tmp, "p", ".herdsman", "r"), filepath.Join(tmp, "pane")
	for _, d := range []string{dir, pane} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	snap := herdr.Snapshot{Panes: []herdr.Pane{{ID: "w1:p1", WorkspaceID: "w1", Cwd: &pane}}}
	rv := RouteView{Route: &route.Route{Dir: dir, OrchestratorPane: "w9:p9"}, Live: true}
	if got := routeArtifactRoots(snap, rv, ""); !slices.Equal(got, []string{dir}) {
		t.Fatalf("roots = %v", got)
	}
	rv.OrchestratorPane = "w1:p1"
	if got := routeArtifactRoots(snap, rv, ""); !slices.Equal(got, []string{dir, pane}) {
		t.Fatalf("placed roots = %v", got)
	}
	rv.Live = false
	if got := routeArtifactRoots(snap, rv, ""); !slices.Equal(got, []string{dir}) {
		t.Fatalf("a stopped route took a workspace: %v", got)
	}
}
