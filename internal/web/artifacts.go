package web

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/route"
)

// Artifacts are HTML files that agents leave in a route's folders: a report,
// a review page, a plan. A route's Artifacts tab lists the ones in its Herdr
// workspace's folders and shows one in a sandboxed frame. The frame loads it from a capability URL that opens
// only the page assets in the file's own folder, and only to the browser that
// asked for it, so a page's relative images and styles work while its scripts
// run in an opaque origin that holds no herdsman secret.

const (
	// A listing is reused this long; an older one is still served while a
	// walk in the background replaces it, so polling never waits on a walk.
	artifactsTTL     = 15 * time.Second
	artifactMaxDepth = 6
	artifactMaxSeen  = 6000 // folder entries walked per root
	artifactBatch    = 256  // folder entries read at once
	artifactMaxFiles = 500
	artifactMaxSize  = 16 << 20
	artifactMaxLists = 8 // listings kept, one per set of roots
	capTTL           = 2 * time.Hour
	maxCaps          = 64
)

// Artifact is one HTML file under one of a route's folders.
type Artifact struct {
	Root     string    `json:"root"`
	Path     string    `json:"path"` // slash-separated, relative to Root
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// rootID is the folder the walk opened at Root, so a grant opens only
	// that folder, even when Root's path leads elsewhere by then.
	rootID os.FileInfo
	// dirID is the folder the walk read the page in, so a grant refuses a
	// folder replaced by another of the same name, even inside Root.
	dirID os.FileInfo
}

// A capability is bound to the address of the browser that opened it: a page
// can read its own URL and send it anywhere, so the URL alone must not open
// the folder to another machine. It holds the granted folder open, not its
// name, so a folder renamed and replaced by a link after the grant never
// turns the link into a grant of another folder.
type artifactCap struct {
	dir     *os.Root
	info    os.FileInfo // the folder, to reuse a live grant of it
	client  string
	expires time.Time
}

// pageAssets are the files a page loads by itself: frames, scripts, styles,
// images, fonts and media. Data files (JSON, text, keys, configs) are never
// served, to the page or to anyone holding its link.
var pageAssets = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".js": true, ".mjs": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".avif": true, ".svg": true, ".ico": true, ".bmp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
	".mp4": true, ".webm": true, ".ogg": true, ".mp3": true, ".wav": true, ".m4a": true,
}

// clientOf is the address a request comes from, without its port.
func clientOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// artifactList is the last listing of one set of roots.
type artifactList struct {
	listed     time.Time
	used       time.Time
	list       []Artifact
	refreshing bool
}

type artifacts struct {
	mu   sync.Mutex
	walk sync.Mutex // one walk at a time
	now  func() time.Time
	// lists holds a listing per set of roots, so a list is only ever served
	// for the roots it was walked from; the least recently used goes first.
	lists map[string]*artifactList
	caps  map[string]artifactCap
	ttl   time.Duration // how long a grant lasts
	sweep *time.Timer   // closes expired grants; nil while none is set
}

func newArtifacts() *artifacts {
	return &artifacts{now: time.Now, lists: map[string]*artifactList{}, caps: map[string]artifactCap{}, ttl: capTTL}
}

// Folders named here are never walked: dependencies, build caches and
// version control hold no artifacts an agent wrote.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "__pycache__": true,
	"venv": true, "Library": true, "Pods": true, "DerivedData": true, "coverage": true,
}

// Hidden folders are skipped except these, where agents keep their pages.
var hiddenArtifactDirs = map[string]bool{".lavish": true, ".herdsman": true, ".artifacts": true}

// routeFolders is a route's own folder and its tasks' worktrees, with every
// worktree form the task table allows resolved as the Changes tab does.
func routeFolders(r *route.Route) []string {
	dirs := []string{r.Dir}
	project := filepath.Dir(filepath.Dir(r.Dir)) // <project>/.herdsman/<route>
	for _, t := range r.Tasks {
		if w := strings.TrimSpace(t.Worktree); w != "" && w != "-" {
			dirs = append(dirs, worktreeDir(project, w))
		}
	}
	return dirs
}

// routeWorkspace is the Herdr workspace a route runs in, placed as the web
// app places it: the workspace of its orchestrator pane, else its adjutant
// pane, else its first task pane Herdr still has. A stopped route has none,
// since the panes it names may now belong to another route.
func routeWorkspace(snap herdr.Snapshot, rv RouteView) string {
	if !rv.Live {
		return ""
	}
	ws := map[string]string{}
	for _, p := range snap.Panes {
		ws[p.ID] = p.WorkspaceID
	}
	panes := []string{rv.OrchestratorPane, rv.AdjutantPane}
	for _, t := range rv.Tasks {
		panes = append(panes, t.Pane)
	}
	for _, p := range panes {
		if w, ok := ws[p]; ok && p != "" {
			return w
		}
	}
	return ""
}

