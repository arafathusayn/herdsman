package rpc

import (
	"io"
	"math"
	"net/http"
	"strconv"
	"time"
)

// maxTimeoutMs is the largest value the timeout header can carry.
const maxTimeoutMs = 9_999_999_999

// Transport adds the binding's headers to each call and turns a failure
// response into an *Error, which errors.As finds through the *url.Error that
// http.Client wraps it in.
//
// http.RoundTripper asks implementations not to interpret responses. This one
// does on purpose: it is the one place where a generated client, which sees only
// an error value, can receive a failure's code and details.
type Transport struct {
	// Base carries the request; nil means http.DefaultTransport.
	Base http.RoundTripper
	// Header is added to every request.
	Header http.Header
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	for name, values := range t.Header {
		out.Header[name] = values
	}
	if preview, _ := req.Context().Value(previewKey{}).(bool); preview {
		switch out.Header.Get("Content-Type") {
		case ContentTypeProto:
			out.Header.Set("Content-Type", ContentTypePreviewProto)
		case ContentTypeJSON:
			out.Header.Set("Content-Type", ContentTypePreviewJSON)
		}
	}
	out.Header.Set(HeaderProtocolVersion, ProtocolVersion)
	if end, ok := req.Context().Deadline(); ok {
		out.Header.Set(HeaderTimeout, timeoutHeader(time.Until(end)))
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	res, err := base.RoundTrip(out)
	if err != nil || res.StatusCode == http.StatusOK {
		return res, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxBody))
	if err != nil {
		return nil, err
	}
	return nil, ParseError(res.StatusCode, body)
}

// timeoutHeader rounds up, so a call with time left never announces zero.
func timeoutHeader(left time.Duration) string {
	ms := int64(math.Ceil(float64(left) / float64(time.Millisecond)))
	return strconv.FormatInt(min(max(ms, 1), maxTimeoutMs), 10)
}
