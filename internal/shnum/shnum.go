// Package shnum reads a plain integer as bash's test builtin does: the
// strict settings parser and the watchers' state counters use it (no
// arithmetic evaluator: a setting is a typed value, A27).
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
