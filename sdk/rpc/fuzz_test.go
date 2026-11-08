package rpc

import (
	"net/http"
	"testing"
)

// FuzzRequestPath drives the whole request path with arbitrary input: it must
// never panic, and every answer must be a success or a well-formed failure.
func FuzzRequestPath(f *testing.F) {
	f.Add(ContentTypeJSON, "", "", []byte(`{"route":{"value":"alpha"}}`))
	f.Add(ContentTypeProto, "1", "1000", []byte{0x0a, 0x02, 0x0a, 0x00})
	f.Add("application/json; charset=utf-8", "2", "x", []byte("{"))
	f.Add("", "", "99999999999", []byte{0xff})
	f.Fuzz(func(t *testing.T, contentType, version, timeout string, body []byte) {
		headers := map[string]string{HeaderProtocolVersion: version, HeaderTimeout: timeout}
		rec := do(statusMux(echo), http.MethodPost, statusPath, contentType, body, headers)
		if rec.Code == http.StatusOK {
			return
		}
		if got := rec.Header().Get("Content-Type"); got != ContentTypeJSON {
			t.Fatalf("failure content type %q", got)
		}
		failure := ParseError(rec.Code, rec.Body.Bytes())
		if !failure.Code.Valid() || failure.Message == "" {
			t.Fatalf("malformed failure %d: %s", rec.Code, rec.Body)
		}
	})
}
