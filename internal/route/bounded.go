package route

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// sumWindow is how much of a parsed file, before where its parse ended,
// must be unchanged for the file to count as only grown.
const sumWindow = 4096

// Tail is a route file parsed up to its last complete line, with the run
// state bounded, so a file that only grew is read on from there (A24).
type Tail struct {
	Route *Route
	// Total counts every run-state line of the file; Route.RunState holds
	// the newest of them.
	Total  int
	size   int64
	sum    [32]byte
	parser parser
}

// LoadBounded reads a route folder keeping the newest keep run-state
// lines. Given the previous Tail of the same file, it reads only what was
// appended when the file grew and the bytes before the old end are
// unchanged; a file that shrank or was replaced is read whole. prev is not
// changed.
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
	t := &Tail{}
	if prev != nil && prev.size > 0 && st.Size() >= prev.size && window(f, prev.size) == prev.sum {
		t.parser = prev.parser
		r := *prev.parser.r
		r.Tasks, r.Checklist, r.RunState = slices.Clone(r.Tasks), slices.Clone(r.Checklist), slices.Clone(r.RunState)
		t.parser.r, t.parser.header = &r, slices.Clone(prev.parser.header)
		t.size = prev.size
	} else {
		t.parser = parser{r: &Route{Dir: dir, Name: filepath.Base(dir)}, keep: keep}
	}
	if _, err := f.Seek(t.size, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // a partial last line waits for its newline
			}
			return nil, err
		}
		t.size += int64(len(line))
		t.parser.line(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
	}
	r := t.parser.r
	if len(r.RunState) > keep {
		r.RunState = slices.Clone(r.RunState[len(r.RunState)-keep:])
	}
	r.Modified, r.Files = st.ModTime(), listFiles(dir)
	t.Route, t.Total, t.sum = r, t.parser.total, window(f, t.size)
	return t, nil
}

// window is the hash of the bytes just before end.
func window(f *os.File, end int64) [32]byte {
	n := min(int64(sumWindow), end)
	b := make([]byte, n)
	if _, err := f.ReadAt(b, end-n); err != nil {
		return [32]byte{}
	}
	return sha256.Sum256(b)
}

// RunStateLast says whether the run state is the file's last section: then
// PageRunState can read it backward from the end.
func (t *Tail) RunStateLast() bool { return t.parser.section == "run state" }

// PageRunState returns up to n run-state lines ending before line index
// before (of total), reading the file backward from its end in bounded
// chunks; the caller knows the run state is the last section
// (Tail.RunStateLast). A header other than the run state's, or the file's
// start without one, falls back to reading the file whole.
func PageRunState(dir string, before, n, total int) ([]LogLine, error) {
	before = max(0, min(before, total))
	want := min(n, before)
	skip := total - before
	f, err := os.Open(filepath.Join(dir, RouteFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	out := make([]LogLine, 0, want)
	seen := 0
	buf := make([]byte, 64<<10)
	var carry []byte
	pos := st.Size()
	done, fallback, last, header := want == 0, false, true, false
	visit := func(line []byte) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("## ")) {
			// The run state's own header ends it; any other header means the
			// run state is not the last section, so the count was wrong.
			fallback = strings.ToLower(strings.TrimSpace(string(line[3:]))) != "run state"
			done, header = true, true
			return
		}
		m := runLine.FindSubmatch(line)
		if m == nil {
			return
		}
		if seen >= skip && len(out) < want {
			out = append(out, LogLine{Time: string(m[1]), Text: string(m[2])})
		}
		seen++
		done = len(out) == want
	}
	for pos > 0 && !done {
		k := min(int64(len(buf)), pos)
		pos -= k
		if _, err := f.ReadAt(buf[:k], pos); err != nil {
			return nil, err
		}
		chunk := append(buf[:k:k], carry...)
		for !done {
			i := bytes.LastIndexByte(chunk, '\n')
			if i < 0 {
				break
			}
			// What follows the file's last newline is a partial line the
			// parse does not count yet; skip it.
			if !last {
				visit(chunk[i+1:])
			}
			last = false
			chunk = chunk[:i]
		}
		carry = append(carry[:0], chunk...)
	}
	if !done && !last {
		visit(carry) // the file's first line
	}
	if !done || (pos == 0 && !header) {
		fallback = true // the file's start came before a run state header
	}
	if fallback {
		r, err := Load(dir)
		if err != nil {
			return nil, err
		}
		end := min(before, len(r.RunState))
		return slices.Clone(r.RunState[max(0, end-n):end]), nil
	}
	slices.Reverse(out)
	return out, nil
}
