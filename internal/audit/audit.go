// Package audit is the read-only audit of a coordinator session
// (`herdsman transcript-audit`, from the skill's transcript-audit.sh): it
// reads the tool-call records of a Claude Code or Codex transcript, never
// prose, and changes nothing. Exit 0: clean; 1: flagged calls; 2: an audit
// gap (a missing, unsupported, malformed or wrong-session transcript),
// which is never a clean audit.
//
// AUDIT_GRANT_FILE names exact full command lines that an administrative
// grant permits; no other line is exempt. AUDIT_CHANNEL_DIR is the
// installed script folder: only its channel scripts, by exact path, may run
// as scripts. The herdsman binary counts as a script run too, except for
// its channel commands: herdsman message <channel action>, herdsman
// approval-gate and herdsman checkpointer guard.
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Exit codes.
const (
	Clean   = 0
	Flagged = 1
	Gap     = 2
)

// Options are the inputs of an audit.
type Options struct {
	Harness    string   // claude or codex
	Transcript string   // the transcript file, absolute
	Session    string   // the current session id
	Roots      []string // allowed write roots, each absolute and ending in "/"
	GrantFile  string   // AUDIT_GRANT_FILE
	ChannelDir string   // AUDIT_CHANNEL_DIR
}

// Usage is the usage line, printed as a gap.
const Usage = "usage: herdsman transcript-audit claude|codex <transcript> <session-id> [allowed-write-root/ ...]"

type gapError struct{ msg string }

func (g *gapError) Error() string { return g.msg }

func gap(format string, a ...any) error { return &gapError{fmt.Sprintf(format, a...)} }

var sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Run audits the transcript and prints GAP, FLAG and AUDIT lines to w. It
// returns the exit code.
func Run(o Options, w io.Writer) int {
	calls, flagged, err := run(o, w)
	if g, ok := errors.AsType[*gapError](err); ok {
		fmt.Fprintf(w, "GAP %s\n", g.msg)
		return Gap
	}
	if flagged > 0 {
		fmt.Fprintf(w, "AUDIT flagged=%d calls=%d: report each flagged call to the human and stop work\n", flagged, calls)
		return Flagged
	}
	fmt.Fprintf(w, "AUDIT clean calls=%d session=%s\n", calls, o.Session)
	return Clean
}

