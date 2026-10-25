package audit

import (
	"regexp"
	"slices"
	"strings"
)

// The B3-002 extra rules (e93bcc1) run only on a command main leaves clean
// and only add flags. They read the command as simple commands with their
// quote context: the program word of each command (after ;, &&, ||, |, &,
// a newline, ( ) { } and $( or a backquote) has its quotes removed and a
// system command folder read as the program's name; arguments keep their
// literal boundaries, so text an echo or printf prints, or a here-document
// body, never reads as a program.
var (
	curlExtraRE = regexp.MustCompilePOSIX(`^(curl|wget)[[:space:]](` + n + `[[:space:]])?(-[a-zA-Z]*X[[:space:]=]*(POST|PATCH|PUT|DELETE)|(--request|--method)[[:space:]=]*(POST|PATCH|PUT|DELETE)|(--data[a-z-]*|--json|--form[a-z-]*|--upload-file|--post-data|--post-file)([[:space:]=]|$)|-[a-zA-Z]*[dFT])`)
	sysPath     = regexp.MustCompile(`^(/usr/local/bin|/opt/homebrew/bin|/usr/bin|/usr/sbin|/bin|/sbin)/[^/]+$`)
	reserved    = []string{"!", "{", "}", "if", "then", "else", "elif", "do", "while", "until", "fi", "done", "esac", "time"}
	headers     = []string{"case", "for", "select", "[", "[["}
	// argSep stands between arguments in a command line built for the
	// patterns: never a blank, so an argument with blanks stays one.
	argSep = '\x1f'
)

// redirect is a >| or >& operator with its target.
type redirect struct{ op, target string }

// lexCommands splits s into simple commands (their words after quote
// removal) and the >| and >& redirects, reading quotes and escapes.
func lexCommands(s string) ([][]string, []redirect) {
	var cmds [][]string
	var reds []redirect
	var cmd []string
	var w strings.Builder
	inword, sq, dq := false, false, false
	var heredocs []string // delimiters whose bodies start after this line
	flush := func() {
		if inword {
			cmd = append(cmd, w.String())
		}
		w.Reset()
		inword = false
	}
	end := func() {
		flush()
		if len(cmd) > 0 {
			cmds = append(cmds, cmd)
		}
		cmd = nil
	}
	at := func(i int) byte {
		if i < len(s) {
			return s[i]
		}
		return 0
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case sq:
			if c == '\'' {
				sq = false
			} else {
				w.WriteByte(c)
			}
		case c == '\\':
			i++
			if i < len(s) {
				w.WriteByte(s[i])
				inword = true
			}
		case c == '$' && at(i+1) == '(' && at(i+2) != '(':
			// A command substitution holds commands of its own.
			end()
			dq = false
			i++
		case c == '`':
			end()
			dq = false
		case dq:
			if c == '"' {
				dq = false
			} else {
				w.WriteByte(c)
			}
		case c == '\'':
			sq, inword = true, true
		case c == '"':
			dq, inword = true, true
		case c == ' ' || c == '\t':
			flush()
		case c == '\n':
			end()
			// A here-document body is text, never commands: skip its
			// lines up to the delimiter line.
			for _, d := range heredocs {
				for i+1 < len(s) {
					j := strings.IndexByte(s[i+1:], '\n')
					line := s[i+1:]
					if j >= 0 {
						line = s[i+1 : i+1+j]
					}
					if j < 0 {
						i = len(s) - 1
					} else {
						i += j + 1
					}
					if strings.TrimLeft(line, "\t") == d {
						break
					}
				}
			}
			heredocs = nil
		case c == ';' || c == '&' || c == '|' || c == '(' || c == ')':
			end()
		case c == '>' || c == '<':
			// A descriptor number right before the operator is not a word.
			if inword && allDigits(w.String()) {
				w.Reset()
				inword = false
			}
			flush()
			op := string(c)
			if d := at(i + 1); d == '>' || d == '|' || d == '&' || d == '<' {
				op += string(d)
				i++
				if op == "<<" && at(i+1) == '<' {
					op += "<"
					i++
				}
			}
			for at(i+1) == ' ' || at(i+1) == '\t' {
				i++
			}
			j := i + 1
			for j < len(s) && strings.IndexByte(" \t\n;&|()<>", s[j]) < 0 {
				j++
			}
			target := strings.NewReplacer(`"`, "", "'", "").Replace(s[i+1 : j])
			i = j - 1
			switch op {
			case ">|", ">&":
				reds = append(reds, redirect{op, target})
			case "<<":
				heredocs = append(heredocs, strings.TrimPrefix(target, "-"))
			}
		default:
			w.WriteByte(c)
			inword = true
		}
	}
	end()
	return cmds, reds
}

// extraClass applies the B3-002 rules to a command main leaves clean.
func (a auditor) extraClass(raw string) string {
	cmds, reds := lexCommands(raw)
	for _, words := range cmds {
		if c := a.extraCommand(words); c != "" {
			return c
		}
	}
	// >| and >&file write a file; >&N, >&N- and >&- copy or move a stream.
	for _, r := range reds {
		t := r.target
		if r.op == ">&" && (t == "-" || allDigits(t) || allDigits(strings.TrimSuffix(t, "-"))) {
			continue
		}
		if t == "/dev/null" || t == "/dev/stdout" || t == "/dev/stderr" || t == "/dev/tty" {
			continue
		}
		if !a.inRoot(t) {
			return "project-write .../" + base(t)
		}
	}
	return ""
}

// extraCommand classifies one simple command by its program word.
func (a auditor) extraCommand(words []string) string {
	i := 0
	for i < len(words) {
		w := words[i]
		switch {
		case slices.Contains(headers, w):
			return "" // a test or a for, case or select header runs nothing
		case slices.Contains(wrappers, w) || slices.Contains(reserved, w) || (strings.Contains(w, "=") && !strings.HasPrefix(w, "=")):
			i++
			continue
		}
		break
	}
	if i >= len(words) {
		return ""
	}
	prog, name := words[i], words[i]
	if sysPath.MatchString(prog) {
		name = base(prog)
	}
	if name == "" {
		return ""
	}
	// An unknown executable named by a path, quoted or not, is a script
	// run, unless it is an allowed channel script or command.
	if strings.Contains(name, "/") {
		if a.channelScript(prog) || (base(prog) == "herdsman" && channelCommand(words[i:])) {
			return ""
		}
		return "script-run " + base(name)
	}
	// The command line for main's patterns: the program and its
	// arguments, each argument's own blanks kept apart from the separators,
	// read only where the pattern starts at the program.
	spaced := name
	for _, w := range words[i+1:] {
		spaced += " " + strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' {
				return argSep
			}
			return r
		}, w)
	}
	at0s := func(re *regexp.Regexp) bool {
		loc := re.FindStringIndex(spaced)
		return loc != nil && loc[0] == 0
	}
	for _, p := range []struct {
		re    *regexp.Regexp
		class string
	}{{gitRE, "git-write"}, {ghAPIRE, "outward-write"}, {ghWRE, "outward-write"}, {curlRE, "outward-write"},
		{curlExtraRE, "outward-write"}, {runRE, "test-or-package"}, {testsRE, "test-or-package"},
		{svcRE, "service-or-container"}, {writeRE, "project-write"}, {sedIRE, "project-write"}} {
		if at0s(p.re) {
			return p.class + " " + name
		}
	}
	if (name == "awk" || name == "gawk" || name == "mawk" || name == "nawk") && awkFxRE.MatchString(spaced) {
		return "script-run awk"
	}
	return ""
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