// routeArtifactRoots are the folders a route's Artifacts tab walks: the
// folders of the panes in its workspace, its route folder and its task
// worktrees. Other workspaces' panes are left out, so pages from unrelated
// projects never show.
func routeArtifactRoots(snap herdr.Snapshot, rv RouteView, home string) []string {
	dirs := routeFolders(rv.Route)
	if ws := routeWorkspace(snap, rv); ws != "" {
		for _, p := range snap.Panes {
			if p.WorkspaceID != ws {
				continue
			}
			for _, d := range []*string{p.Cwd, p.ForegroundCwd} {
				if d != nil && *d != "" {
					dirs = append(dirs, *d)
				}
			}
		}
	}
	return walkRoots(dirs, home)
}

// artifactRoute is the route a request names, or nil after an error reply.
func (s *Server) artifactRoute(w http.ResponseWriter, id string) (*RouteView, herdr.Snapshot) {
	st := s.hub.Current()
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "route is required"})
		return nil, st.Snapshot
	}
	rv := findRoute(st.Routes, id)
	if rv == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown route"})
	}
	return rv, st.Snapshot
}

func (s *Server) artifactRoots(snap herdr.Snapshot, rv *RouteView) []string {
	home, _ := os.UserHomeDir()
	return routeArtifactRoots(snap, *rv, home)
}

// walkRoots keeps the folders worth walking, once each and sorted. The home
// folder and the file system root are left out: walking them would take long
// and find unrelated pages. A folder that is itself a link is walked at its
// target, since a walk does not descend into a link given as its root.
func walkRoots(dirs []string, home string) []string {
	var out []string
	for _, d := range dirs {
		d = filepath.Clean(d)
		if st, err := os.Lstat(d); err == nil && st.Mode()&os.ModeSymlink != 0 {
			if d, err = filepath.EvalSymlinks(d); err != nil {
				continue
			}
		}
		if !filepath.IsAbs(d) || d == "/" || d == home || slices.Contains(out, d) {
			continue
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// find lists the HTML files under roots, newest first. Only the first call
// for a set of roots waits for a walk; later ones get that set's last
// listing while a stale one is refreshed in the background.
func (a *artifacts) find(roots []string) []Artifact {
	key := strings.Join(roots, "\x00")
	a.mu.Lock()
	if e := a.lists[key]; e != nil {
		e.used = a.now()
		list := e.list
		if !e.refreshing && a.now().Sub(e.listed) >= artifactsTTL {
			e.refreshing = true
			go a.refresh(roots, key)
		}
		a.mu.Unlock()
		return list
	}
	a.mu.Unlock()
	return a.refresh(roots, key)
}

func (a *artifacts) refresh(roots []string, key string) []Artifact {
	a.walk.Lock()
	defer a.walk.Unlock()
	a.mu.Lock()
	if e := a.lists[key]; e != nil && a.now().Sub(e.listed) < artifactsTTL {
		e.refreshing = false
		a.mu.Unlock()
		return e.list
	}
	a.mu.Unlock()
	list := walkArtifacts(roots)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if _, ok := a.lists[key]; !ok && len(a.lists) >= artifactMaxLists {
		var oldest string
		for k, e := range a.lists {
			if oldest == "" || e.used.Before(a.lists[oldest].used) {
				oldest = k
			}
		}
		delete(a.lists, oldest)
	}
	a.lists[key] = &artifactList{listed: now, used: now, list: list}
	return list
}

// walkArtifacts walks roots for HTML files. A file under two roots belongs
// to the deeper one.
func walkArtifacts(roots []string) []Artifact {
	byPath := map[string]Artifact{}
	for _, root := range roots {
		walkRoot(root, byPath)
	}
	list := make([]Artifact, 0, len(byPath))
	for _, v := range byPath {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Modified.Equal(list[j].Modified) {
			return list[i].Modified.After(list[j].Modified)
		}
		return list[i].Root+list[i].Path < list[j].Root+list[j].Path
	})
	return list
}

// walkRoot adds the HTML files under root to byPath. Folders are read in
// batches that stop at the entry budget, so a folder far wider than the
// budget is never read or held whole, and os.Root keeps the walk inside root.
func walkRoot(root string, byPath map[string]Artifact) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return
	}
	defer r.Close()
	id, err := r.Stat(".")
	if err != nil {
		return
	}
	budget := artifactMaxSeen
	stack := []string{"."} // folders left to read, relative to root
	for len(stack) > 0 && budget > 0 && len(byPath) < artifactMaxFiles {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		f, err := r.Open(dir)
		if err != nil {
			continue
		}
		dirID, err := f.Stat()
		if err != nil {
			_ = f.Close()
			continue
		}
		for budget > 0 && len(byPath) < artifactMaxFiles {
			entries, err := f.ReadDir(min(artifactBatch, budget))
			budget -= len(entries)
			for _, d := range entries {
				rel := filepath.Join(dir, d.Name())
				if d.IsDir() {
					name := d.Name()
					hidden := strings.HasPrefix(name, ".") && !hiddenArtifactDirs[name]
					if !hidden && !skipDirs[name] && strings.Count(rel, string(filepath.Separator)) < artifactMaxDepth {
						stack = append(stack, rel)
					}
					continue
				}
				addArtifact(Artifact{Root: root, Path: filepath.ToSlash(rel), rootID: id, dirID: dirID}, d, byPath)
			}
			if err != nil { // io.EOF at the folder's end
				break
			}
		}
		_ = f.Close()
	}
}

