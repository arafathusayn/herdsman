// Package shnum reads a number from settings text as bash does, so a
// ported command accepts and refuses the same values as its script.
package shnum

import (
	"strconv"
	"strings"
)

// Test reads an integer as bash's test does ([ "$x" -ge "$y" ]): leading
// white space, an optional sign, decimal digits (leading zeros stay
// decimal), trailing blanks. ok is false where test fails with "integer
// expression expected", which makes the comparison false.
func Test(s string) (int64, bool) {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	s = strings.TrimRight(s, " \t")
	if strings.HasPrefix(s, "+") {
		s = s[1:]
		if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// Arith reads a literal as bash's $(( )) does: white space around it, an
// optional sign, and decimal, octal (leading 0) or hexadecimal (0x)
// digits. ok is false where bash stops with an arithmetic error.
func Arith(s string) (int64, bool) {
	s = strings.Trim(s, " \t\n\v\f\r")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	base := 10
	switch {
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		base, s = 16, s[2:]
	case len(s) > 1 && s[0] == '0':
		base, s = 8, s[1:]
	}
	if s == "" || s[0] == '+' || s[0] == '-' {
		return 0, false
	}
	n, err := strconv.ParseInt(s, base, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}
