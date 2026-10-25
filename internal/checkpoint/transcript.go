package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// Session is what herdr reports about the target, as the script reads it
// from `herdr agent get` with jq: Agent is `.agent // empty`, SID is
// `.agent_session.value` (jq -r: "null" when missing, "" when the call
// printed nothing), Kind is `.agent_session.kind // "id"`.
type Session struct {
	Agent string
	SID   string
	Kind  string
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
		if sid != "" && isFile(named) {
			return Choice{File: named, Why: "the Codex rollout of the session id that Herdr reports ('" + sid + "')"}
		}
		return Choice{Why: "no Codex rollout for session id '" + sid + "' under " + dir}
	}
	named := newest(globLevels(e.Home+"/.claude/projects", []string{"*", escape(sid) + ".jsonl"}))
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
	case latest == "":
		return Choice{Why: "no file for session id '" + sid + "' and no .jsonl in " + dir}
	default:
		return Choice{Why: "no file for session id '" + sid + "'",
			Hint: "the newest transcript of " + dir + " is " + latest + "; if the id is stale, set TRANSCRIPT=<path>"}
	}
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

// tailLines is `tail -n n`: the last n lines of the file, the last one
// counted even without a final newline.
func tailLines(p string, n int) ([]byte, error) {
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
	const chunk = 64 << 10
	var buf []byte
	pos := size
	for pos > 0 {
		step := min(int64(chunk), pos)
		pos -= step
		b := make([]byte, step)
		if _, err := f.ReadAt(b, pos); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		buf = append(b, buf...)
		body := buf
		if pos+int64(len(buf)) == size && len(body) > 0 && body[len(body)-1] == '\n' {
			body = body[:len(body)-1]
		}
		if bytes.Count(body, []byte{'\n'}) >= n {
			break
		}
	}
	body := buf
	trail := len(body) > 0 && body[len(body)-1] == '\n'
	if trail {
		body = body[:len(body)-1]
	}
	idx := len(body)
	for range n {
		i := bytes.LastIndexByte(body[:idx], '\n')
		if i < 0 {
			idx = -1
			break
		}
		idx = i
	}
	start := idx + 1
	return buf[start:], nil
}

// ContextTokens is the script's context_tokens: of the last 400 lines of
// the transcript, the context of the last record that carries usage, as
// jq prints it ("" for none). Claude Code: input + cache creation + cache
// read tokens of the last main-thread assistant turn. Codex: the input
// tokens of the last token_count event with usage. The records are read
// as jq reads them: a value it cannot read or a field of a wrong type ends
// the read, and what was found before counts.
func ContextTokens(p string) string {
	b, err := tailLines(p, 400)
	if err != nil {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	last := ""
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			return last
		}
		out, ok, fine := record(v)
		if !fine {
			return last
		}
		if ok {
			last = out
		}
	}
}

// record applies the script's jq filter to one value: the output, whether
// there is one, and false where jq stops with an error.
func record(v any) (string, bool, bool) {
	if v == nil {
		return "", false, true
	}
	m, isObj := v.(map[string]any)
	if !isObj {
		return "", false, false
	}
	if m["type"] == "assistant" {
		msg, fine := index(m["message"])
		if !fine {
			return "", false, false
		}
		usage := msg["usage"]
		if truthy(usage) && !truthy(m["isSidechain"]) {
			u, fine := usage.(map[string]any)
			if !fine {
				return "", false, false
			}
			sum, fine := add(u["input_tokens"], alt(u["cache_creation_input_tokens"]))
			if fine {
				sum, fine = add(sum, alt(u["cache_read_input_tokens"]))
			}
			if !fine {
				return "", false, false
			}
			return jqText(sum), true, true
		}
	}
	if m["type"] != "event_msg" {
		return "", false, true
	}
	payload, fine := index(m["payload"])
	if !fine {
		return "", false, false
	}
	if payload["type"] != "token_count" {
		return "", false, true
	}
	info, fine := index(payload["info"])
	if !fine {
		return "", false, false
	}
	ltu := info["last_token_usage"]
	if !truthy(ltu) {
		return "", false, true
	}
	l, fine := index(ltu)
	if !fine {
		return "", false, false
	}
	return jqText(l["input_tokens"]), true, true
}

// index is jq's .field on a value: an object, or null (every field null);
// any other value is an error.
func index(v any) (map[string]any, bool) {
	if v == nil {
		return map[string]any{}, true
	}
	m, ok := v.(map[string]any)
	return m, ok
}

func truthy(v any) bool { return v != nil && v != false }

// alt is jq's (x // 0).
func alt(v any) any {
	if !truthy(v) {
		return json.Number("0")
	}
	return v
}

// add is jq's + on null and numbers; other types end the read.
func add(a, b any) (any, bool) {
	// b is never null: it is always (x // 0).
	if a == nil {
		return b, isNum(b)
	}
	x, ok1 := num(a)
	y, ok2 := num(b)
	if !ok1 || !ok2 {
		return nil, false
	}
	return x + y, true
}

func isNum(v any) bool { _, ok := num(v); return ok }

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil || errors.Is(err, strconv.ErrRange)
	case float64:
		return n, true
	}
	return 0, false
}

// jqText prints a value as jq -r does: a string raw, a number literal as
// written, a computed number as an integer when it is one, else JSON.
func jqText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e17 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', 17, 64)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