func run(o Options, w io.Writer) (int, int, error) {
	if o.Harness != "claude" && o.Harness != "codex" {
		return 0, 0, gap("no transcript adapter for harness: %s", o.Harness)
	}
	if !strings.HasPrefix(o.Transcript, "/") {
		return 0, 0, gap("transcript path must be absolute")
	}
	if !readableRegular(o.Transcript) {
		return 0, 0, gap("transcript missing or not a regular file")
	}
	if !sessionRe.MatchString(o.Session) {
		return 0, 0, gap("invalid session id")
	}
	for _, r := range o.Roots {
		if len(r) < 2 || !strings.HasPrefix(r, "/") || !strings.HasSuffix(r, "/") {
			return 0, 0, gap("allowed write root must be absolute and end in /: %s", r)
		}
	}
	if o.ChannelDir != "" && !strings.HasPrefix(o.ChannelDir, "/") {
		return 0, 0, gap("AUDIT_CHANNEL_DIR must be absolute")
	}
	var grants []string
	if o.GrantFile != "" {
		if !readableRegular(o.GrantFile) {
			return 0, 0, gap("grant file unavailable")
		}
		b, _ := os.ReadFile(o.GrantFile)
		grants = strings.Split(string(b), "\n")
	}
	records, err := readRecords(o.Transcript)
	if err != nil {
		return 0, 0, gap("malformed transcript record")
	}
	if err := checkSession(o, records); err != nil {
		return 0, 0, err
	}
	var rows []row
	if o.Harness == "claude" {
		rows = claudeRows(records)
	} else {
		rows = codexRows(records)
	}
	a := auditor{roots: o.Roots, channelDir: o.ChannelDir}
	calls, flagged := 0, 0
	for _, r := range rows {
		if r.tool == "" {
			continue
		}
		calls++
		class, target := "", "-"
		switch r.tool {
		case "!malformed":
			return calls, flagged, gap("malformed tool-call arguments: call %s", r.id)
		case "Write", "Edit", "MultiEdit", "NotebookEdit", "apply_patch", "edit_file", "write_file":
			if !a.inRoot(r.path) {
				class, target = "project-write", ".../"+base(r.path)
			}
		case "Read", "Grep", "Glob", "LS", "NotebookRead", "WebFetch", "WebSearch", "TodoWrite", "ToolSearch", "AskUserQuestion", "Skill",
			"BashOutput", "TaskOutput", "update_plan", "view_image":
		case "Bash", "shell", "exec_command", "local_shell", "shell_command", "container.exec":
			if r.cmd == "" {
				return calls, flagged, gap("malformed tool-call arguments: call %s", r.id)
			}
			c := a.shellClass(r.cmd)
			if c != "" && slices.Contains(grants, r.cmd) {
				c = ""
			}
			if c != "" {
				class, target, _ = strings.Cut(c, " ")
			}
		default:
			if !strings.HasPrefix(r.tool, "mcp__") {
				return calls, flagged, gap("unsupported tool %s (call %s)", r.tool, r.id)
			}
			lower := strings.ToLower(r.tool)
			last := lower[strings.LastIndex(lower, "__")+2:]
			if mcpWrite.MatchString(lower) {
				class = "tracker-or-chat-write"
			} else if !mcpRead.MatchString("_" + last + "_") {
				return calls, flagged, gap("unsupported tool %s (call %s)", r.tool, r.id)
			}
		}
		if class == "gap" {
			// A call whose shell syntax the audit does not parse is never
			// clean: it counts as flagged.
			flagged++
			fmt.Fprintf(w, "GAP call=%s tool=%s unsupported shell syntax: %s\n", r.id, r.tool, target)
		} else if class != "" {
			flagged++
			fmt.Fprintf(w, "FLAG tool=%s call=%s class=%s target=%s\n", r.tool, r.id, class, target)
		}
	}
	return calls, flagged, nil
}

