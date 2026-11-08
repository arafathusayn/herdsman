package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

var statusPath = Path(daemonv1.MessageServiceName, "Status")

func statusMux(fn func(context.Context, *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error)) http.Handler {
	return NewMux(Unary(daemonv1.MessageServiceName, "Status", fn))
}

func echo(_ context.Context, req *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
	return &daemonv1.StatusResponse{Route: req.GetRoute(), Status: "ok"}, nil
}

func do(handler http.Handler, method, path, contentType string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func wantFailure(t *testing.T, rec *httptest.ResponseRecorder, status int, code Code) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != ContentTypeJSON {
		t.Fatalf("content type %q", got)
	}
	if got := ParseError(rec.Code, rec.Body.Bytes()).Code; got != code {
		t.Fatalf("code %q, want %q: %s", got, code, rec.Body)
	}
}

func TestServesBothEncodings(t *testing.T) {
	binary, err := proto.Marshal(&daemonv1.StatusRequest{Route: &corev1.RouteId{Value: "alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, contentType, reply string
		body                     []byte
	}{
		{"json", ContentTypeJSON, ContentTypeJSON, []byte(`{"route":{"value":"alpha"},"later_field":1}`)},
		{"json charset", ContentTypeJSON + "; charset=utf-8", ContentTypeJSON, []byte(`{"route":{"value":"alpha"}}`)},
		{"proto", ContentTypeProto, ContentTypeProto, binary},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(statusMux(echo), http.MethodPost, statusPath, c.contentType, c.body, nil)
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != c.reply {
				t.Fatalf("%d %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
			}
			var res daemonv1.StatusResponse
			codec, _ := codecFor(c.reply)
			if err := codec.unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if res.GetRoute().GetValue() != "alpha" {
				t.Fatalf("response %v", &res)
			}
		})
	}
}

func TestRefusesBadRequests(t *testing.T) {
	cases := []struct {
		name, method, contentType string
		body                      []byte
		headers                   map[string]string
		status                    int
		code                      Code
	}{
		{"method", http.MethodGet, ContentTypeJSON, nil, nil, http.StatusMethodNotAllowed, Unimplemented},
		{"content type", http.MethodPost, "text/plain", []byte("x"), nil, http.StatusUnsupportedMediaType, Unimplemented},
		{"no content type", http.MethodPost, "", []byte("x"), nil, http.StatusUnsupportedMediaType, Unimplemented},
		{"protocol version", http.MethodPost, ContentTypeJSON, []byte("{}"), map[string]string{HeaderProtocolVersion: "2"}, http.StatusBadRequest, InvalidArgument},
		{"compressed", http.MethodPost, ContentTypeJSON, []byte("{}"), map[string]string{"Content-Encoding": "gzip"}, http.StatusNotImplemented, Unimplemented},
		{"timeout letters", http.MethodPost, ContentTypeJSON, []byte("{}"), map[string]string{HeaderTimeout: "soon"}, http.StatusBadRequest, InvalidArgument},
		{"timeout sign", http.MethodPost, ContentTypeJSON, []byte("{}"), map[string]string{HeaderTimeout: "+5"}, http.StatusBadRequest, InvalidArgument},
		{"timeout length", http.MethodPost, ContentTypeJSON, []byte("{}"), map[string]string{HeaderTimeout: "12345678901"}, http.StatusBadRequest, InvalidArgument},
		{"timeout spent", http.MethodPost, ContentTypeJSON, []byte("{}"), map[string]string{HeaderTimeout: "0"}, http.StatusGatewayTimeout, DeadlineExceeded},
		{"bad json", http.MethodPost, ContentTypeJSON, []byte("{"), nil, http.StatusBadRequest, InvalidArgument},
		{"empty json", http.MethodPost, ContentTypeJSON, nil, nil, http.StatusBadRequest, InvalidArgument},
		{"bad proto", http.MethodPost, ContentTypeProto, []byte{0xff}, nil, http.StatusBadRequest, InvalidArgument},
		{"too large", http.MethodPost, ContentTypeProto, make([]byte, MaxBody+1), nil, http.StatusTooManyRequests, ResourceExhausted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantFailure(t, do(statusMux(echo), c.method, statusPath, c.contentType, c.body, c.headers), c.status, c.code)
		})
	}
}

func TestUnencodableResponseIsInternal(t *testing.T) {
	handler := statusMux(func(context.Context, *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
		return &daemonv1.StatusResponse{Status: string([]byte{0xff})}, nil
	})
	wantFailure(t, do(handler, http.MethodPost, statusPath, ContentTypeProto, nil, nil), http.StatusInternalServerError, Internal)
}

