package route

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// markEvery is how many run-state lines lie between two remembered
// offsets: a page reads at most this many lines before its own.
const markEvery = 64

// ErrChanged says the route file no longer holds the lines of the snapshot
// a page was asked from: the caller reloads.
var ErrChanged = errors.New("the route file changed since it was read")

// Tail is a route file parsed up to its last complete line, with the run
// state bounded, so a file that only grew is read on from there (A24).
type Tail struct {
	Route *Route
	// Total counts every run-state line of the file; Route.RunState holds
	// the newest of them.
	Total int
	size  int64
	// sum is the hash of the file's first size bytes: a file whose parsed
	// part changed anywhere is parsed again.
	sum    []byte
	parser parser
	// marks[j] is the offset of run-state line j*markEvery.
	marks []int64
}

// LoadBounded reads a route folder keeping the newest keep run-state
// lines. Given the previous Tail of the same file, it parses only what was
// appended when every byte of the old part is unchanged (checked through a
// hash with one fixed buffer, so the check allocates the same at any
// size); a file edited anywhere before the old end is parsed whole. prev
// is not changed.
func LoadBounded(dir string, keep int, prev *Tail) (*Tail, error) {
	f, err := os.Open(filepath.Join(dir, RouteFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(nil, 64<<10)
	t := &Tail{}
	h := sha256.New()
	if prev != nil && prev.size > 0 && st.Size() >= prev.size && sameStart(f, prev, h, br) {
		t.parser = prev.parser
		r := *prev.parser.r
		r.Tasks, r.Checklist, r.RunState = slices.Clone(r.Tasks), slices.Clone(r.Checklist), slices.Clone(r.RunState)
		t.parser.r, t.parser.header = &r, slices.Clone(prev.parser.header)
		t.size, t.marks = prev.size, slices.Clone(prev.marks)
	} else {
		h.Reset()
		t.parser = parser{r: &Route{Dir: dir, Name: filepath.Base(dir)}, keep: keep}
	}
	br.Reset(io.NewSectionReader(f, t.size, st.Size()-t.size))
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // a partial last line waits for its newline
			}
			return nil, err
		}
		_, _ = io.WriteString(h, line)
		before := t.parser.total
		t.parser.line(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		if t.parser.total > before && before%markEvery == 0 {
			t.marks = append(t.marks, t.size)
		}
		t.size += int64(len(line))
	}
	r := t.parser.r
	if len(r.RunState) > keep {
		r.RunState = slices.Clone(r.RunState[len(r.RunState)-keep:])
	}
	r.Modified, r.Files = st.ModTime(), listFiles(dir)
	t.Route, t.Total, t.sum = r, t.parser.total, h.Sum(nil)
	return t, nil
}

// sameStart says whether the file's first prev.size bytes are the ones
// prev parsed; h is left holding their hash.
func sameStart(f *os.File, prev *Tail, h hash.Hash, br *bufio.Reader) bool {
	br.Reset(io.NewSectionReader(f, 0, prev.size))
	n, _ := br.WriteTo(h)
	return n == prev.size && bytes.Equal(h.Sum(nil), prev.sum)
}

// Page returns up to n run-state lines ending before line index before,
// as this snapshot counted them, reading from the remembered offset at or
// before the page's first line: at most markEvery lines more than the page,
// at any file size and whatever section follows the run state. A file that
// no longer holds those lines gives ErrChanged.
func (t *Tail) Page(before, n int) ([]LogLine, error) {
	before = max(0, min(before, t.Total))
	from := max(0, before-n)
	out := make([]LogLine, 0, before-from)
	if from == before {
		return out, nil
	}
	f, err := os.Open(filepath.Join(t.Route.Dir, RouteFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	j := from / markEvery
	br := bufio.NewReaderSize(io.NewSectionReader(f, t.marks[j], t.size-t.marks[j]), 64<<10)
	section, i := "run state", j*markEvery
	for first := true; i < before; first = false {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, ErrChanged // the file ends before the snapshot did
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.HasPrefix(line, "## ") {
			section = strings.ToLower(strings.TrimSpace(line[3:]))
			continue
		}
		m := runLine.FindStringSubmatch(line)
		if first && m == nil {
			return nil, ErrChanged // the mark no longer starts a run-state line
		}
		if section != "run state" || m == nil {
			continue
		}
		if i >= from {
			out = append(out, LogLine{Time: m[1], Text: m[2]})
		}
		i++
	}
	return out, nil
}
