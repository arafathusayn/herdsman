package route

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
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

// chunkSize is the size of the blocks of the whole file whose hashes are
// kept; verifyChunks of them are read again on each refresh, in turn, so an
// edit no kept line shows is found within one cycle of refreshes.
const (
	chunkSize    = 1 << 20
	verifyChunks = 2
)

// maxSpans bounds the window's kept line ranges; past it they become one
// range (a file of many empty sections: correct, but read whole).
const maxSpans = 4 * (Window + markEvery)

// lineCap is how much of a line a page holds that it does not return.
const lineCap = 64 << 10

// ErrChanged says the route file is not the one a page was asked from (it
// was replaced, rotated, edited or it shrank): the caller reloads.
var ErrChanged = errors.New("the route file changed since it was read")

// span is a byte range of the route file.
type span struct{ start, end int64 }

// mark is the offset of a run-state record, its index, and the header line
// that opened its section.
type mark struct {
	off   int64
	index int
	head  span
}

// Tail is a route file parsed up to its last complete line, with the run
// state bounded, so a file that only grew is read on from there (A24).
type Tail struct {
	Route *Route
	// Total counts every run-state record of the file; Route.RunState holds
	// the newest of them.
	Total int
	size  int64
	// info is the file as it was read: identity, size, time and change time.
	info   os.FileInfo
	parser parser
	// meta are the lines the parse takes metadata from: the lines before
	// the first section, the task table and checklist sections with the
	// header that ends each, and any line, a record too, that set the title
	// or a pane; metaSum is their hash. Other sections are not kept.
	meta    []span
	metaSum []byte
	// tailSum is the hash of the bytes just before size.
	tailSum []byte
	// marks are the offsets of every markEvery-th record, the oldest at or
	// before the window's start (at most Window/markEvery+2 of them).
	marks []mark
	// head is the header line that opened the run-state section being read.
	head span
	// win are the window's run-state lines and the headers that open or
	// end its run-state sections, from the window's start; winSum is the
	// hash of the oldest mark's header and of win. Other sections between
	// them are not read: their cost does not count against the window.
	win    []span
	winSum []byte
	// winH, while a refresh parses, holds the hash of the window as it was
	// checked, so appended window lines extend it and it is not read twice.
	winH     hash.Hash
	winStart int64
	// sums are the hashes of the file's whole chunkSize blocks; part is
	// the hash state of the bytes after them; next is the block the next
	// refresh checks first (len(sums) is the partial one).
	sums  [][]byte
	part  []byte
	chunk hash.Hash
	next  int
	// history names the parse the run state came from: an append keeps it,
	// a full parse (the file was edited, replaced or first read) gives a new
	// one (History).
	history string
	// owed counts the blocks not yet checked since the file last changed
	// without a full parse; while it is above zero the file is not proven
	// and the caller loads it again, unchanged or not (Unverified).
	owed int
}

// Unverified says whether some block of the file was not checked since the
// file last changed: the caller loads the file again even when its size
// and times are the same, and each load checks verifyChunks more blocks.
func (t *Tail) Unverified() bool { return t.owed > 0 }

// History names the run state's history: it stays while the file only
// grows and changes with each full parse, so a reader that kept older
// records drops them when it changes. It is unique to this process run.
func (t *Tail) History() string { return t.history }

var (
	historyRun = strconv.FormatInt(time.Now().UnixNano(), 36)
	historySeq atomic.Uint64
)

func newHistory() string { return historyRun + "." + strconv.FormatUint(historySeq.Add(1), 36) }

// tracked are the sections whose lines the parse takes metadata from.
var tracked = map[string]bool{"": true, "task table": true, "checklist": true}