func readableRegular(p string) bool {
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func base(p string) string { return p[strings.LastIndex(p, "/")+1:] }

// readRecords reads every JSON value of the transcript, as jq reads a
// stream.
func readRecords(p string) ([]any, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out []any
	for {
		var v any
		if err := dec.Decode(&v); errors.Is(err, io.EOF) {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

// tostring is jq's tostring: a string as it is, anything else as JSON.
func tostring(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// alt is jq's a // b.
func alt(a, b any) any {
	if a == nil || a == false {
		return b
	}
	return a
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// checkSession requires the records to belong to exactly the named session.
func checkSession(o Options, records []any) error {
	seen := map[string]bool{}
	for _, r := range records {
		m := obj(r)
		if m == nil {
			continue
		}
		if o.Harness == "claude" {
			if v, ok := m["sessionId"]; ok {
				seen[tostring(v)] = true
			}
		} else if m["type"] == "session_meta" {
			seen[tostring(alt(obj(m["payload"])["id"], ""))] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	list := strings.TrimRight(strings.Join(ids, "\n"), "\n")
	if list == "" {
		return gap("no session record in transcript")
	}
	if list != o.Session {
		return gap("transcript is not the named current session")
	}
	return nil
}

// row is one tool call (and, for a patch, one patched path).
type row struct{ tool, id, path, cmd string }

var unsafeChars = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ", "\x1f", " ")

func newRow(tool, id, path, cmd string) row {
	return row{unsafeChars.Replace(tool), unsafeChars.Replace(id), unsafeChars.Replace(path), unsafeChars.Replace(cmd)}
}

func claudeRows(records []any) []row {
	var rows []row
	for _, r := range records {
		m := obj(r)
		if m == nil || m["type"] != "assistant" {
			continue
		}
		content, _ := obj(m["message"])["content"].([]any)
		for _, c := range content {
			item := obj(c)
			if item == nil || item["type"] != "tool_use" {
				continue
			}
			in := obj(alt(item["input"], map[string]any{}))
			cmd, _ := in["command"].(string)
			rows = append(rows, newRow(tostring(alt(item["name"], "?")), tostring(alt(item["id"], "-")),
				tostring(alt(alt(in["file_path"], in["notebook_path"]), "")), cmd))
		}
	}
	return rows
}

var patchRe = regexp.MustCompile(`\*\*\* (?:Add File|Update File|Delete File|Move to): ([^\n]*)`)

func patchRows(name, id string, p any) []row {
	ms := patchRe.FindAllStringSubmatch(tostring(p), -1)
	if len(ms) == 0 {
		return []row{newRow(name, id, "", "")}
	}
	var rows []row
	for _, m := range ms {
		rows = append(rows, newRow(name, id, m[1], ""))
	}
	return rows
}

func joinWords(v any) string {
	a, ok := v.([]any)
	if !ok {
		return tostring(v)
	}
	parts := make([]string, len(a))
	for i, x := range a {
		parts[i] = tostring(x)
	}
	return strings.Join(parts, " ")
}

func codexRows(records []any) []row {
	var rows []row
	for _, r := range records {
		m := obj(r)
		if m == nil || m["type"] != "response_item" {
			continue
		}
		p := obj(m["payload"])
		if p == nil {
			continue
		}
		id := tostring(alt(p["call_id"], "-"))
		name := tostring(alt(p["name"], "?"))
		switch p["type"] {
		case "function_call":
			var args map[string]any
			if v, err := decodeOne(tostring(alt(p["arguments"], "{}"))); err == nil {
				args = obj(v)
			}
			switch {
			case args == nil:
				rows = append(rows, row{"!malformed", id, "", ""})
			case p["name"] == "apply_patch":
				rows = append(rows, patchRows("apply_patch", id, alt(alt(args["input"], args["patch"]), ""))...)
			default:
				cmd := ""
				switch c := args["command"].(type) {
				case []any:
					cmd = joinWords(c)
				case string:
					cmd = c
				default:
					if s, ok := args["cmd"].(string); ok {
						cmd = s
					}
				}
				rows = append(rows, newRow(name, id, "", cmd))
			}
		case "custom_tool_call":
			rows = append(rows, patchRows(name, id, alt(p["input"], ""))...)
		case "local_shell_call":
			rows = append(rows, newRow("local_shell", id, "", joinWords(alt(obj(p["action"])["command"], []any{}))))
		}
	}
	return rows
}

// decodeOne is jq's fromjson: exactly one JSON value.
func decodeOne(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data")
	}
	return v, nil
}

// The command classes, as POSIX extended expressions (leftmost-longest,
// as bash's =~).
const (
	b = `(^|[;&|({[:space:]])`
	e = `([[:space:]]|$|[;&|)])`
	n = `[^;&|]*`
)

var (
	gitRE    = regexp.MustCompilePOSIX(b + `git[[:space:]](` + n + `[[:space:]])?(commit|push)` + e)
	ghAPIRE  = regexp.MustCompilePOSIX(b + `gh[[:space:]]+api[[:space:]](` + n + `[[:space:]])?((-X|--method)[[:space:]=]*(POST|PATCH|PUT|DELETE)|(-f|-F|--field|--raw-field|--input)([[:space:]=]|$)|-[fF][^[:space:]])`)
	ghWRE    = regexp.MustCompilePOSIX(b + `gh[[:space:]](` + n + `[[:space:]])?(create|edit|merge|close|reopen|comment|review|delete|ready|rerun|cancel|set|fork|upload|lock|transfer|develop)` + e)
	curlRE   = regexp.MustCompilePOSIX(b + `(curl|wget)[[:space:]](` + n + `[[:space:]])?(-[a-zA-Z]*X[[:space:]=]*(POST|PATCH|PUT|DELETE)|(--request|--method)[[:space:]=]*(POST|PATCH|PUT|DELETE)|(--data[a-z-]*|--json|--form[a-z-]*|--upload-file|--post-data|--post-file)([[:space:]=]|$)|-[a-zA-Z]*[dFT])`)
	awkRunRE = regexp.MustCompilePOSIX(b + `(awk|gawk|mawk|nawk)[[:space:]]`)
	awkFxRE  = regexp.MustCompilePOSIX(`system[[:space:]]*[(]|getline|print[^;}]*[>|]`)
	writeRE  = regexp.MustCompilePOSIX(b + `(cp|mv|rm|mkdir|rmdir|touch|ln|chmod|chown|tee|install|rsync|truncate|dd|patch|unzip|tar)[[:space:]]`)
	sedIRE   = regexp.MustCompilePOSIX(b + `(sed|perl)[[:space:]](` + n + `[[:space:]])?-[a-zA-Z]*i`)
	runRE    = regexp.MustCompilePOSIX(b + `(npm|npx|pnpm|pnpx|yarn|bun|bunx|deno|node|pip|pip3|uv|uvx|poetry|cargo|go|make|gradle|gradlew|mvn|pytest|jest|vitest|tox|rspec|bundle|composer|mix|dotnet|xcodebuild)` + e)
	testsRE  = regexp.MustCompilePOSIX(b + `([^[:space:]]*/)?[A-Za-z0-9_.-]*tests?[.](sh|py)` + e)
	svcRE    = regexp.MustCompilePOSIX(b + `(docker|docker-compose|podman|nerdctl|colima|kubectl|helm|systemctl|launchctl|service|terraform|pulumi|wrangler|railway|vercel|flyctl|brew[[:space:]]+services)` + e)
	redirRE  = regexp.MustCompilePOSIX(`>(>|[|]|&)?[[:space:]]*([^[:space:];&|()\\]+)`)
	// sysDirRE is a system command folder before a program name: the
	// program is classified by its name (e93bcc1).
	sysDirRE = regexp.MustCompile(`(^|[;&|({[:space:]])(/usr/local/bin|/opt/homebrew/bin|/usr/bin|/usr/sbin|/bin|/sbin)/`)
	sysPath  = regexp.MustCompile(`^(/usr/local/bin|/opt/homebrew/bin|/usr/bin|/usr/sbin|/bin|/sbin)/([^/]+)$`)
	mcpWrite = regexp.MustCompile(`(send|create|update|delete|post|comment|write|add|edit|merge|upload|remove|move|schedule|react|approve|publish|set_|_set|assign|archive|attach|start|stop)`)
	mcpRead  = regexp.MustCompile(`_(get|list|read|search|query|fetch|view|find|describe|show|count|lookup|retrieve|status|info)_`)
	leadSep  = regexp.MustCompile(`^[;&|({[:space:]]*`)
)

type auditor struct {
	roots      []string
	channelDir string
}

// program is the program word of a match: the first word after the
// separator the pattern starts with, never an argument.
func program(match string) string {
	// The trailing "" keeps an all-separator match (none of the patterns
	// gives one) from indexing past the end.
	return append(strings.Fields(leadSep.ReplaceAllString(match, "")), "")[0]
}

// shellClass returns "class program" for a command, or "".
func (a auditor) shellClass(raw string) string {
	// Syntax the command splitter does not parse is a gap, before any
	// other class: no class is reliable then (b0a9b04).
	cmds, why := splitCommands(raw)
	if why != "" {
		return "gap " + why
	}
	var m string
	class := ""
	cmd := normalized(raw)
	switch {
	case gitRE.MatchString(cmd):
		class, m = "git-write", gitRE.FindString(cmd)
	case ghAPIRE.MatchString(cmd):
		class, m = "outward-write", ghAPIRE.FindString(cmd)
	case ghWRE.MatchString(cmd):
		class, m = "outward-write", ghWRE.FindString(cmd)
	case curlRE.MatchString(cmd):
		class, m = "outward-write", curlRE.FindString(cmd)
	case runRE.MatchString(cmd):
		class, m = "test-or-package", runRE.FindString(cmd)
	case testsRE.MatchString(cmd):
		class, m = "test-or-package", testsRE.FindString(cmd)
	case svcRE.MatchString(cmd):
		class, m = "service-or-container", svcRE.FindString(cmd)
	case awkRunRE.MatchString(cmd) && awkFxRE.MatchString(cmd):
		return "script-run awk"
	default:
		if w := a.shellWrite(raw); w != "" {
			return w
		}
		return a.scriptRun(cmds)
	}
	return class + " " + program(m)
}

func stripQuotes(s string) string { return strings.NewReplacer(`"`, "", "'", "").Replace(s) }

// normalized is a command without quotes and with each system command
// folder dropped before a program, so /usr/bin/curl reads as curl
// (e93bcc1): quotes never hide a program name.
func normalized(s string) string { return sysDirRE.ReplaceAllString(stripQuotes(s), "$1") }

// shellWrite returns "project-write <target>" for a redirect outside the
// allowed roots or a file mutation, or "".
func (a auditor) shellWrite(cmd string) string {
	rest := stripQuotes(cmd)
	for {
		loc := redirRE.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		op, t := "", rest[loc[4]:loc[5]]
		if loc[2] >= 0 {
			op = rest[loc[2]:loc[3]]
		}
		rest = rest[loc[1]:]
		// >, >>, >| and >&: a >& to a descriptor number or - copies a
		// stream; any other target is a file (e93bcc1).
		if op == "&" && (t == "-" || (len(t) <= 2 && strings.Trim(t, "0123456789") == "")) {
			continue
		}
		if t == "/dev/null" || t == "/dev/stdout" || t == "/dev/stderr" || t == "/dev/tty" {
			continue
		}
		if !a.inRoot(t) {
			return "project-write .../" + base(t)
		}
	}
	norm := normalized(cmd)
	if m := writeRE.FindString(norm); m != "" {
		return "project-write " + program(m)
	}
	if m := sedIRE.FindString(norm); m != "" {
		return "project-write " + program(m)
	}
	return ""
}

func (a auditor) inRoot(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	if strings.Contains(p, "/../") || strings.Contains(p, "/./") || strings.HasSuffix(p, "/..") || strings.HasSuffix(p, "/.") {
		return false
	}
	for _, r := range a.roots {
		if strings.HasPrefix(p, r) {
			return true
		}
	}
	return false
}

// channelScripts are the channel scripts that may run by their exact path
// under the installed folder.
var channelScripts = []string{"message.sh", "message-register.sh", "message-send.sh", "message-read.sh", "message-ack.sh",
	"message-status.sh", "message-wait.sh", "message-authority.sh", "publish-leak-gate.sh"}

func (a auditor) channelScript(p string) bool {
	if a.channelDir == "" {
		return false
	}
	for _, s := range channelScripts {
		if p == strings.TrimSuffix(a.channelDir, "/")+"/"+s {
			return true
		}
	}
	return false
}

// channelActions are the herdsman message actions the skill text names:
// the channel scripts' actions and the loops (courier, card-watch,
// resume-goal) that replace the channel's watcher scripts.
var channelActions = []string{"status", "wait", "read", "release", "ack", "send", "hold", "unhold", "register", "authority", "leak-gate", "courier", "card-watch", "resume-goal"}

// channelCommand reports whether words (from the herdsman program on) run
// a channel command of the binary.
func channelCommand(words []string) bool {
	if len(words) < 2 {
		return false
	}
	switch words[1] {
	case "approval-gate":
		return true
	case "checkpointer":
		return len(words) > 2 && words[2] == "guard"
	case "message":
		return len(words) > 2 && slices.Contains(channelActions, words[2])
	}
	return false
}

var wrappers = []string{"env", "exec", "nohup", "time", "command", "builtin", "sudo", "nice", "timeout", "doas", "stdbuf", "ionice", "chroot", "setsid"}

var interpreters = regexp.MustCompile(`^(bash|sh|zsh|dash|ksh|fish|python|python2|python3|python3\..*|perl|ruby|node|deno|bun|php|lua|osascript|tclsh|awk|gawk|mawk)$`)

// scriptRun returns "script-run <program>" for the first script or
// interpreter run that is not allowed, or "". Command text cannot show what
// a script does, so every script or interpreter run is not clean unless it
// starts a channel script by its exact path (or is a channel command of the
// herdsman binary).
func (a auditor) scriptRun(cmds [][]string) string {
	for _, words := range cmds {
		i, wrapped, skip := 0, "", false
	walk:
		for i < len(words) {
			w := words[i]
			if m := sysPath.FindStringSubmatch(w); m != nil {
				w = m[2]
			}
			switch {
			case slices.Contains(wrappers, w):
				wrapped = w
				i++
			case slices.Contains(reserved, w):
				// Reserved words that open or close a compound command
				// start nothing.
				if wrapped != "" {
					break walk
				}
				i++
			case w == "[" || w == "[[" || w == "for" || w == "case" || w == "select":
				// A test or a for, case or select header runs no program.
				skip = wrapped == ""
				break walk
			case strings.Contains(w, "="):
				i++
			case w != "" && (w[0] == '-' || (w[0] >= '0' && w[0] <= '9')):
				// A wrapper with an option or a count hides what it starts.
				if wrapped != "" {
					return "script-run " + wrapped
				}
				break walk
			default:
				break walk
			}
		}
		if skip || i >= len(words) {
			continue
		}
		first := words[i]
		// A program from a system command folder runs by its name; any
		// other path is a script run (e93bcc1).
		prog := first
		if m := sysPath.FindStringSubmatch(first); m != nil {
			prog = m[2]
		}
		// A command word with a blank, shell punctuation, an expansion or a
		// pattern is no known program: a quoted name, a variable or a
		// substitution, whose run cannot be read from the text (b0a9b04).
		if odd.MatchString(prog) {
			if a.channelScript(first) {
				continue
			}
			return "script-run " + base(prog)
		}
		joined := " " + strings.Join(words, " ") + " "
		switch {
		case interpreters.MatchString(prog):
			script := ""
			for _, x := range words[i+1:] {
				if !strings.HasPrefix(x, "-") {
					script = x
					break
				}
			}
			if (prog == "awk" || prog == "gawk" || prog == "mawk") && !strings.Contains(joined, " -f ") {
				continue
			}
			if script != "" && a.channelScript(script) && !strings.Contains(joined, " -c ") {
				continue
			}
			return "script-run " + prog
		case prog == "eval" || prog == "source" || prog == "." || prog == "xargs":
			return "script-run " + prog
		case strings.Contains(prog, "/") || strings.HasSuffix(prog, ".sh") || strings.HasSuffix(prog, ".py") ||
			strings.HasSuffix(prog, ".pl") || strings.HasSuffix(prog, ".rb") || strings.HasSuffix(prog, ".js"):
			if a.channelScript(first) {
				continue
			}
			return "script-run " + base(prog)
		case prog == "herdsman":
			if channelCommand(words[i:]) {
				continue
			}
			return "script-run herdsman"
		}
	}
	return ""
}

var (
	reserved = []string{"!", "{", "}", "if", "then", "else", "elif", "do", "while", "until", "fi", "done", "esac"}
	odd      = regexp.MustCompile(`[^A-Za-z0-9._+@%:,=/~-]`)
)
