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
	data, info, same, err := readIfChanged(c.path, c.info)
	if errors.Is(err, fs.ErrNotExist) {
		c.info, c.refs = nil, nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if same {
		return c.refs, nil
	}
	refs, err := parsePacked(data, c.hexLen)
	if err != nil {
		return nil, err
	}
	c.info, c.refs = info, refs
	return refs, nil
}

// readIfChanged returns the file's content and identity, or reports same when
// known still describes it. git replaces these files by rename, so an open
// descriptor reads one complete version.
func readIfChanged(path string, known fs.FileInfo) (string, fs.FileInfo, bool, error) {
	file, err := os.Open(path)
	var info fs.FileInfo
	if err == nil {
		defer file.Close()
		info, err = file.Stat()
	}
	if err != nil {
		return "", nil, false, err
	}
	if known != nil && os.SameFile(known, info) && known.Size() == info.Size() && known.ModTime().Equal(info.ModTime()) {
		return "", info, true, nil
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", nil, false, err
	}
	return string(data), info, false, nil
}

// parsePacked returns the entries sorted by name and applies git's grammar: a
// header only on the first line, a peeled line only straight after a ref, one
// object id width. Whatever git would refuse, or repair by another route, such
// as a file that claims sorted and is not, is an error so that git answers.
func parsePacked(data string, hexLen int) ([]packedRef, error) {
	claimsSorted := false
	if strings.HasPrefix(data, "#") {
		line, rest, ok := strings.Cut(data, "\n")
		traits, isHeader := strings.CutPrefix(line, "# pack-refs with: ")
		if !ok || !isHeader {
			return nil, fmt.Errorf("gitrefs: packed-refs has an invalid header: %q", line)
		}
		claimsSorted = slices.Contains(strings.Split(traits, " "), "sorted")
		data = rest
	}
	var refs []packedRef
	for len(data) > 0 {
		line, rest, ok := strings.Cut(data, "\n")
		if !ok || len(line) <= hexLen+1 || line[hexLen] != ' ' || !isObjectID(line[:hexLen]) || !validRefname(line[hexLen+1:]) {
			return nil, fmt.Errorf("gitrefs: packed-refs has an unexpected line: %q", line)
		}
		refs = append(refs, packedRef{name: line[hexLen+1:], object: strings.ToLower(line[:hexLen])})
		data = rest
		if strings.HasPrefix(data, "^") {
			peeled, after, ok := strings.Cut(data[1:], "\n")
			if !ok || len(peeled) != hexLen || !isHex(peeled) {
				return nil, fmt.Errorf("gitrefs: packed-refs has an invalid peeled line: %q", peeled)
			}
			data = after
		}
	}
	compare := func(a, b packedRef) int { return strings.Compare(a.name, b.name) }
	if !slices.IsSortedFunc(refs, compare) {
		if claimsSorted {
			return nil, fmt.Errorf("gitrefs: packed-refs claims sorted and is not")
		}
		slices.SortFunc(refs, compare)
	}
	for i := 1; i < len(refs); i++ {
		if refs[i].name == refs[i-1].name {
			return nil, fmt.Errorf("gitrefs: packed-refs repeats %s", refs[i].name)
		}
	}
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
