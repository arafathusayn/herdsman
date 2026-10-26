package inbox

import (
	"encoding/json"
	"testing"
)

// A25 item 5: the strict JSON reader takes another agent's input. What it
// accepts is valid JSON that decodes.
func FuzzStrictJSON(f *testing.F) {
	for _, s := range []string{`{}`, `{"a":1}`, `{"a":1,"a":2}`, `[1,2]`, `{"a":"\u0000"}`, `{"a":1e400}`, `{`, ``, `null`, `{"a":{"b":[true,false,null]}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if StrictJSON(b) != nil {
			return
		}
		if !json.Valid(b) {
			t.Fatalf("accepted invalid JSON %q", b)
		}
		if _, err := Decode(b); err != nil {
			t.Fatalf("accepted %q, which does not decode: %v", b, err)
		}
	})
}
