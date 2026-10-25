package checkpoint

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

// Reading is the context of a transcript: the tokens of the last record of
// its last 400 lines that carries usage. Claude Code: input + cache
// creation + cache read tokens of the last main-thread assistant turn.
// Codex: the input tokens of the last token_count event with usage.
type Reading struct {
	Tokens int64
	OK     bool // a record with usage was read
	// Skipped counts the lines that are not JSON, and the usage records
	// whose counts are not whole non-negative numbers: each is passed
	// over, so a half-written last line or one bad record never hides the
	// records before it.
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

// ContextTokens reads the context of the transcript p (no reading for a
// file that cannot be read). The tail read is bounded: 400 lines.
func ContextTokens(p string) Reading {
	b, err := tailLines(p, 400)
	if err != nil {
		return Reading{}
	}
	var r Reading
	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) != nil || dec.More() {
			r.Skipped++
			continue
		}
		n, has, ok := record(v)
		switch {
		case !ok:
			r.Skipped++
		case has:
			r.Tokens, r.OK = n, true
		}
	}
	return r
}

// record reads one value: its tokens, whether it carries usage, and false
// for a usage record whose counts are unreadable.
func record(v any) (int64, bool, bool) {
	m, _ := v.(map[string]any)
	switch m["type"] {
	case "assistant":
		msg, _ := m["message"].(map[string]any)
		usage, has := msg["usage"]
		if !has || usage == nil || m["isSidechain"] == true {
			return 0, false, true
		}
		u, ok := usage.(map[string]any)
		if !ok {
			return 0, true, false
		}
		var sum int64
		for _, k := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
			// A missing count adds nothing, as the script's null + n.
			n, ok := count(u[k], true)
			if !ok || sum > math.MaxInt64-n {
				return 0, true, false
			}
			sum += n
		}
		return sum, true, true
	case "event_msg":
		payload, _ := m["payload"].(map[string]any)
		info, _ := payload["info"].(map[string]any)
		if payload["type"] != "token_count" || info["last_token_usage"] == nil {
			return 0, false, true
		}
		l, ok := info["last_token_usage"].(map[string]any)
		if !ok {
			return 0, true, false
		}
		n, ok := count(l["input_tokens"], false)
		return n, true, ok
	}
	return 0, false, true
}

// count reads a token count: a whole non-negative number. A missing or
// null count is 0 where optional is true.
func count(v any, optional bool) (int64, bool) {
	if v == nil {
		return 0, optional
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return i, err == nil && i >= 0
}
