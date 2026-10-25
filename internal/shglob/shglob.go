// Package shglob lists a folder as the shell's "$dir"/pattern does: the
// folder is taken literally, never as a pattern, so a folder named with
// [, * or ? lists its own files.
package shglob

import (
	"os"
	"path/filepath"
	"strings"
)

// Glob returns the entries of dir whose names match the shell pattern, in
// name order. A name that starts with "." matches only a pattern that
// starts with "." (bash without dotglob). An unreadable folder or a bad
// pattern is an error, so a caller can fail closed.
func Glob(dir, pattern string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(pattern, ".") {
			continue
		}
		ok, err := filepath.Match(pattern, name)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out, nil
}
