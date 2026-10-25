package audit

import "strings"

// sub stands for a command substitution in the word that holds it, so a
// command word made from one is never a known program.
const sub = "$_SUB_"

// splitCommands is the transcript-audit command splitter (b0a9b04). It
// returns the simple commands of s, each as its words after quote removal,
// or the reason the syntax is not parsed. Single quotes, double quotes,
// $'...' and backslash escapes keep their text in one word; ; & | ( ) and
// newlines separate commands only outside quotes. A redirect operator and
// its target are not words; a descriptor number before the operator is
// dropped. $(...), `...`, <(...) and >(...) hold commands of their own, one
// level deep. A nested substitution, a here-document, or an unterminated
// quote, substitution, arithmetic or parameter expansion is a gap. Each line
// is read on its own, as the script's awk program reads records.
func splitCommands(s string) ([][]string, string) {
	var out [][]string
	for _, line := range strings.Split(s, "\n") {
		p := splitter{s: line}
		p.run()
		if p.why != "" {
			return nil, p.why
		}
		out = append(out, p.out...)
	}
	return out, ""
}

type splitter struct {
	s   string
	out [][]string
	why string

	cmd              []string
	w                strings.Builder
	inword, skip, dq bool
	ctx              byte // 0, ')' or '`'
	pd               int

	xcmd                []string
	xw                  string
	xin, xdq, xskip, sq bool
}

func (p *splitter) addc(c string) { p.w.WriteString(c); p.inword = true }

func (p *splitter) flushw() {
	if p.inword {
		if p.skip {
			p.skip = false
		} else {
			p.cmd = append(p.cmd, p.w.String())
		}
	}
	p.w.Reset()
	p.inword = false
}

func (p *splitter) endcmd() {
	p.flushw()
	p.skip = false
	if len(p.cmd) > 0 {
		p.out = append(p.out, p.cmd)
	}
	p.cmd = nil
}

func (p *splitter) enter(kind byte) {
	if p.ctx != 0 {
		p.why = "nested command substitution"
		return
	}
	p.ctx = kind
	p.xcmd, p.xw, p.xin, p.xdq, p.xskip = p.cmd, p.w.String(), p.inword, p.dq, p.skip
	p.cmd, p.inword, p.dq, p.skip, p.pd = nil, false, false, false, 0
	p.w.Reset()
}

func (p *splitter) leave() {
	p.endcmd()
	p.ctx = 0
	p.cmd, p.inword, p.dq, p.skip = p.xcmd, p.xin, p.xdq, p.xskip
	p.w.Reset()
	p.w.WriteString(p.xw)
	p.addc(sub)
}

// balance is the index of the close that balances the open at from, or -1.
func (p *splitter) balance(o, cl byte, from int) int {
	depth := 0
	for j := from; j < len(p.s); j++ {
		switch p.s[j] {
		case o:
			depth++
		case cl:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

func (p *splitter) at(i int) byte {
	if i < len(p.s) {
		return p.s[i]
	}
	return 0
}

func (p *splitter) run() {
	s, n := p.s, len(p.s)
	for i := 0; i < n && p.why == ""; i++ {
		c, d := s[i], p.at(i+1)
		if p.sq {
			if c == '\'' {
				p.sq = false
			} else {
				p.addc(string(c))
			}
			continue
		}
		switch {
		case c == '\\':
			i++
			e := p.at(i)
			if p.dq && strings.IndexByte("$`\"\\", e) < 0 {
				p.addc("\\")
			}
			if e != 0 {
				p.addc(string(e))
			}
		case c == '`':
			if p.ctx == '`' {
				p.leave()
			} else {
				p.enter('`')
			}
		case c == '$' && d == '(' && p.at(i+2) == '(':
			j := p.balance('(', ')', i+1)
			if j < 0 {
				p.why = "unterminated arithmetic"
				return
			}
			p.addc(s[i : j+1])
			i = j
		case c == '$' && d == '(':
			i++
			p.enter(')')
		case c == '$' && d == '{':
			j := p.balance('{', '}', i+1)
			if j < 0 {
				p.why = "unterminated parameter expansion"
				return
			}
			p.addc(s[i : j+1])
			i = j
		case c == '$' && d == '\'' && !p.dq:
			p.inword = true
			for i += 2; i < n; i++ {
				e := s[i]
				if e == '\\' {
					i++
					if i < n {
						p.addc(string(s[i]))
					}
					continue
				}
				if e == '\'' {
					break
				}
				p.addc(string(e))
			}
			if i >= n {
				p.why = "unterminated quote"
				return
			}
		case p.dq:
			if c == '"' {
				p.dq = false
			} else {
				p.addc(string(c))
			}
		case c == '\'':
			p.sq, p.inword = true, true
		case c == '"':
			p.dq, p.inword = true, true
		case c == '#' && !p.inword:
			i = n
		case c == ' ' || c == '\t':
			p.flushw()
		case (c == '<' || c == '>') && d == '(':
			p.flushw()
			i++
			p.enter(')')
		case c == '<' || c == '>':
			if p.inword && allDigits(p.w.String()) {
				p.w.Reset()
				p.inword = false
			} else {
				p.flushw()
			}
			switch {
			case c == '<' && d == '<':
				if p.at(i+2) != '<' {
					p.why = "here-document"
					return
				}
				i += 2
			case d == '>' || d == '|' || d == '&' || (c == '<' && d == '>'):
				i++
			}
			p.skip = true
		case c == ';' || c == '&' || c == '|' || c == '\n':
			p.endcmd()
		case c == '(':
			if p.ctx == ')' {
				p.pd++
			}
			p.endcmd()
		case c == ')':
			if p.ctx == ')' {
				if p.pd > 0 {
					p.pd--
					p.endcmd()
				} else {
					p.leave()
				}
				continue
			}
			p.endcmd()
		default:
			p.addc(string(c))
		}
	}
	if p.why != "" {
		return
	}
	if p.sq || p.dq {
		p.why = "unterminated quote"
		return
	}
	if p.ctx != 0 {
		p.why = "unterminated command substitution"
		return
	}
	p.endcmd()
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
