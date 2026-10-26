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

// Window is how many of the newest run-state records the route view shows
// and pages; older ones stay in the route file (A24: what the app holds and
// reads does not grow with the route's history).
const Window = 2000

// markEvery is how many records lie between two remembered offsets of the
// window; the window then starts at most markEvery records before Window.
const markEvery = 64

// tailCheck is how many bytes before a parse's end must be unchanged, with
// the metadata and the window, for the file to count as only grown.
const tailCheck = 4096

// ErrChanged says the route file is not the one a page was asked from (it
// was replaced, rotated, edited or it shrank): the caller reloads.
var ErrChanged = errors.New("the route file changed since it was read")

// span is a byte range of the route file.
type span struct{ start, end int64 }

// mark is the offset of a run-state record and its index.
type mark struct {
	off   int64
	index int
}

// Tail is a route file parsed up to its last complete line, with the run
// state bounded, so a file that only grew is read on from there (A24).
type Tail struct {
	Route *Route
	// Total counts every run-state record of the file; Route.RunState holds
	// the newest of them.
	Total int
	size  int64
	// info is the file as it was read: identity, size and time.
	info   os.FileInfo
	parser parser
	// meta are the lines the parse takes metadata from: the lines before
	// the first section, the task table and checklist sections with the
	// header that ends each, and any line that set the title or a pane;
	// metaSum is their hash. Other sections (notes, repeated run-state
	// headers) are not kept.
	meta    []span
	metaSum []byte
	// tailSum is the hash of the bytes just before size.
	tailSum []byte
	// marks are the offsets of every markEvery-th record, the oldest at or
	// before the window's start (at most Window/markEvery+2 of them);
	// winSum is the hash of the bytes from the window's start to size: the
	// shown records and the pages come from one version of the file.
	marks  []mark
	winSum []byte
}

// tracked are the sections whose lines the parse takes metadata from.
var tracked = map[string]bool{"": true, "task table": true, "checklist": true}

// LoadBounded reads a route folder keeping the newest keep run-state
// records. Given the previous Tail of the same file, it parses only what
// was appended when the file is the same one, did not shrink, and its
// metadata lines, its window of the newest records and the bytes just
// before the old end are unchanged; else it parses the file whole. The
// check reads those bytes only, so its cost does not grow with history.
// prev is not changed.
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
	t := &Tail{info: st}
	h := sha256.New()
	if prev != nil && os.SameFile(prev.info, st) && st.Size() >= prev.size && unchanged(f, prev, h, br) {
		t.parser = prev.parser
		r := *prev.parser.r
		r.Tasks, r.Checklist, r.RunState = slices.Clone(r.Tasks), slices.Clone(r.Checklist), slices.Clone(r.RunState)
		t.parser.r, t.parser.header = &r, slices.Clone(prev.parser.header)
		t.size, t.meta, t.marks = prev.size, slices.Clone(prev.meta), slices.Clone(prev.marks)
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
		t.add(line, h)
	}
	r := t.parser.r
	if len(r.RunState) > keep {
		r.RunState = slices.Clone(r.RunState[len(r.RunState)-keep:])
	}
	r.Modified, r.Files = st.ModTime(), listFiles(dir)
	t.Route, t.Total, t.metaSum = r, t.parser.total, h.Sum(nil)
	t.tailSum = sumOf(f, max(0, t.size-tailCheck), t.size, br)
	t.winSum = sumOf(f, t.windowStart(), t.size, br)
	return t, nil
}

// add parses one line at the parse's end and files it.
func (t *Tail) add(line string, h hash.Hash) {
	start := t.size
	t.size += int64(len(line))
	text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	r := t.parser.r
	before, title, adj, orch := t.parser.total, r.Title, r.AdjutantPane, r.OrchestratorPane
	// A header that ends a tracked section is kept: it bounds that section.
	ends := strings.HasPrefix(text, "## ") && tracked[t.parser.section]
	t.parser.line(text)
	switch {
	case t.parser.total > before:
		if before%markEvery == 0 {
			t.marks = append(t.marks, mark{start, before})
		}
		// Keep the marks the window needs: the second one still has Window
		// records from it on, the first is dropped.
		for len(t.marks) > 1 && t.parser.total-t.marks[1].index >= Window {
			t.marks = t.marks[1:]
		}
	case tracked[t.parser.section] || ends || r.Title != title || r.AdjutantPane != adj || r.OrchestratorPane != orch:
		_, _ = io.WriteString(h, line)
		if n := len(t.meta); n > 0 && t.meta[n-1].end == start {
			t.meta[n-1].end = t.size
		} else {
			t.meta = append(t.meta, span{start, t.size})
		}
	}
}