// LoadBounded reads a route folder keeping the newest keep run-state
// records. Given the previous Tail of the same file, it parses only what
// was appended when the file is the same one, did not shrink, and its
// metadata lines, its window, the bytes just before the old end and the
// blocks this refresh checks in turn are unchanged; else it parses the
// file whole. Each refresh reads the metadata lines, the window's
// run-state lines, verifyChunks blocks and what was appended, so its cost
// does not grow with history or with other sections; an edit elsewhere is
// found within one cycle of refreshes. prev is not changed.
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
	buf := make([]byte, 64<<10)
	br := bufio.NewReaderSize(nil, 64<<10)
	t := &Tail{info: st}
	h := sha256.New()
	if prev != nil && os.SameFile(prev.info, st) && st.Size() >= prev.size && t.unchanged(f, prev, h, buf) {
		// A change starts a new cycle of block checks; a load of the same
		// file goes on with the one owed.
		checked := min(verifyChunks, len(prev.sums)+1)
		if SameState(st, prev.info) {
			t.owed = max(0, prev.owed-checked)
		} else {
			t.owed = len(prev.sums) + 1 - checked
		}
		t.parser, t.history = prev.parser, prev.history
		r := *prev.parser.r
		r.Tasks, r.Checklist, r.RunState = slices.Clone(r.Tasks), slices.Clone(r.Checklist), slices.Clone(r.RunState)
		t.parser.r, t.parser.header = &r, slices.Clone(prev.parser.header)
		t.size, t.meta, t.marks, t.win, t.head = prev.size, slices.Clone(prev.meta), slices.Clone(prev.marks), slices.Clone(prev.win), prev.head
		t.sums = slices.Clip(prev.sums)
		t.chunk = restore(prev.part)
	} else {
		h.Reset()
		t.parser, t.history = parser{r: &Route{Dir: dir, Name: filepath.Base(dir)}, keep: keep}, newHistory()
		t.winH, t.chunk, t.next = nil, sha256.New(), 0
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
	t.tailSum = sumOf(f, max(0, t.size-tailCheck), t.size, buf)
	if t.winH == nil || t.winStart != t.windowStart() {
		t.winH, t.winStart = t.winHash(f, buf)
	}
	t.winSum, t.winH = t.winH.Sum(nil), nil
	t.part, _ = t.chunk.(encoding.BinaryMarshaler).MarshalBinary()
	t.chunk = nil
	return t, nil
}

// restore is a hash in the state part saved.
func restore(part []byte) hash.Hash {
	h := sha256.New()
	if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(part); err != nil {
		h.Reset()
	}
	return h
}

// add parses one line at the parse's end and files it.
func (t *Tail) add(line string, h hash.Hash) {
	start := t.size
	t.size += int64(len(line))
	t.feed(line, start)
	text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	r := t.parser.r
	before, title, adj, orch := t.parser.total, r.Title, r.AdjutantPane, r.OrchestratorPane
	section := t.parser.section
	header := strings.HasPrefix(text, "## ")
	t.parser.line(text)
	// A header that ends a tracked section is kept: it bounds that section.
	if tracked[t.parser.section] || (header && tracked[section]) ||
		r.Title != title || r.AdjutantPane != adj || r.OrchestratorPane != orch {
		_, _ = io.WriteString(h, line)
		t.meta = grow(t.meta, start, t.size)
	}
	if header && t.parser.section == "run state" {
		t.head = span{start, t.size}
	}
	if section == "run state" || (header && t.parser.section == "run state") {
		t.win = grow(t.win, start, t.size)
		if t.winH != nil {
			_, _ = io.WriteString(t.winH, line)
		}
		if len(t.win) > maxSpans {
			t.win = []span{{t.win[0].start, t.size}}
			t.winH = nil
		}
	}
	if t.parser.total > before {
		if before%markEvery == 0 {
			t.marks = append(t.marks, mark{start, before, t.head})
		}
		// Keep the marks the window needs: the second one still has Window
		// records from it on, the first is dropped, with the window's lines
		// before the new start.
		for len(t.marks) > 1 && t.parser.total-t.marks[1].index >= Window {
			t.marks = t.marks[1:]
		}
		ws := t.windowStart()
		for len(t.win) > 0 && t.win[0].end <= ws {
			t.win = t.win[1:]
		}
		if len(t.win) > 0 && t.win[0].start < ws {
			t.win[0].start = ws
		}
	}
}

// grow adds the line from start to end to spans, joining it to the last
// span it follows.
func grow(spans []span, start, end int64) []span {
	if n := len(spans); n > 0 && spans[n-1].end == start {
		spans[n-1].end = end
		return spans
	}
	return append(spans, span{start, end})
}

