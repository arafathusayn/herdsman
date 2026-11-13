package toml

import (
	"slices"
	"strings"
)

// RenameTable moves the table at from to the path to by rewriting every
// section header at or under it and every dotted key that spells it, and
// reports whether anything matched. A table written inline is one key/value,
// so it is not renamed here. Comments and the order of the file stay as they
// were.
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
		if st.kind == keyValue {
			renamed = renameDottedKey(st, from, to) || renamed
			continue
		}
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

// renameDottedKey rewrites a key whose own dotted parts spell the table. A key
// under a header naming the table is moved by the header instead.
func renameDottedKey(st *statement, from, to []string) bool {
	section := len(st.path) - st.keyParts
	if section >= len(from) || len(st.path) <= len(from) || !hasPathPrefix(st.path, from) {
		return false
	}
	st.respell(slices.Concat(to, st.path[len(from):])[section:])
	return true
}

// respell replaces the key's text and nothing else on its line.
func (st *statement) respell(parts []string) {
	separator := strings.LastIndexByte(st.head, '=')
	keyEnd := len(strings.TrimRight(st.head[:separator], " \t"))
	keyStart := len(st.head) - len(strings.TrimLeft(st.head, " \t"))
	st.head = st.head[:keyStart] + keyText(parts) + st.head[keyEnd:]
}

// RenameKey respells the key written at from, keeping its value, its comment
// and its place, and reports whether such a key was found. The sections above
// it must already carry to's names. A key inside an inline table is not a
// statement, so it is left to the caller.
func (d *Document) RenameKey(from, to []string) (bool, error) {
	if err := checkPath(from); err != nil {
		return false, err
	}
	if err := checkPath(to); err != nil {
		return false, err
	}
	stmts := slices.Clone(d.stmts)
	for i := range stmts {
		st := &stmts[i]
		if st.kind == keyValue && slices.Equal(st.path, from) {
			st.respell(to[len(st.path)-st.keyParts:])
			return true, d.commit(stmts)
		}
	}
	return false, nil
}
