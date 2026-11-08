package gitrefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
)

type packedRef struct {
	name   string
	object string
}

// packedCache holds the last parse of packed-refs and reparses only when the
// file changed. git replaces the file by rename, so an open descriptor always
// reads one complete version.
type packedCache struct {
	path   string
	hexLen int

	mu   sync.Mutex
	info fs.FileInfo
	refs []packedRef
}

// current returns the refs of packed-refs as it is now. A missing file has none.
func (c *packedCache) current() ([]packedRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	file, err := os.Open(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		c.info, c.refs = nil, nil
		return nil, nil
	}
	var info fs.FileInfo
	if err == nil {
		defer file.Close()
		info, err = file.Stat()
	}
	if err != nil {
		return nil, err
	}
	if c.info != nil && os.SameFile(c.info, info) && c.info.Size() == info.Size() && c.info.ModTime().Equal(info.ModTime()) {
		return c.refs, nil
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	refs, err := parsePacked(string(data), c.hexLen)
	if err != nil {
		return nil, err
	}
	c.info, c.refs = info, refs
	return refs, nil
}

// parsePacked returns the entries sorted by name, without those whose name is
// invalid, which git ignores as well. Any line git would reject is an error.
func parsePacked(data string, hexLen int) ([]packedRef, error) {
	var refs []packedRef
	for len(data) > 0 {
		end := strings.IndexByte(data, '\n')
		if end < 0 {
			return nil, errors.New("gitrefs: packed-refs does not end in a newline")
		}
		line := data[:end]
		data = data[end+1:]
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		if len(line) <= hexLen+1 || line[hexLen] != ' ' || !isObjectID(line[:hexLen]) {
			return nil, fmt.Errorf("gitrefs: packed-refs has an unexpected line: %q", line)
		}
		if name := line[hexLen+1:]; validRefname(name) {
			refs = append(refs, packedRef{name: name, object: strings.ToLower(line[:hexLen])})
		}
	}
	slices.SortFunc(refs, func(a, b packedRef) int { return strings.Compare(a.name, b.name) })
	return refs, nil
}

func lookupPacked(refs []packedRef, name string) (string, bool) {
	index, found := slices.BinarySearchFunc(refs, name, func(ref packedRef, target string) int {
		return strings.Compare(ref.name, target)
	})
	if !found {
		return "", false
	}
	return refs[index].object, true
}