func TestMethodNotAllowedNamesPost(t *testing.T) {
	rec := do(statusMux(echo), http.MethodGet, statusPath, ContentTypeJSON, nil, nil)
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow %q", got)
	}
}

func TestUnknownProcedure(t *testing.T) {
	wantFailure(t, do(statusMux(echo), http.MethodPost, BasePath+"no.Such/Thing", ContentTypeJSON, []byte("{}"), nil), http.StatusNotFound, Unimplemented)
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("broken") }

func TestUnreadableBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, statusPath, brokenBody{})
	req.Header.Set("Content-Type", ContentTypeJSON)
	rec := httptest.NewRecorder()
	statusMux(echo).ServeHTTP(rec, req)
	wantFailure(t, rec, http.StatusBadRequest, InvalidArgument)
}

func TestTimeoutReachesTheService(t *testing.T) {
	var left time.Duration
	handler := statusMux(func(ctx context.Context, req *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
		end, ok := ctx.Deadline()
		if !ok {
			return nil, Errorf(Internal, "no deadline")
		}
		left = time.Until(end)
		return echo(ctx, req)
	})
	rec := do(handler, http.MethodPost, statusPath, ContentTypeJSON, []byte("{}"), map[string]string{HeaderTimeout: "60000"})
	if rec.Code != http.StatusOK || left <= 0 || left > time.Minute {
		t.Fatalf("%d, left %v: %s", rec.Code, left, rec.Body)
	}
}

func TestEveryCodeHasItsStatus(t *testing.T) {
	want := map[Code]int{Canceled: 499, Unknown: 500, InvalidArgument: 400, DeadlineExceeded: 504, NotFound: 404, AlreadyExists: 409,
		PermissionDenied: 403, ResourceExhausted: 429, FailedPrecondition: 400, Aborted: 409, OutOfRange: 400, Unimplemented: 501,
		Internal: 500, Unavailable: 503, DataLoss: 500, Unauthenticated: 401}
	if len(Codes()) != len(want) {
		t.Fatalf("%d codes, want %d", len(Codes()), len(want))
	}
	for _, code := range Codes() {
		t.Run(string(code), func(t *testing.T) {
			handler := statusMux(func(context.Context, *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
				return nil, fmt.Errorf("wrapped: %w", Errorf(code, "why"))
			})
			rec := do(handler, http.MethodPost, statusPath, ContentTypeJSON, []byte("{}"), nil)
			wantFailure(t, rec, want[code], code)
			if got := ParseError(rec.Code, rec.Body.Bytes()).Message; got != "why" {
				t.Fatalf("message %q", got)
			}
		})
	}
}

func TestFromError(t *testing.T) {
	cases := []struct {
		err  error
		code Code
	}{
		{context.DeadlineExceeded, DeadlineExceeded},
		{context.Canceled, Canceled},
		{errors.New("plain"), Unknown},
	}
	for _, c := range cases {
		if got := FromError(c.err).Code; got != c.code {
			t.Errorf("%v: %q, want %q", c.err, got, c.code)
		}
	}
}

func TestInvalidCodeGoesOutAsUnknown(t *testing.T) {
	failure := &Error{Code: "made_up"}
	if got := ParseError(500, failure.marshal()).Code; got != Unknown {
		t.Fatalf("code %q", got)
	}
	if got := Code("made_up").HTTPStatus(); got != http.StatusInternalServerError {
		t.Fatalf("status %d", got)
	}
}

func TestErrorText(t *testing.T) {
	if got := Errorf(NotFound, "no %s", "route").Error(); got != "not_found: no route" {
		t.Fatalf("%q", got)
	}
	if got := (&Error{Code: NotFound}).Error(); got != "not_found" {
		t.Fatalf("%q", got)
	}
}

func TestDetailsRoundTrip(t *testing.T) {
	failure := Errorf(NotFound, "gone")
	if err := failure.AddDetail(&corev1.Error{Code: "unknown_route", Message: "m"}); err != nil {
		t.Fatal(err)
	}
	body := failure.marshal()
	if strings.Contains(string(body), "=") || !strings.Contains(string(body), `"type":"herdsman.core.v1.Error"`) {
		t.Fatalf("body %s", body)
	}
	parsed := ParseError(404, body)
	var detail corev1.Error
	if !parsed.Detail(&detail) || detail.GetCode() != "unknown_route" {
		t.Fatalf("detail %v of %s", &detail, body)
	}
	if parsed.Detail(&corev1.RouteId{}) {
		t.Fatal("found a detail of another type")
	}
}