// addArtifact adds the file a names, found as d, when it is an HTML page.
func addArtifact(a Artifact, d fs.DirEntry, byPath map[string]Artifact) {
	if len(byPath) >= artifactMaxFiles || !d.Type().IsRegular() || !isHTML(d.Name()) {
		return
	}
	info, err := d.Info()
	if err != nil || info.Size() > artifactMaxSize {
		return
	}
	p := filepath.Join(a.Root, filepath.FromSlash(a.Path))
	if old, ok := byPath[p]; ok && len(old.Root) >= len(a.Root) {
		return
	}
	a.Size, a.Modified = info.Size(), info.ModTime()
	byPath[p] = a
}

func isHTML(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".html" || ext == ".htm"
}

// errNotListed refuses a folder that is no longer the one listed.
var errNotListed = errors.New("the folder changed since it was listed")

// openPageDir opens the folder rel (slash-separated) under a listed root and
// pins it: the folder opened at root must be the one the walk listed (id),
// whatever link now sits at root or any folder above it, no link may sit on
// the way to rel, and the folder opened at rel must be the one the walk read
// the page in (dirID), so the handle reaches only the folder listed. os.Root
// keeps a link swapped in during the open inside the root too.
func openPageDir(root string, id os.FileInfo, rel string, dirID os.FileInfo) (*os.Root, error) {
	if id == nil || dirID == nil {
		return nil, errNotListed
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	if opened, err := r.Stat("."); err != nil || !os.SameFile(id, opened) {
		_ = r.Close()
		return nil, errNotListed
	}
	rel = strings.Trim(rel, "/")
	if rel == "" {
		if !os.SameFile(id, dirID) {
			_ = r.Close()
			return nil, errNotListed
		}
		return r, nil
	}
	segs := strings.Split(rel, "/")
	for i := range segs {
		st, err := r.Lstat(filepath.Join(segs[:i+1]...))
		if err != nil || !st.IsDir() { // a link is not a folder to Lstat
			_ = r.Close()
			return nil, errNotListed
		}
	}
	sub, err := r.OpenRoot(filepath.FromSlash(rel))
	_ = r.Close()
	if err != nil {
		return nil, err
	}
	if opened, err := sub.Stat("."); err != nil || !os.SameFile(dirID, opened) {
		_ = sub.Close()
		return nil, errNotListed
	}
	return sub, nil
}

// grant returns a capability for the open folder dir that only client may
// use, reusing a live one for the same folder. The capability owns dir.
func (a *artifacts) grant(dir *os.Root, client string) string {
	info, _ := dir.Stat(".")
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.dropExpired(now)
	defer a.armSweep(now)
	for k, c := range a.caps {
		if c.client == client && info != nil && c.info != nil && os.SameFile(c.info, info) {
			_ = dir.Close()
			c.expires = now.Add(a.ttl)
			a.caps[k] = c
			return k
		}
	}
	if len(a.caps) >= maxCaps {
		var oldest string
		for k, c := range a.caps {
			if oldest == "" || c.expires.Before(a.caps[oldest].expires) {
				oldest = k
			}
		}
		a.drop(oldest)
	}
	k := secret()
	a.caps[k] = artifactCap{dir: dir, info: info, client: client, expires: now.Add(a.ttl)}
	return k
}

// drop closes a grant's folder and forgets the grant. The caller holds mu.
func (a *artifacts) drop(k string) {
	_ = a.caps[k].dir.Close()
	delete(a.caps, k)
}

// dropExpired drops the grants that expired by now. The caller holds mu.
func (a *artifacts) dropExpired(now time.Time) {
	for k, c := range a.caps {
		if now.After(c.expires) {
			a.drop(k)
		}
	}
}

// armSweep sets a timer for the next grant to expire, so expired grants
// close their folders without waiting for another grant. One timer at most,
// and none while no grant is live; it holds no goroutine while it waits.
// The caller holds mu.
func (a *artifacts) armSweep(now time.Time) {
	if a.sweep != nil || len(a.caps) == 0 {
		return
	}
	var next time.Time
	for _, c := range a.caps {
		if next.IsZero() || c.expires.Before(next) {
			next = c.expires
		}
	}
	a.sweep = time.AfterFunc(max(next.Sub(now), 0)+time.Millisecond, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.sweep = nil
		now := a.now()
		a.dropExpired(now)
		a.armSweep(now)
	})
}

