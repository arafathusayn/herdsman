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

// tailCheck is how many bytes before a parse's end must be unchanged, with
// the metadata, for the file to count as only grown.
const tailCheck = 4096

// ErrChanged says the route file is not the one a page was asked from (it
// was replaced, rotated, edited or it shrank): the caller reloads.
var ErrChanged = errors.New("the route file changed since it was read")

// span is a byte range of the route file.
type span struct{ start, end int64 }

// runSpan is a run-state section's byte range and its record count.
type runSpan struct {
	span
	count int
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
	// meta are the lines outside the run state (headers included), and
	// metaSum their hash: an edit to any of them is parsed again.
	meta    []span
	metaSum []byte
	// tailSum is the hash of the bytes just before size: a rotation of the
	// run state moves them.
	tailSum []byte
	// runs are the run-state ranges that hold the newest Window records.
	runs []runSpan
}

// LoadBounded reads a route folder keeping the newest keep run-state
// records. Given the previous Tail of the same file, it parses only what
// was appended when the file is the same one, did not shrink, and its
// metadata lines and the bytes just before the old end are unchanged; else
// it parses the file whole. The check reads the metadata and 4 KiB, not
// the run state, so its cost does not grow with history. prev is not
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
	br := bufio.NewReaderSize(nil, 64<<10)
	t := &Tail{info: st}
	h := sha256.New()
	if prev != nil && os.SameFile(prev.info, st) && st.Size() >= prev.size && unchanged(f, prev, h, br) {
		t.parser = prev.parser
		r := *prev.parser.r
		r.Tasks, r.Checklist, r.RunState = slices.Clone(r.Tasks), slices.Clone(r.Checklist), slices.Clone(r.RunState)
		t.parser.r, t.parser.header = &r, slices.Clone(prev.parser.header)
		t.size, t.meta, t.runs = prev.size, slices.Clone(prev.meta), slices.Clone(prev.runs)
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
	t.tailSum = tailSum(f, t.size, br)
	return t, nil
}

// add parses one line at the parse's end and files its byte range.
func (t *Tail) add(line string, h hash.Hash) {
	start := t.size
	t.size += int64(len(line))
	text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	before := t.parser.total
	inRun := t.parser.section == "run state" && !strings.HasPrefix(text, "## ")
	t.parser.line(text)
	if !inRun {
		_, _ = io.WriteString(h, line)
		if n := len(t.meta); n > 0 && t.meta[n-1].end == start {
			t.meta[n-1].end = t.size
		} else {
			t.meta = append(t.meta, span{start, t.size})
		}
		return
	}
	n := len(t.runs)
	if n == 0 || t.runs[n-1].end != start {
		t.runs = append(t.runs, runSpan{span: span{start, start}})
		n++
	}
	last := &t.runs[n-1]
	last.end = t.size
	last.count += t.parser.total - before
	// Keep only the ranges that hold the newest Window records.
	held := 0
	for _, s := range t.runs {
		held += s.count
	}
	for len(t.runs) > 1 && held-t.runs[0].count >= Window {
		held -= t.runs[0].count
		t.runs = t.runs[1:]
	}
}

// unchanged says whether prev's metadata lines and the bytes just before
// its end are still the same; h is left holding the metadata's hash.
func unchanged(f *os.File, prev *Tail, h hash.Hash, br *bufio.Reader) bool {
	for _, s := range prev.meta {
		// A short read hashes fewer bytes, so the sums differ.
		br.Reset(io.NewSectionReader(f, s.start, s.end-s.start))
		_, _ = br.WriteTo(h)
	}
	return bytes.Equal(h.Sum(nil), prev.metaSum) && bytes.Equal(tailSum(f, prev.size, br), prev.tailSum)
}

// tailSum is the hash of the tailCheck bytes before end (fewer at the
// file's start); a short read hashes fewer bytes, so the sums differ.
func tailSum(f *os.File, end int64, br *bufio.Reader) []byte {
	start := max(0, end-tailCheck)
	h := sha256.New()
	br.Reset(io.NewSectionReader(f, start, end-start))
	_, _ = br.WriteTo(h)
	return h.Sum(nil)
}

// beforeRead runs between a page's snapshot check and its reads; tests
// change the file there.
var beforeRead = func() {}

// From is the index of the oldest run-state record the view pages to.
func (t *Tail) From() int { return max(0, t.Total-Window) }

// Page returns up to n run-state records ending before index before, no
// older than From, read backward from this snapshot's end through its
// run-state ranges in fixed 64 KiB chunks. A file that is not the snapshot
// (another file, another size or time) gives ErrChanged.
func (t *Tail) Page(before, n int) ([]LogLine, error) {
	before = max(t.From(), min(before, t.Total))
	want := before - max(t.From(), before-n)
	out := make([]LogLine, 0, want)
	if want == 0 {
		return out, nil
	}
	f, err := os.Open(filepath.Join(t.Route.Dir, RouteFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(st, t.info) || st.Size() != t.info.Size() || !st.ModTime().Equal(t.info.ModTime()) {
		return nil, ErrChanged
	}
	beforeRead()
	skip := t.Total - before
	seen := 0
	// One read buffer and one work buffer serve every chunk: a page
	// allocates them once, plus its own records.
	buf := make([]byte, 64<<10)
	work := make([]byte, 0, 2*len(buf))
	var carry []byte
	visit := func(line []byte) bool {
		m := runLine.FindSubmatch(bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r")))
		if m == nil {
			return false
		}
		if seen >= skip {
			out = append(out, LogLine{Time: string(m[1]), Text: string(m[2])})
		}
		seen++
		return len(out) == want
	}
	for i := len(t.runs) - 1; i >= 0; i-- {
		s := t.runs[i]
		pos := s.end
		carry = carry[:0]
		for pos > s.start {
			k := min(int64(len(buf)), pos-s.start)
			pos -= k
			if _, err := f.ReadAt(buf[:k], pos); err != nil {
				return nil, ErrChanged
			}
			work = append(append(work[:0], buf[:k]...), carry...)
			chunk := work
			for {
				j := bytes.LastIndexByte(chunk[:max(0, len(chunk)-1)], '\n')
				if j < 0 {
					break
				}
				if visit(chunk[j+1:]) {
					slices.Reverse(out)
					return out, nil
				}
				chunk = chunk[:j+1]
			}
			carry = append(carry[:0], chunk...)
		}
		if visit(carry) {
			slices.Reverse(out)
			return out, nil
		}
	}
	slices.Reverse(out)
	return out, nil
}