func TestAddDetailReportsMarshalFailure(t *testing.T) {
	invalid := &corev1.RouteId{Value: string([]byte{0xff})}
	if err := Errorf(Internal, "x").AddDetail(invalid); err == nil {
		t.Fatal("no error for invalid UTF-8")
	}
}

func TestParseErrorFallsBackToStatus(t *testing.T) {
	cases := []struct {
		status int
		code   Code
	}{
		{400, Internal}, {401, Unauthenticated}, {403, PermissionDenied}, {404, Unimplemented}, {429, Unavailable},
		{502, Unavailable}, {503, Unavailable}, {504, Unavailable}, {500, Unknown}, {418, Unknown},
	}
	for _, c := range cases {
		got := ParseError(c.status, []byte("<html>"))
		if got.Code != c.code || got.Message != fmt.Sprintf("HTTP %d", c.status) {
			t.Errorf("%d: %v", c.status, got)
		}
	}
	if got := ParseError(500, []byte(`{"code":"not_a_code"}`)).Code; got != Unknown {
		t.Errorf("unknown code in body gave %q", got)
	}
}

func TestParseErrorDropsBadDetails(t *testing.T) {
	body := `{"code":"internal","details":[{"type":"x.Y","value":"!!"},{"type":"","value":"AA"},{"type":"x.Z","value":"AA=="}]}`
	got := ParseError(500, []byte(body))
	if len(got.Details) != 1 || got.Details[0].GetTypeUrl() != typeURLPrefix+"x.Z" {
		t.Fatalf("details %v", got.Details)
	}
	var mismatch anypb.Any
	if got.Detail(&mismatch) {
		t.Fatal("matched an unrelated type")
	}
}

type tripFunc func(*http.Request) (*http.Response, error)

func (f tripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newRequest(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://x/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestTransportSetsHeaders(t *testing.T) {
	var seen http.Header
	transport := &Transport{Header: http.Header{"X-Party": {"p1"}}, Base: tripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Header
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req := newRequest(t, ctx)
	res, err := transport.RoundTrip(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("%v %v", res, err)
	}
	if seen.Get(HeaderProtocolVersion) != "1" || seen.Get("X-Party") != "p1" || seen.Get(HeaderTimeout) == "" {
		t.Fatalf("headers %v", seen)
	}
	if req.Header.Get(HeaderProtocolVersion) != "" {
		t.Fatal("the caller's request was changed")
	}
}

func TestTransportNoDeadlineNoHeader(t *testing.T) {
	var seen http.Header
	transport := &Transport{Base: tripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Header
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	if _, err := transport.RoundTrip(newRequest(t, context.Background())); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen[HeaderTimeout]; ok {
		t.Fatalf("headers %v", seen)
	}
}

func TestTransportDefaultsToDefaultTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Transport{}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
}

func TestClientFindsFailureThroughHTTPClient(t *testing.T) {
	failure := Errorf(NotFound, "gone")
	hc := &http.Client{Transport: &Transport{Base: tripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewReader(failure.marshal()))}, nil
	})}}
	_, err := hc.Do(newRequest(t, context.Background()))
	var got *Error
	if !errors.As(err, &got) || got.Code != NotFound {
		t.Fatalf("error %v", err)
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("cut") }
func (failingBody) Close() error             { return nil }

func TestTransportPassesFailures(t *testing.T) {
	sentinel := errors.New("no route to host")
	transport := &Transport{Base: tripFunc(func(*http.Request) (*http.Response, error) { return nil, sentinel })}
	if _, err := transport.RoundTrip(newRequest(t, context.Background())); !errors.Is(err, sentinel) {
		t.Fatalf("error %v", err)
	}
	transport = &Transport{Base: tripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: failingBody{}}, nil
	})}
	if _, err := transport.RoundTrip(newRequest(t, context.Background())); err == nil || err.Error() != "cut" {
		t.Fatalf("error %v", err)
	}
}

func TestTimeoutHeaderBounds(t *testing.T) {
	cases := []struct {
		left time.Duration
		want string
	}{
		{-time.Second, "1"}, {time.Microsecond, "1"}, {1500 * time.Microsecond, "2"}, {time.Duration(1<<63 - 1), "9223372036"},
		{200 * 24 * time.Hour, "9999999999"},
	}
	for _, c := range cases[:3] {
		if got := timeoutHeader(c.left); got != c.want {
			t.Errorf("%v: %s, want %s", c.left, got, c.want)
		}
	}
	if got := timeoutHeader(cases[4].left); got != cases[4].want {
		t.Errorf("clamp: %s", got)
	}
}

func TestWriteErrorAnswersWithTheCodesStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, Errorf(Unavailable, "busy"))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"unavailable"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
