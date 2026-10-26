package checkpoint

import (
	"context"
	"encoding/json"
)

// ProbeResult is the probe as `herdsman checkpointer --probe --json`
// prints it, one object:
//
//	why      the reason for the choice (the text after "transcript: ")
//	hint     a newer or the newest transcript to check, or ""
//	file     the transcript read, or "" when there is none
//	context  the context tokens read from it, or null for no reading
type ProbeResult struct {
	Why     string `json:"why"`
	Hint    string `json:"hint"`
	File    string `json:"file"`
	Context *int64 `json:"context"`
}

// ProbeJSON prints the probe as one JSON object.
func (c *Checkpointer) ProbeJSON(ctx context.Context) int {
	ch := c.transcript(ctx)
	res := ProbeResult{Why: ch.Why, Hint: ch.Hint, File: ch.File}
	if ch.File != "" {
		if r := ContextTokens(ch.File); r.OK {
			res.Context = &r.Tokens
		}
	}
	b, _ := json.Marshal(res)
	_, _ = c.Stdout.Write(append(b, '\n'))
	return 0
}
