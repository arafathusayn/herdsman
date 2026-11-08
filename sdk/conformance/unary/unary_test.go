// Package unary proves that a stock Connect client can call the unary binding
// unchanged: every failure code, both encodings, and both protocol headers.
package unary

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

const procedure = "/rpc/herdsman.daemon.v1.MessageService/Status"

// connectCodes pairs each binding code with connect-go's own constant, so the
// two tables are compared rather than assumed equal.
var connectCodes = map[rpc.Code]connect.Code{
	rpc.Canceled:           connect.CodeCanceled,
	rpc.Unknown:            connect.CodeUnknown,
	rpc.InvalidArgument:    connect.CodeInvalidArgument,
	rpc.DeadlineExceeded:   connect.CodeDeadlineExceeded,
	rpc.NotFound:           connect.CodeNotFound,
	rpc.AlreadyExists:      connect.CodeAlreadyExists,
	rpc.PermissionDenied:   connect.CodePermissionDenied,
	rpc.ResourceExhausted:  connect.CodeResourceExhausted,
	rpc.FailedPrecondition: connect.CodeFailedPrecondition,
	rpc.Aborted:            connect.CodeAborted,
	rpc.OutOfRange:         connect.CodeOutOfRange,
	rpc.Unimplemented:      connect.CodeUnimplemented,
	rpc.Internal:           connect.CodeInternal,
	rpc.Unavailable:        connect.CodeUnavailable,
	rpc.DataLoss:           connect.CodeDataLoss,
	rpc.Unauthenticated:    connect.CodeUnauthenticated,
}

var encodings = []struct {
	name    string
	options []connect.ClientOption
}{
	{"proto", nil},
	{"json", []connect.ClientOption{connect.WithProtoJSON()}},
}

// recorder remembers the headers of the last request.
type recorder struct {
	mu     sync.Mutex
	header http.Header
}

func (r *recorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.header = req.Header.Clone()
		r.mu.Unlock()
		next.ServeHTTP(w, req)
	})
}

func (r *recorder) get(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.header.Get(name)
}

func start(t *testing.T, fn func(context.Context, *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error)) (*httptest.Server, *recorder) {
	t.Helper()
	seen := &recorder{}
	server := httptest.NewServer(seen.wrap(rpc.NewMux(rpc.Unary(daemonv1.MessageServiceName, "Status", fn))))
	t.Cleanup(server.Close)
	return server, seen
}

func call(ctx context.Context, server *httptest.Server, options []connect.ClientOption) (*connect.Response[daemonv1.StatusResponse], error) {
	client := connect.NewClient[daemonv1.StatusRequest, daemonv1.StatusResponse](server.Client(), server.URL+procedure, options...)
	return client.CallUnary(ctx, connect.NewRequest(&daemonv1.StatusRequest{Route: &corev1.RouteId{Value: "alpha"}}))
}

func TestSuccess(t *testing.T) {
	for _, encoding := range encodings {
		t.Run(encoding.name, func(t *testing.T) {
			server, seen := start(t, func(_ context.Context, req *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
				return &daemonv1.StatusResponse{Route: req.GetRoute(), LastSeq: 7}, nil
			})
			res, err := call(context.Background(), server, encoding.options)
			if err != nil {
				t.Fatal(err)
			}
			if res.Msg.GetRoute().GetValue() != "alpha" || res.Msg.GetLastSeq() != 7 {
				t.Fatalf("response %v", res.Msg)
			}
			if got := seen.get(rpc.HeaderProtocolVersion); got != "1" {
				t.Fatalf("protocol version %q", got)
			}
		})
	}
}

func TestEveryCode(t *testing.T) {
	if len(connectCodes) != len(rpc.Codes()) {
		t.Fatalf("%d codes compared, binding has %d", len(connectCodes), len(rpc.Codes()))
	}
	for _, encoding := range encodings {
		for _, code := range rpc.Codes() {
			t.Run(encoding.name+"/"+string(code), func(t *testing.T) {
				server, _ := start(t, func(context.Context, *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
					failure := rpc.Errorf(code, "the reason")
					if err := failure.AddDetail(&corev1.Error{Code: "daemon_code"}); err != nil {
						return nil, err
					}
					return nil, failure
				})
				_, err := call(context.Background(), server, encoding.options)
				var got *connect.Error
				if !errors.As(err, &got) {
					t.Fatalf("error %v", err)
				}
				if got.Code() != connectCodes[code] || got.Message() != "the reason" {
					t.Fatalf("got %v %q, want %v", got.Code(), got.Message(), connectCodes[code])
				}
				if len(got.Details()) != 1 {
					t.Fatalf("details %v", got.Details())
				}
				value, err := got.Details()[0].Value()
				if err != nil {
					t.Fatal(err)
				}
				detail, ok := value.(*corev1.Error)
				if !ok || detail.GetCode() != "daemon_code" {
					t.Fatalf("detail %v", value)
				}
			})
		}
	}
}

func TestTimeoutHeaderReachesTheService(t *testing.T) {
	for _, encoding := range encodings {
		t.Run(encoding.name, func(t *testing.T) {
			var hadDeadline bool
			server, seen := start(t, func(ctx context.Context, _ *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
				_, hadDeadline = ctx.Deadline()
				return &daemonv1.StatusResponse{}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := call(ctx, server, encoding.options); err != nil {
				t.Fatal(err)
			}
			if !hadDeadline || seen.get(rpc.HeaderTimeout) == "" {
				t.Fatalf("deadline %v, header %q", hadDeadline, seen.get(rpc.HeaderTimeout))
			}
		})
	}
}

func TestDeadlinePassesAsItsCode(t *testing.T) {
	server, _ := start(t, func(ctx context.Context, _ *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := call(ctx, server, nil)
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("error %v", err)
	}
}

func TestUnknownProcedureIsUnimplemented(t *testing.T) {
	server, _ := start(t, func(context.Context, *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
		return &daemonv1.StatusResponse{}, nil
	})
	client := connect.NewClient[daemonv1.StatusRequest, daemonv1.StatusResponse](server.Client(), server.URL+"/rpc/no.Such/Thing")
	_, err := client.CallUnary(context.Background(), connect.NewRequest(&daemonv1.StatusRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("error %v", err)
	}
}

// TestStatusTable reads the raw response, since a client hides the HTTP status.
func TestStatusTable(t *testing.T) {
	want := map[rpc.Code]int{rpc.Canceled: 499, rpc.Unknown: 500, rpc.InvalidArgument: 400, rpc.DeadlineExceeded: 504, rpc.NotFound: 404,
		rpc.AlreadyExists: 409, rpc.PermissionDenied: 403, rpc.ResourceExhausted: 429, rpc.FailedPrecondition: 400, rpc.Aborted: 409,
		rpc.OutOfRange: 400, rpc.Unimplemented: 501, rpc.Internal: 500, rpc.Unavailable: 503, rpc.DataLoss: 500, rpc.Unauthenticated: 401}
	for code, status := range want {
		t.Run(string(code), func(t *testing.T) {
			server, _ := start(t, func(context.Context, *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
				return nil, rpc.Errorf(code, "x")
			})
			res, err := http.Post(server.URL+procedure, rpc.ContentTypeJSON, bytes.NewReader([]byte("{}")))
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != status || res.Header.Get("Content-Type") != rpc.ContentTypeJSON {
				t.Fatalf("%d %q, want %d", res.StatusCode, res.Header.Get("Content-Type"), status)
			}
		})
	}
}
