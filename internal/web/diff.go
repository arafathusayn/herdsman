package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/gitdiff"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/route"
)

// DiffSource is the code change of one task of a route, of a worktree an
// agent works in, or of the project checkout the route lives in.
type DiffSource struct {
	ID     string        `json:"id"`
	Kind   string        `json:"kind"` // "task", "worktree" or "checkout"
	Label  string        `json:"label"`
	Task   string        `json:"task,omitempty"`
	Agent  string        `json:"agent,omitempty"`
	Branch string        `json:"branch,omitempty"`
	Dir    string        `json:"dir,omitempty"`
	Agents []string      `json:"agents,omitempty"` // agent panes working in this folder now
	Diff   *gitdiff.Diff `json:"diff,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// worker is an agent pane and the folder it works in.
type worker struct {
	name, dir string
}

// workers names every agent pane and its folder: the route's name for it if
// the task table has one, else herdr's.
func workers(snap herdr.Snapshot, r *route.Route) []worker {
	byPane := map[string]string{}
	for _, t := range r.Tasks {
		if t.Agent != "" && t.Pane != "" {
			byPane[t.Pane] = t.Agent
		}
	}
	panes := map[string]herdr.Pane{}
	for _, p := range snap.Panes {
		panes[p.ID] = p
	}
	var out []worker
	for _, a := range snap.Agents {
		w := worker{name: byPane[a.PaneID]}
		for _, n := range []*string{a.Name, a.DisplayAgent, a.Agent} {
			if w.name == "" && n != nil {
				w.name = *n
			}
		}
		if w.name == "" {
			w.name = a.PaneID
		}
		p := panes[a.PaneID]
		for _, d := range []*string{p.ForegroundCwd, p.Cwd, a.Cwd} {
			if w.dir == "" && d != nil {
				w.dir = *d
			}
		}
		if w.dir != "" {
			out = append(out, w)
		}
	}
	return out
}

// realPath resolves symlinks (macOS /tmp is /private/tmp) so folders compare.
// A folder that does not exist (yet) resolves through its nearest parent that does.
func realPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func within(dir, root string) bool {
	return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
}

// routeDiffs reads, for each task with a worktree or a branch, what it
// changed against the base branch; then each other worktree of the project
// that an agent works in; then the project checkout itself.
func routeDiffs(ctx context.Context, r *route.Route, ws []worker) []DiffSource {
	project := filepath.Dir(filepath.Dir(r.Dir)) // <project>/.herdsman/<route>
	trees, _ := gitdiff.Worktrees(ctx, project)  // none when the project is not a repository
	roots := []string{realPath(project)}
	for _, t := range trees {
		if !t.Prunable {
			roots = append(roots, realPath(t.Path))
		}
	}
	// Each agent belongs to the deepest work tree holding its folder, so one
	// in a worktree nested inside the checkout is not counted for both.
	agentsIn := map[string][]string{}
	for _, w := range ws {
		dir, best := realPath(w.dir), ""
		for _, root := range roots {
			if within(dir, root) && len(root) > len(best) {
				best = root
			}
		}
		if best != "" && !slices.Contains(agentsIn[best], w.name) {
			agentsIn[best] = append(agentsIn[best], w.name)
		}
	}

	var out []DiffSource
	seen := map[string]bool{}
	add := func(src DiffSource, d *gitdiff.Diff, err error) {
		src.Diff = d
		if err != nil {
			src.Error = err.Error()
		}
		out = append(out, src)
	}
	for _, t := range r.Tasks {
		src := DiffSource{ID: "task:" + t.Task, Kind: "task", Label: t.Task, Task: t.Task, Agent: t.Agent, Branch: t.Branch}
		switch {
		case t.Worktree != "":
			src.Dir = worktreeDir(project, t.Worktree)
			key := realPath(src.Dir)
			if seen[key] {
				continue
			}
			seen[key] = true
			src.Agents = agentsIn[key]
			d, err := gitdiff.Worktree(ctx, src.Dir, "")
			add(src, d, err)
		case t.Branch != "":
			if seen["branch:"+t.Branch] {
				continue
			}
			seen["branch:"+t.Branch] = true
			src.Dir = project
			d, err := gitdiff.Branch(ctx, project, t.Branch, "")
			add(src, d, err)
		}
	}
	main := realPath(project)
	for _, t := range trees {
		key := realPath(t.Path)
		if t.Prunable || key == main || seen[key] || len(agentsIn[key]) == 0 {
			continue
		}
		seen[key] = true
		label := t.Branch
		if label == "" {
			label = filepath.Base(t.Path)
		}
		src := DiffSource{ID: "worktree:" + t.Path, Kind: "worktree", Label: label, Branch: t.Branch, Dir: t.Path, Agents: agentsIn[key]}
		d, err := gitdiff.Worktree(ctx, t.Path, "")
		add(src, d, err)
	}
	if !seen[main] {
		src := DiffSource{ID: "project", Kind: "checkout", Label: "Project checkout", Dir: project, Agents: agentsIn[main]}
		d, err := gitdiff.Worktree(ctx, project, "")
		add(src, d, err)
	}
	return out
}

// worktreeDir resolves a task table's worktree cell: absolute, under the
// home folder, under the project (`<repo>/…`), or relative to the project.
func worktreeDir(project, cell string) string {
	cell = strings.TrimSpace(cell)
	switch {
	case strings.HasPrefix(cell, "~/"):
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, cell[2:])
		}
	case strings.HasPrefix(cell, "<repo>/"):
		return filepath.Join(project, cell[len("<repo>/"):])
	case filepath.IsAbs(cell):
		return filepath.Clean(cell)
	}
	return filepath.Join(project, cell)
}

// routeDiff serves the code diffs of one route. Reading them runs git a few
// times per task, so at most two requests read at once; the ETag lets a
// polling browser get a bodyless 304 while nothing changes.
func (s *Server) routeDiff(w http.ResponseWriter, r *http.Request) {
	rv := findRoute(s.hub.Current().Routes, r.URL.Query().Get("route"))
	if rv == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown route"})
		return
	}
	rt := rv.Route
	select {
	case s.diffSlots <- struct{}{}:
		defer func() { <-s.diffSlots }()
	case <-r.Context().Done():
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	st := s.hub.Current()
	b, err := json.Marshal(map[string]any{"sources": routeDiffs(ctx, rt, workers(st.Snapshot, rt))})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}
