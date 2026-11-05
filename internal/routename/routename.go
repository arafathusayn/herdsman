// Package routename is the one rule for a daemon route or repository
// name: a path segment the daemon accepts (its routes.json key, the name
// of the route database <route>.db) and every reader of those files
// checks the same way.
package routename

import (
	"regexp"
	"strings"
)

var re = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Valid reports whether s is a route or repository name: 1 to 64 of
// letters, digits, '.', '_' and '-', starting with a letter or digit,
// with no "..".
func Valid(s string) bool { return re.MatchString(s) && !strings.Contains(s, "..") }
