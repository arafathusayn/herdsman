package message

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// LeakPattern is publish-leak-gate.sh's default pattern (POSIX ERE, read
// case-insensitively): private paths, home references, tool and model names,
// agent names, assignments and absolute paths.
const LeakPattern = `/Users/|/home/|/private/|/tmp/|/var/|/opt/|/Volumes/|~/|\$HOME|\$\{HOME\}|herdsman|herdr|run-check|claude|codex|gpt[- ]|opus|sonnet|(^|[^[:alnum:]_])(im|rv|int)-[[:alnum:]_-]+|[[:alnum:]_]+=|(^|[[:space:](])/[[:alnum:]_.-]+/`

// LeakGate is publish-leak-gate.sh <file> [exact-required-heading ...]: exit
// 0 when no line matches the pattern (LEAK_PATTERN adds one; an exact line
// of LEAK_ALLOW_FILE is allowed) and the fences and required sections are
// complete; 1 on a leak or a structure fault; 2 on an input error.
func LeakGate(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: herdsman message leak-gate <file> [required heading ...]")
		return 2
	}
	input := args[0]
	if st, err := os.Lstat(input); err != nil || !st.Mode().IsRegular() {
		fmt.Fprintln(stderr, "cannot read regular input")
		return 2
	}
	b, err := os.ReadFile(input)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read regular input")
		return 2
	}
	text := string(b)
	lines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	scan := func(pattern string) int {
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			fmt.Fprintln(stderr, "pattern or input error")
			return 2
		}
		for i, l := range lines {
			if !re.MatchString(l) {
				continue
			}
			if allow := getenv("LEAK_ALLOW_FILE"); allow != "" {
				st, err := os.Lstat(allow)
				ab, rerr := os.ReadFile(allow)
				if err != nil || !st.Mode().IsRegular() || rerr != nil {
					fmt.Fprintln(stderr, "invalid exact-line allow list")
					return 2
				}
				if exactLine(string(ab), l) {
					continue
				}
			}
			fmt.Fprintf(stdout, "LEAK: preserve content while removing private wrappers:\n%d:%s\n", i+1, l)
			return 1
		}
		return 0
	}
	if rc := scan(LeakPattern); rc != 0 {
		return rc
	}
	if p := getenv("LEAK_PATTERN"); p != "" {
		if rc := scan(p); rc != 0 {
			return rc
		}
	}
	if !structureOK(lines, "") {
		fmt.Fprintln(stderr, "EMPTY, incomplete, or unreadable code fence: restore content")
		return 1
	}
	for _, h := range args[1:] {
		if !structureOK(lines, h) {
			fmt.Fprintf(stderr, "missing or empty required section: %s\n", h)
			return 1
		}
	}
	fmt.Fprintln(stdout, "pattern and structure checks passed; manual policy/read-back review required")
	return 0
}

func exactLine(list, l string) bool {
	for _, x := range strings.Split(list, "\n") {
		if x == l {
			return true
		}
	}
	return false
}

var blankRe = regexp.MustCompile(`^[ \t\r\f\v]*$`)

// structureOK is the gate's awk check: every fence closed and not empty;
// with wanted, that heading present with text in its section (its child
// headings included; a heading of the same or a higher level ends it).
func structureOK(lines []string, wanted string) bool {
	opened, bad, active, seen, body, content := false, false, false, false, false, false
	var mark byte
	width, wantedLevel := 0, 0
	for _, l := range lines {
		s := strings.TrimLeft(l, " \t\r\f\v")
		if strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~") {
			c := s[0]
			n := 0
			for n < len(s) && s[n] == c {
				n++
			}
			if !opened {
				opened, mark, width, content = true, c, n, false
				continue
			}
			if c == mark && n >= width && blankRe.MatchString(s[n:]) {
				if !content {
					bad = true
				}
				opened = false
				continue
			}
		}
		if !opened && strings.HasPrefix(l, "#") {
			level := strings.IndexFunc(l, func(r rune) bool { return r != '#' })
			if level < 1 {
				level = len(l)
			}
			if active && level > wantedLevel {
				continue
			}
			active = l == wanted
			if active {
				seen, wantedLevel = true, level
			}
			continue
		}
		if strings.TrimSpace(l) != "" {
			if opened {
				content = true
			}
			if active {
				body = true
			}
		}
	}
	return !(opened || bad || (wanted != "" && !(seen && body)))
}
