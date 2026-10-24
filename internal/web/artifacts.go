package web

import (
	"encoding/json"
	"io/fs"
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
)

// Artifacts are HTML files that agents leave in the session's folders: a
// report, a review page, a plan. The Artifacts tab lists them and shows one
// in a sandboxed frame. The frame loads it from a capability URL that opens
// only the file's own folder, so a page's relative images and styles work
// while its scripts run in an opaque origin that holds no herdsman secret.

const (
	// A listing is reused this long; an older one is still served while a
	// walk in the background replaces it, so polling never waits on a walk.
	artifactsTTL     = 15 * time.Second
	artifactMaxDepth = 6
	artifactMaxSeen  = 6000 // folder entries walked per root
	artifactMaxFiles = 500
	artifactMaxSize  = 16 << 20
	capTTL           = 2 * time.Hour
	maxCaps          = 64
)

// Artifact is one HTML file under one of the session's folders.
type Artifact struct {
	Root     string    `json:"root"`
	Path     string    `json:"path"` // slash-separated, relative to Root
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type artifactCap struct {
	dir     string
	expires time.Time
}

type artifacts struct {
	mu         sync.Mutex
	walk       sync.Mutex // one walk at a time
	now        func() time.Time
	key        string
	listed     time.Time
	list       []Artifact
	refreshing bool
	caps       map[string]artifactCap
}

func newArtifacts() *artifacts {
	return &artifacts{now: time.Now, caps: map[string]artifactCap{}}
}

// Folders named here are never walked: dependencies, build caches and
// version control hold no artifacts an agent wrote.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "__pycache__": true,
	"venv": true, "Library": true, "Pods": true, "DerivedData": true, "coverage": true,
}

// Hidden folders are skipped except these, where agents keep their pages.
var hiddenArtifactDirs = map[string]bool{".lavish": true, ".herdsman": true, ".artifacts": true}

// artifactRoots are the session's folders: every pane's folders, every route
// folder and every task worktree. The home folder and the file system root
// are left out: walking them would take long and find unrelated pages.
func (s *Server) artifactRoots() []string {
	st := s.hub.Current()
	var dirs []string
	add := func(p *string) {
		if p != nil && *p != "" {
			dirs = append(dirs, *p)
		}
	}
	for _, p := range st.Snapshot.Panes {
		add(p.Cwd)
		add(p.ForegroundCwd)
	}
	for _, r := range st.Routes {
		dirs = append(dirs, r.Dir)
		for _, t := range r.Tasks {
			if filepath.IsAbs(t.Worktree) {
				dirs = append(dirs, t.Worktree)
			}
		}
	}
	home, _ := os.UserHomeDir()
	var out []string
	for _, d := range dirs {
		d = filepath.Clean(d)
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
// waits for a walk; later ones get the last listing while a stale one is
// refreshed in the background.
func (a *artifacts) find(roots []string) []Artifact {
	key := strings.Join(roots, "\x00")
	a.mu.Lock()
	list := a.list
	if list != nil && (a.refreshing || (key == a.key && a.now().Sub(a.listed) < artifactsTTL)) {
		a.mu.Unlock()
		return list
	}
	if list != nil {
		a.refreshing = true
		a.mu.Unlock()
		go a.refresh(roots, key)
		return list
	}
	a.mu.Unlock()
	return a.refresh(roots, key)
}

func (a *artifacts) refresh(roots []string, key string) []Artifact {
	a.walk.Lock()
	defer a.walk.Unlock()
	a.mu.Lock()
	if a.list != nil && key == a.key && a.now().Sub(a.listed) < artifactsTTL {
		list := a.list
		a.refreshing = false
		a.mu.Unlock()
		return list
	}
	a.mu.Unlock()
	list := walkArtifacts(roots)
	a.mu.Lock()
	a.key, a.listed, a.list, a.refreshing = key, a.now(), list, false
	a.mu.Unlock()
	return list
}

// walkArtifacts walks roots for HTML files. A file under two roots belongs
// to the deeper one.
func walkArtifacts(roots []string) []Artifact {
	byPath := map[string]Artifact{}
	for _, root := range roots {
		seen := 0
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			seen++
			if seen > artifactMaxSeen || len(byPath) >= artifactMaxFiles {
				return fs.SkipAll
			}
			rel, _ := filepath.Rel(root, p)
			if d.IsDir() {
				if p == root {
					return nil
				}
				name := d.Name()
				hidden := strings.HasPrefix(name, ".") && !hiddenArtifactDirs[name]
				if hidden || skipDirs[name] || strings.Count(rel, string(filepath.Separator)) >= artifactMaxDepth {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !isHTML(d.Name()) {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() > artifactMaxSize {
				return nil
			}
			if old, ok := byPath[p]; ok && len(old.Root) >= len(root) {
				return nil
			}
			byPath[p] = Artifact{Root: root, Path: filepath.ToSlash(rel), Size: info.Size(), Modified: info.ModTime()}
			return nil
		})
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

func isHTML(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".html" || ext == ".htm"
}

// grant returns a capability for dir, reusing a live one.
func (a *artifacts) grant(dir string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for k, c := range a.caps {
		if now.After(c.expires) {
			delete(a.caps, k)
		}
	}
	for k, c := range a.caps {
		if c.dir == dir {
			a.caps[k] = artifactCap{dir: dir, expires: now.Add(capTTL)}
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
		delete(a.caps, oldest)
	}
	k := secret()
	a.caps[k] = artifactCap{dir: dir, expires: now.Add(capTTL)}
	return k
}

func (a *artifacts) dir(cap string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.caps[cap]
	if !ok || a.now().After(c.expires) {
		return "", false
	}
	return c.dir, true
}

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	roots := s.artifactRoots()
	writeJSON(w, http.StatusOK, map[string]any{"roots": roots, "artifacts": s.artifacts.find(roots)})
}

// openArtifact grants a frame access to one listed artifact's folder.
func (s *Server) openArtifact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Root string `json:"root"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	for _, a := range s.artifacts.find(s.artifactRoots()) {
		if a.Root != body.Root || a.Path != body.Path {
			continue
		}
		dir, file := path.Split(a.Path)
		cap := s.artifacts.grant(filepath.Join(a.Root, filepath.FromSlash(dir)))
		writeJSON(w, http.StatusOK, map[string]string{"url": "/artifact/" + cap + "/" + url.PathEscape(file)})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such artifact in the session's folders"})
}

// serveArtifact serves a file from a granted folder. Hidden files stay out,
// os.Root keeps links from leaving the folder, and the CSP sandbox gives the
// page an opaque origin even when it is opened outside the frame. No CORS
// header is sent, so the page's scripts cannot read the folder's files.
func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.artifacts.dir(r.PathValue("cap"))
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
	root, err := os.OpenRoot(dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Del("Access-Control-Allow-Origin")
	h.Del("Access-Control-Expose-Headers")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}