// feed adds a line at start to the block hashes.
func (t *Tail) feed(line string, start int64) {
	for line != "" {
		n := min(int64(len(line)), chunkSize-start%chunkSize)
		_, _ = io.WriteString(t.chunk, line[:n])
		line, start = line[n:], start+n
		if start%chunkSize == 0 {
			t.sums = append(t.sums, t.chunk.Sum(nil))
			t.chunk.Reset()
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

// winHash hashes the oldest mark's header and the window's lines; it
// returns the window's start too.
func (t *Tail) winHash(f *os.File, buf []byte) (hash.Hash, int64) {
	h := sha256.New()
	if len(t.marks) > 0 {
		hashSpan(f, h, t.marks[0].head, buf)
	}
	for _, s := range t.win {
		hashSpan(f, h, s, buf)
	}
	return h, t.windowStart()
}

// hashSpan adds the bytes of s to h through buf; a short read adds fewer
// bytes, so the sums differ.
func hashSpan(f *os.File, h hash.Hash, s span, buf []byte) {
	for off := s.start; off < s.end; {
		n, _ := f.ReadAt(buf[:min(int64(len(buf)), s.end-off)], off)
		if n == 0 {
			return
		}
		_, _ = h.Write(buf[:n])
		off += int64(n)
	}
}

// unchanged says whether prev's metadata lines, its window, the bytes just
// before its end and the blocks due for a check are still the same; h is
// left holding the metadata's hash, and t the window's hash and the next
// block to check.
func (t *Tail) unchanged(f *os.File, prev *Tail, h hash.Hash, buf []byte) bool {
	for _, s := range prev.meta {
		hashSpan(f, h, s, buf)
	}
	if !bytes.Equal(h.Sum(nil), prev.metaSum) ||
		!bytes.Equal(sumOf(f, max(0, prev.size-tailCheck), prev.size, buf), prev.tailSum) {
		return false
	}
	w, ws := prev.winHash(f, buf)
	if !bytes.Equal(w.Sum(nil), prev.winSum) {
		return false
	}
	t.winH, t.winStart = w, ws
	blocks := len(prev.sums) + 1
	for k := range min(verifyChunks, blocks) {
		i := (prev.next + k) % blocks
		if i < len(prev.sums) {
			if !bytes.Equal(sumOf(f, int64(i)*chunkSize, int64(i+1)*chunkSize, buf), prev.sums[i]) {
				return false
			}
			continue
		}
		start := int64(len(prev.sums)) * chunkSize
		if !bytes.Equal(sumOf(f, start, prev.size, buf), restore(prev.part).Sum(nil)) {
			return false
		}
	}
	t.next = (prev.next + verifyChunks) % blocks
	return true
}

// sumOf is the hash of the bytes from start to end; a short read hashes
// fewer bytes, so the sums differ.
func sumOf(f *os.File, start, end int64, buf []byte) []byte {
	h := sha256.New()
	hashSpan(f, h, span{start, end}, buf)
	return h.Sum(nil)
}

// same says whether the open file is still this snapshot's: the same file,
// size, time and change time. Putting the time back after an edit changes
// the change time, so such an edit is not the snapshot either.
func (t *Tail) same(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && SameState(st, t.info)
}

// SameState says whether two reads of a file saw the same file, size,
// modification time and change time.
func SameState(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && changeTime(a).Equal(changeTime(b))
}

// beforeRead runs between a page's snapshot check and its reads; tests
// change the file there.
var beforeRead = func() {}

// From is the index of the oldest run-state record the view pages to.
func (t *Tail) From() int { return max(0, t.Total-Window) }

// Page returns up to n run-state records ending before index before, no
// older than From, read forward from this snapshot's window start (at most
// Window+markEvery records and the sections between them, whatever the
// history; of a line it does not return it holds at most lineCap bytes).
// The file must be the snapshot before and after the read (file, size,
// time, change time), and hold the records the snapshot counted; else
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
	br := bufio.NewReaderSize(io.NewSectionReader(f, start, t.size-start), lineCap)
	// Lines are read in place. A line longer than the buffer is told apart
	// by its first lineCap bytes: only a record the page returns is gathered
	// whole; the rest of any other line is skipped.
	var long []byte
	inRun, i := true, t.marks[0].index
	for i < before {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			keep := inRun && i >= from && runLine.Match(line)
			long = append(long[:0], line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = br.ReadSlice('\n')
				if keep {
					long = append(long, line...)
				}
			}
			if err != nil {
				return nil, ErrChanged
			}
			line = long
		} else if err != nil {
			return nil, ErrChanged // the file ends before the snapshot's records do
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
