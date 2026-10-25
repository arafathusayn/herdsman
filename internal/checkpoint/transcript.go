package checkpoint

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/shglob"
)

// Choice is the transcript the checkpointer reads, as the script's
// transcript function sets TRANSCRIPT_FILE, TRANSCRIPT_WHY and
// TRANSCRIPT_HINT.
type Choice struct {
	File string // "" when there is none
	Why  string
	Hint string
}

// Session is what herdr reports about the target: the harness, the
// session id ("" when herdr reports none) and its kind ("id" or "path").
// Missing names why there is no id.
type Session struct {
	Agent   string
	SID     string
	Kind    string
	Missing string
}

// noSession is the why line when herdr reports no session id.
func noSession(s Session) string {
	return "herdr reports no session id for the target (" + s.Missing + ")"
}

// Env is the part of the environment the choice reads.
type Env struct {
	Transcript string // TRANSCRIPT ("" is unset)
	Home       string // HOME
	CodexHome  string // CODEX_HOME ("" is unset)
	PWD        string // the working folder
}

// Choose picks the transcript as the script does. The file of the session
// id herdr reports is always used when it exists; a newer file of the same
// folder is a hint only. TRANSCRIPT overrides the choice. A Codex target
// is read from its rollout file.
func Choose(s Session, e Env) Choice {
	if e.Transcript != "" {
		if isFile(e.Transcript) {
			return Choice{File: e.Transcript, Why: "TRANSCRIPT is set; the file is used whatever the session id says"}
		}
		return Choice{Why: "TRANSCRIPT is set but the file does not exist: " + e.Transcript}
	}
	sid := s.SID
	if s.Agent == "codex" && sid == "" {
		return Choice{Why: noSession(s)}
	}
	if s.Agent == "codex" {
		base := e.CodexHome
		if base == "" {
			base = e.Home + "/.codex"
		}
		dir := base + "/sessions"
		var named string
		if s.Kind == "path" {
			named = sid
		} else {
			pat := "rollout-*-" + escape(sid) + ".jsonl"
			named = newest(append(globLevels(dir, []string{"*", "*", "*", pat}), globLevels(dir, []string{pat})...))
		}
		if isFile(named) {
			return Choice{File: named, Why: "the Codex rollout of the session id that Herdr reports ('" + sid + "')"}
		}
		return Choice{Why: "no Codex rollout for session id '" + sid + "' under " + dir}
	}
	var named string
	if sid != "" {
		named = newest(globLevels(e.Home+"/.claude/projects", []string{"*", escape(sid) + ".jsonl"}))
	}
	var dir string
	if named != "" {
		dir = filepath.Dir(named)
	} else {
		dir = e.Home + "/.claude/projects/" + Derived(e.PWD)
	}
	latest := newest(globLevels(dir, []string{"*.jsonl"}))
	switch {
	case named != "":
		c := Choice{File: named, Why: "the file of the session id that Herdr reports ('" + sid + "')"}
		if newer(latest, named) {
			c.Hint = "a newer transcript exists: " + latest + "; if the id is stale, set TRANSCRIPT=<path>"
		}
		return c
	}
	why := "no file for session id '" + sid + "'"
	if sid == "" {
		why = noSession(s)
	}
	if latest == "" {
		return Choice{Why: why + " and no .jsonl in " + dir}
	}
	return Choice{Why: why, Hint: "the newest transcript of " + dir + " is " + latest + "; if the id is stale, set TRANSCRIPT=<path>"}
}

// Derived is the project folder name Claude Code gives a working folder, as
// the script's sed 's/[^A-Za-z0-9]/-/g': every other character becomes "-".
func Derived(pwd string) string {
	var b strings.Builder
	for _, r := range pwd {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// isFile is test -f: a regular file, after links.
func isFile(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// escape quotes a value for a glob pattern, as "$sid" inside a bash glob is
// literal.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// globLevels expands "$dir"/p1/p2/... as bash does: the folder literal,
// each level a pattern, a level before the last matching folders only. A
// level may hold a "/" of a literal value (a session id with a slash).
func globLevels(dir string, levels []string) []string {
	var parts []string
	for _, l := range levels {
		parts = append(parts, strings.Split(l, "/")...)
	}
	cur := []string{dir}
	for i, p := range parts {
		var next []string
		for _, d := range cur {
			found, _ := shglob.Glob(d, p)
			for _, f := range found {
				if i < len(parts)-1 {
					if st, err := os.Stat(f); err != nil || !st.IsDir() {
						continue
					}
				}
				next = append(next, f)
			}
		}
		cur = next
	}
	return cur
}

// newest is `ls -t ... | head -1`: the latest modification time first, ties
// in name order; "" for none.
func newest(files []string) string {
	type entry struct {
		name string
		ns   int64
	}
	var es []entry
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		es = append(es, entry{f, st.ModTime().UnixNano()})
	}
	if len(es) == 0 {
		return ""
	}
	slices.SortStableFunc(es, func(a, b entry) int {
		if a.ns != b.ns {
			if a.ns > b.ns {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	return es[0].name
}

// newer is test a -nt b.
func newer(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return true
	}
	return sa.ModTime().After(sb.ModTime())
}

// tailLines is `tail -n n` within the last limit bytes: the last n lines of
// the file, the last one counted even without a final newline. One read of
// at most limit bytes; a line cut by that bound is dropped, so a huge record
// never fills memory.
func tailLines(p string, n int, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	start := max(size-limit, 0)
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if start > 0 {
		// The window starts inside a line unless the byte before it ends one.
		prev := make([]byte, 1)
		if _, err := f.ReadAt(prev, start-1); err != nil || prev[0] != '\n' {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				return nil, nil
			}
			buf = buf[i+1:]
		}
	}
	body := bytes.TrimSuffix(buf, []byte{'\n'})
	idx := len(body)
	for range n {
		i := bytes.LastIndexByte(body[:idx], '\n')
		if i < 0 {
			return buf, nil
		}
		idx = i
	}
	return buf[idx+1:], nil
}