// dir is the folder cap opens for client; an expired grant is dropped. A use
// that races a grant closing the folder fails to read it and serves nothing.
func (a *artifacts) dir(cap, client string) (*os.Root, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.caps[cap]
	if ok && a.now().After(c.expires) {
		a.drop(cap)
		return nil, false
	}
	if !ok || c.client != client {
		return nil, false
	}
	return c.dir, true
}

// listArtifacts lists the HTML files in one route's workspace folders.
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	rv, snap := s.artifactRoute(w, r.URL.Query().Get("route"))
	if rv == nil {
		return
	}
	roots := s.artifactRoots(snap, rv)
	writeJSON(w, http.StatusOK, map[string]any{"roots": roots, "artifacts": s.artifacts.find(roots)})
}

// openArtifact grants a frame access to the folder of one artifact in the
// route's listing.
func (s *Server) openArtifact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Route string `json:"route"`
		Root  string `json:"root"`
		Path  string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	rv, snap := s.artifactRoute(w, body.Route)
	if rv == nil {
		return
	}
	for _, a := range s.artifacts.find(s.artifactRoots(snap, rv)) {
		if a.Root != body.Root || a.Path != body.Path {
			continue
		}
		dir, file := path.Split(a.Path)
		d, err := openPageDir(a.Root, a.rootID, dir, a.dirID)
		if err != nil {
			break
		}
		cap := s.artifacts.grant(d, clientOf(r))
		writeJSON(w, http.StatusOK, map[string]string{"url": "/artifact/" + cap + "/" + url.PathEscape(file)})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such artifact in the route's workspace folders"})
}

// serveArtifact serves a page asset from a granted folder to the browser the
// folder was granted to. Hidden files and data files stay out, the folder is
// the one held open at the grant, os.Root keeps links from leaving it, and the CSP sandbox gives the page an opaque
// origin even when it is opened outside the frame, so its scripts reach no
// herdsman secret. Only that opaque origin gets a CORS header, which module
// scripts need.
func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request) {
	root, ok := s.artifacts.dir(r.PathValue("cap"), clientOf(r))
	if !ok {
		http.Error(w, "this artifact link expired: open it again from the Artifacts tab", http.StatusNotFound)
		return
	}
	rel := r.PathValue("path")
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			http.NotFound(w, r)
			return
		}
	}
	if !pageAssets[strings.ToLower(path.Ext(rel))] {
		http.NotFound(w, r)
		return
	}
	// os.Root follows links that stay in the folder, so a link named like a
	// page asset could serve a hidden or data file. No link may sit on the
	// path, and the file opened must be the one checked.
	segs := strings.Split(rel, "/")
	var last os.FileInfo
	var err error
	for i := range segs {
		last, err = root.Lstat(filepath.Join(segs[:i+1]...))
		if err != nil || last.Mode()&os.ModeSymlink != 0 {
			http.NotFound(w, r)
			return
		}
	}
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(st, last) {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Del("Access-Control-Expose-Headers")
	h.Del("Access-Control-Allow-Origin")
	// The frame's opaque origin sends "Origin: null", and module scripts load
	// in CORS mode, so they need this. It opens nothing new: the link already
	// serves only page assets, only to this browser.
	if r.Header.Get("Origin") == "null" {
		h.Set("Access-Control-Allow-Origin", "null")
	}
	h.Add("Vary", "Origin")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}
