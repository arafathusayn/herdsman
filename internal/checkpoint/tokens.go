package checkpoint

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
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

// TailBytes bounds the transcript read: the last 400 lines within the last
// 4 MiB, read once.
const TailBytes = 4 << 20

// ContextTokens reads the context of the transcript p (no reading for a
// file that cannot be read).
func ContextTokens(p string) Reading {
	b, err := tailLines(p, 400, TailBytes)
	if err != nil {
		return Reading{}
	}
	var r Reading
	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		n, has, ok := record(line)
		switch {
		case !ok:
			r.Skipped++
		case has:
			r.Tokens, r.OK = n, true
		}
	}
	return r
}

// The record shapes: only the fields the reading needs are decoded, so the
// text of a message (often large) is checked but never copied. A field of
// another JSON type reads as absent, as jq reads .field of null.
type (
	loose     string
	transLine struct {
		Type        loose           `json:"type"`
		IsSidechain json.RawMessage `json:"isSidechain"`
		Message     message         `json:"message"`
		Payload     payload         `json:"payload"`
	}
	message struct {
		Usage json.RawMessage `json:"usage"`
	}
	payload struct {
		Type loose `json:"type"`
		Info info  `json:"info"`
	}
	info struct {
		Last json.RawMessage `json:"last_token_usage"`
	}
	counts struct {
		Input    json.RawMessage `json:"input_tokens"`
		Creation json.RawMessage `json:"cache_creation_input_tokens"`
		Read     json.RawMessage `json:"cache_read_input_tokens"`
	}
)

func (l *loose) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, (*string)(l))
	}
	return nil
}

func object(b []byte) bool { return len(b) > 0 && b[0] == '{' }

func (m *message) UnmarshalJSON(b []byte) error {
	if !object(b) {
		return nil
	}
	type plain message
	return json.Unmarshal(b, (*plain)(m))
}

func (p *payload) UnmarshalJSON(b []byte) error {
	if !object(b) {
		return nil
	}
	type plain payload
	return json.Unmarshal(b, (*plain)(p))
}

func (i *info) UnmarshalJSON(b []byte) error {
	if !object(b) {
		return nil
	}
	type plain info
	return json.Unmarshal(b, (*plain)(i))
}

func absent(b json.RawMessage) bool { return len(b) == 0 || string(b) == "null" }

// record reads one line: its tokens, whether it carries usage, and false
// for a line that is not exactly one JSON value or a usage record whose
// counts are unreadable.
func record(line []byte) (int64, bool, bool) {
	var l transLine
	if json.Unmarshal(line, &l) != nil {
		return 0, false, false
	}
	switch l.Type {
	case "assistant":
		if absent(l.Message.Usage) || string(l.IsSidechain) == "true" {
			return 0, false, true
		}
		var u counts
		if !object(l.Message.Usage) || json.Unmarshal(l.Message.Usage, &u) != nil {
			return 0, true, false
		}
		var sum int64
		for _, raw := range []json.RawMessage{u.Input, u.Creation, u.Read} {
			// A missing count adds nothing, as the script's null + n.
			n, ok := int64(0), true
			if !absent(raw) {
				n, ok = count(raw)
			}
			if !ok || sum > math.MaxInt64-n {
				return 0, true, false
			}
			sum += n
		}
		return sum, true, true
	case "event_msg":
		if l.Payload.Type != "token_count" || absent(l.Payload.Info.Last) {
			return 0, false, true
		}
		var u counts
		if !object(l.Payload.Info.Last) || json.Unmarshal(l.Payload.Info.Last, &u) != nil || absent(u.Input) {
			return 0, true, false
		}
		n, ok := count(u.Input)
		return n, true, ok
	}
	return 0, false, true
}

// count reads a token count: a JSON number that is exactly a whole
// non-negative int64, however it is spelled (300000, 3e5, 300000.0).
// Strings, fractions and overflow are refused.
func count(raw json.RawMessage) (int64, bool) {
	// raw is one valid JSON value (the line was checked whole), so a value
	// that is not a number fails both parses.
	s := string(raw)
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, i >= 0
	}
	// Bounded before the exact check: a finite float of at most int64's
	// size, so the rational below stays small.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || f < 0 || f > math.MaxInt64 {
		return 0, false
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	return r.Num().Int64(), true
}
