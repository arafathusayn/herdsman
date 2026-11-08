package toml

import (
	"slices"
	"strings"
)

// RenameTable moves the table at from to the path to by rewriting every
// section header at or under it, and reports whether any header matched. A
// table written inline or through dotted keys has no header, so it is not
// renamed here. Comments and the order of the file stay as they were.
func (d *Document) RenameTable(from, to []string) (bool, error) {
	if err := checkPath(from); err != nil {
		return false, err
	}
	if err := checkPath(to); err != nil {
		return false, err
	}
	stmts := slices.Clone(d.stmts)
	renamed := false
	for i := range stmts {
		st := &stmts[i]
		if (st.kind != tableHeader && st.kind != arrayHeader) || !hasPathPrefix(st.path, from) {
			continue
		}
		open, closer := "[", "]"
		if st.kind == arrayHeader {
			open, closer = "[[", "]]"
		}
		indent := st.head[:len(st.head)-len(strings.TrimLeft(st.head, " \t"))]
		st.head = indent + open + keyText(slices.Concat(to, st.path[len(from):])) + closer
		renamed = true
	}
	if !renamed {
		return false, nil
	}
	return true, d.commit(stmts)
}
