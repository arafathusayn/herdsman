// Package route finds herdsman route folders and reads their 00-route.md:
// the task table that maps agents to tasks, the checklist and the run state.
package route

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// RouteFile is the file every route folder holds.
const RouteFile = "00-route.md"

// maxFiles bounds the file list of one route; see listFiles for what it keeps.
const maxFiles = 200

type Task struct {
	Task     string `json:"task"`
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"`
	Agent    string `json:"agent"`
	Pane     string `json:"pane"`
	Contract string `json:"contract"`
}

type CheckItem struct {
	Done bool   `json:"done"`
	ID   string `json:"id"`
	Text string `json:"text"`
}

type LogLine struct {
	Time string `json:"time"`
	Text string `json:"text"`
}

type File struct {
	Path     string    `json:"path"` // relative to the route folder
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type Route struct {
	Dir              string `json:"dir"`
	Name             string `json:"name"`
	Title            string `json:"title"`
	OrchestratorPane string `json:"orchestrator_pane"`
	// AdjutantPane is the optional adjutant's pane: the user's front, which
	// relays to the orchestrator through herdsman messages.
	AdjutantPane string      `json:"adjutant_pane,omitempty"`
	Tasks        []Task      `json:"tasks"`
	Checklist    []CheckItem `json:"checklist"`
	RunState     []LogLine   `json:"run_state"`
	Files        []File      `json:"files"`
	Modified     time.Time   `json:"modified"`
}

// Find returns the route folders under the .herdsman folder of each start
// directory or its nearest ancestor that has one, newest first.
func Find(starts []string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, s := range starts {
		root := nearestHerdsman(s)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			d := filepath.Join(root, e.Name())
			if e.IsDir() && fileExists(filepath.Join(d, RouteFile)) {
				dirs = append(dirs, d)
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return mtime(dirs[i]).After(mtime(dirs[j])) })
	return dirs
}

func nearestHerdsman(dir string) string {
	for d := filepath.Clean(dir); d != "" && d != "/" && d != "."; d = filepath.Dir(d) {
		if st, err := os.Stat(filepath.Join(d, ".herdsman")); err == nil && st.IsDir() {
			return filepath.Join(d, ".herdsman")
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func mtime(dir string) time.Time {
	st, err := os.Stat(filepath.Join(dir, RouteFile))
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

var (
	orchestratorPane = regexp.MustCompile(`(?i)orchestrator:.*?\bpane ([A-Za-z0-9]+:[A-Za-z0-9]+)`)
	adjutantPane     = regexp.MustCompile(`(?i)^\W*adjutant:.*?\bpane ([A-Za-z0-9]+:[A-Za-z0-9]+)`)
	checkLine        = regexp.MustCompile(`^- \[( |x|X)\] (?:(C[0-9]+\.[0-9]+)\s+)?(.*)$`)
	runLine          = regexp.MustCompile(`^- ([0-9]{2}:[0-9]{2}) (.*)$`)
	// A cell can carry notes after its value: "wS:p8 (tab wS:t4 ...)".
	agentToken = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}\b`)
	paneToken  = regexp.MustCompile(`\b[A-Za-z0-9]+:[A-Za-z0-9]+\b`)
)

// Load reads a route folder.
func Load(dir string) (*Route, error) {
	f, err := os.Open(filepath.Join(dir, RouteFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, _ := f.Stat()
	r := &Route{Dir: dir, Name: filepath.Base(dir), Modified: st.ModTime()}
	section := ""
	var header []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# ") && r.Title == "":
			r.Title = strings.TrimSpace(line[2:])
			continue
		case strings.HasPrefix(line, "## "):
			section = strings.ToLower(strings.TrimSpace(line[3:]))
			header = nil
			continue
		}
		if r.AdjutantPane == "" {
			if m := adjutantPane.FindStringSubmatch(line); m != nil {
				r.AdjutantPane = m[1]
				continue
			}
		}
		if r.OrchestratorPane == "" {
			if m := orchestratorPane.FindStringSubmatch(line); m != nil {
				r.OrchestratorPane = m[1]
			}
		}
		switch section {
		case "task table":
			cells := tableCells(line)
			if cells == nil || isSeparator(cells) {
				continue
			}
			if header == nil {
				header = lower(cells)
				continue
			}
			r.Tasks = append(r.Tasks, taskFrom(header, cells))
		case "checklist":
			if m := checkLine.FindStringSubmatch(line); m != nil {
				r.Checklist = append(r.Checklist, CheckItem{Done: m[1] != " ", ID: m[2], Text: m[3]})
			}
		case "run state":
			if m := runLine.FindStringSubmatch(line); m != nil {
				r.RunState = append(r.RunState, LogLine{Time: m[1], Text: m[2]})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	r.Files = listFiles(dir)
	return r, nil
}

func tableCells(line string) []string {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "|") || !strings.HasSuffix(t, "|") || len(t) < 2 {
		return nil
	}
	parts := strings.Split(t[1:len(t)-1], "|")
	for i := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(parts[i]), "`")
	}
	return parts
}

func isSeparator(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}

func lower(cells []string) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = strings.ToLower(c)
	}
	return out
}

func taskFrom(header, cells []string) Task {
	get := func(name string) string {
		for i, h := range header {
			if h == name && i < len(cells) {
				if cells[i] == "-" {
					return ""
				}
				return cells[i]
			}
		}
		return ""
	}
	return Task{
		Task: get("task"), Branch: get("branch"), Worktree: get("worktree"),
		Agent: agentToken.FindString(get("agent")), Pane: paneToken.FindString(get("pane")),
		Contract: get("contract"),
	}
}

// listFiles lists the readable text files of a route folder, skipping large
// probe output and anything outside the folder.
func listFiles(dir string) []File {
	var out []File
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, File{Path: filepath.ToSlash(rel), Size: info.Size(), Modified: info.ModTime()})
		return nil
	})
	// The route's own files (top level, and one folder down such as reports/)
	// always show; the cap drops only the oldest of the deeper files, which
	// probes can produce by the hundred.
	shallow := func(f File) bool { return strings.Count(f.Path, "/") <= 1 }
	sort.SliceStable(out, func(i, j int) bool {
		if si, sj := shallow(out[i]), shallow(out[j]); si != sj {
			return si
		}
		return out[i].Modified.After(out[j].Modified)
	})
	if len(out) > maxFiles {
		keep := maxFiles
		for keep < len(out) && shallow(out[keep]) {
			keep++
		}
		out = out[:keep]
	}
	return out
}

// ReadFile reads a file inside the route folder; paths that leave it are refused.
func ReadFile(dir, rel string) ([]byte, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path outside the route folder")
	}
	full := filepath.Join(dir, clean)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, err
	}
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	if resolved != base && !strings.HasPrefix(resolved, base+string(filepath.Separator)) {
		return nil, fmt.Errorf("path outside the route folder")
	}
	return os.ReadFile(resolved)
}

// AppendLog appends one run-state line "- HH:MM <text>" to the route file,
// the same line format the orchestrator writes.
func AppendLog(dir, text string, now time.Time) error {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return fmt.Errorf("empty log text")
	}
	f, err := os.OpenFile(filepath.Join(dir, RouteFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "- %s %s\n", now.Format("15:04"), text)
	return err
}
