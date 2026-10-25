package checkpoint

import (
	"bufio"
	"io"
	"math"
	"os"
	"strconv"
)

// Reading is the context of a transcript: the tokens of the last record of
// its last 400 lines that carries usage. Claude Code: input + cache
// creation + cache read tokens of the last main-thread assistant turn.
// Codex: the input tokens of the last token_count event with usage.
type Reading struct {
	Tokens int64
	OK     bool // a record with usage was read
	// Skipped counts the lines that are not exactly one JSON value, and
	// the usage records whose counts are not whole non-negative numbers:
	// each is passed over, so a half-written last line or one bad record
	// never hides the records around it.
	Skipped int
}

// String is the context as the log and the probe print it: the tokens, or
// "" when there is no reading.
func (r Reading) String() string {
	if !r.OK {
		return ""
	}
	return strconv.FormatInt(r.Tokens, 10)
}

// ContextTokens reads the context of the transcript p from its last 400
// lines (no reading for a file that cannot be read). Each line is
// streamed: memory stays flat whatever the size of a record.
func ContextTokens(p string) Reading {
	f, err := os.Open(p)
	if err != nil {
		return Reading{}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Reading{}
	}
	start, err := lastLinesStart(f, st.Size(), 400)
	if err != nil {
		return Reading{}
	}
	l := &lineReader{r: bufio.NewReaderSize(io.NewSectionReader(f, start, st.Size()-start), scanBuf)}
	var r Reading
	for !l.done {
		held, err := l.line()
		if !held {
			continue
		}
		var tokens int64
		has, ok := false, false
		if err == nil {
			tokens, has, ok = l.reading()
		}
		switch {
		case !ok:
			r.Skipped++
		case has:
			r.Tokens, r.OK = tokens, true
		}
	}
	return r
}

// reading reads the kept fields of one line: its tokens, whether it
// carries usage, and false for a usage record whose counts are unreadable.
// A field of another JSON type reads as absent, as jq reads .field of null;
// the fields of a member that is not an object were never kept.
func (l *lineReader) reading() (int64, bool, bool) {
	g := l.got
	absent := func(path string) bool { c, ok := g[path]; return !ok || c.kind == 'z' }
	is := func(path, s string) bool { c := g[path]; return c.kind == 's' && !c.long && c.str == s }
	count := func(path string) (int64, bool) { c := g[path]; return c.count, c.kind == 'n' && c.ok }
	// A line that is null is no record; one that is not an object is not a
	// transcript record (jq fails on .type of it).
	switch g[""].kind {
	case 'z':
		return 0, false, true
	case 'o':
	default:
		return 0, false, false
	}
	if is("type", "assistant") {
		if g["message"].kind != 'o' || absent("message.usage") || g["isSidechain"].kind == 't' {
			return 0, false, true
		}
		if g["message.usage"].kind != 'o' {
			return 0, true, false
		}
		var sum int64
		for _, k := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
			path := "message.usage." + k
			// A missing count adds nothing, as the script's null + n.
			if absent(path) {
				continue
			}
			n, ok := count(path)
			if !ok || sum > math.MaxInt64-n {
				return 0, true, false
			}
			sum += n
		}
		return sum, true, true
	}
	if !is("type", "event_msg") || g["payload"].kind != 'o' || !is("payload.type", "token_count") ||
		g["payload.info"].kind != 'o' || absent("payload.info.last_token_usage") {
		return 0, false, true
	}
	last := "payload.info.last_token_usage"
	if g[last].kind != 'o' || absent(last+".input_tokens") {
		return 0, true, false
	}
	n, ok := count(last + ".input_tokens")
	return n, true, ok
}