// windowStart is the offset the window's bytes start at: the oldest mark,
// or the parse's end when there is no record.
func (t *Tail) windowStart() int64 {
	if len(t.marks) == 0 {
		return t.size
	}
	return t.marks[0].off
}

// unchanged says whether prev's metadata lines, its window and the bytes
// just before its end are still the same; h is left holding the
// metadata's hash.
func unchanged(f *os.File, prev *Tail, h hash.Hash, br *bufio.Reader) bool {
	for _, s := range prev.meta {
		// A short read hashes fewer bytes, so the sums differ.
		br.Reset(io.NewSectionReader(f, s.start, s.end-s.start))
		_, _ = br.WriteTo(h)
	}
	return bytes.Equal(h.Sum(nil), prev.metaSum) &&
		bytes.Equal(sumOf(f, max(0, prev.size-tailCheck), prev.size, br), prev.tailSum) &&
		bytes.Equal(sumOf(f, prev.windowStart(), prev.size, br), prev.winSum)
}

// sumOf is the hash of the bytes from start to end; a short read hashes
// fewer bytes, so the sums differ.
func sumOf(f *os.File, start, end int64, br *bufio.Reader) []byte {
	h := sha256.New()
	br.Reset(io.NewSectionReader(f, start, end-start))
	_, _ = br.WriteTo(h)
	return h.Sum(nil)
}

// same says whether the open file is still this snapshot's: the same file,
// size and time.
func (t *Tail) same(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && os.SameFile(st, t.info) && st.Size() == t.info.Size() && st.ModTime().Equal(t.info.ModTime())
}

// beforeRead runs between a page's snapshot check and its reads; tests
// change the file there.
var beforeRead = func() {}

// From is the index of the oldest run-state record the view pages to.
func (t *Tail) From() int { return max(0, t.Total-Window) }

// Page returns up to n run-state records ending before index before, no
// older than From, read forward from this snapshot's window start (at most
// Window+markEvery records and the sections between them, whatever the
// history). The file must be the snapshot before and after the read
// (file, size, time), and hold the records the snapshot counted; else
// ErrChanged.
func (t *Tail) Page(before, n int) ([]LogLine, error) {
	before = max(t.From(), min(before, t.Total))
	from := max(t.From(), before-n)
	out := make([]LogLine, 0, before-from)
	if from == before {
		return out, nil
	}
	f, err := os.Open(filepath.Join(t.Route.Dir, RouteFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !t.same(f) {
		return nil, ErrChanged
	}
	beforeRead()
	start := t.windowStart()
	br := bufio.NewReaderSize(io.NewSectionReader(f, start, t.size-start), 64<<10)
	// Lines are read in place (a line longer than the buffer is gathered in
	// long); only the page's own records are copied.
	var long []byte
	inRun, i := true, t.marks[0].index
	for i < before {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, line...)
			continue
		}
		if err != nil {
			return nil, ErrChanged // the file ends before the snapshot's records do
		}
		if len(long) > 0 {
			long = append(long, line...)
			line, long = long, long[:0]
		}
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		switch {
		case bytes.HasPrefix(line, []byte("## ")):
			inRun = bytes.EqualFold(bytes.TrimSpace(line[3:]), []byte("run state"))
		case !inRun || !runLine.Match(line):
		case i < from:
			i++
		default:
			m := runLine.FindSubmatch(line)
			out = append(out, LogLine{Time: string(m[1]), Text: string(m[2])})
			i++
		}
	}
	// A change during the read gives ErrChanged, never a shifted page.
	if !t.same(f) {
		return nil, ErrChanged
	}
	return out, nil
}
