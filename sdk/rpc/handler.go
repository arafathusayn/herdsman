// Package rpc is the unary HTTP binding every herdsman service is served
// through. A method is POST /rpc/<package>.<Service>/<Method>, its body the
// request as JSON or binary protobuf, and it is byte-compatible with the unary
// half of the Connect protocol, so a stock Connect client can call it. The
// package has no dependency beyond net/http and protobuf-go.
package rpc

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// The binding's names on the wire.
const (
	BasePath              = "/rpc/"
	ContentTypeJSON       = "application/json"
	ContentTypeProto      = "application/proto"
	HeaderProtocolVersion = "Connect-Protocol-Version"
	HeaderTimeout         = "Connect-Timeout-Ms"
	ProtocolVersion       = "1"
)

// MaxBody bounds a request or response body.
const MaxBody = 4 << 20

// maxTimeoutDigits is the longest timeout header the protocol allows.
const maxTimeoutDigits = 10

// Procedure is one served method: its path and its handler.
type Procedure struct {
	Path    string
	Handler http.Handler
}

// Path is the HTTP path of a method.
func Path(service, method string) string {
	return BasePath + service + "/" + method
}

// Unary binds one handler function to its path.
func Unary[Req, Res any, PReq interface {
	*Req
	proto.Message
}, PRes interface {
	*Res
	proto.Message
}](service, method string, fn func(context.Context, PReq) (PRes, error)) Procedure {
	return Procedure{
		Path: Path(service, method),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req := PReq(new(Req))
			serve(w, r, req, func(ctx context.Context) (proto.Message, error) {
				res, err := fn(ctx, req)
				if err != nil {
					return nil, err
				}
				return res, nil
			})
		}),
	}
}

// NewMux serves the procedures. Mount it at BasePath. A path that names no
// procedure gets a 404 whose body a client can read.
func NewMux(procedures ...Procedure) http.Handler {
	mux := http.NewServeMux()
	for _, procedure := range procedures {
		mux.Handle(procedure.Path, procedure.Handler)
	}
	mux.HandleFunc(BasePath, func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusNotFound, Errorf(Unimplemented, "no such procedure: %s", r.URL.Path))
	})
	return mux
}

// codec is one of the two body encodings.
type codec struct {
	contentType string
	marshal     func(proto.Message) ([]byte, error)
	unmarshal   func([]byte, proto.Message) error
}

var (
	jsonCodec = codec{
		contentType: ContentTypeJSON,
		marshal:     func(m proto.Message) ([]byte, error) { return protojson.Marshal(m) },
		// A later contract may add fields an older server has not heard of.
		unmarshal: func(b []byte, m proto.Message) error {
			return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, m)
		},
	}
	protoCodec = codec{contentType: ContentTypeProto, marshal: proto.Marshal, unmarshal: proto.Unmarshal}
)

// codecFor picks the encoding of a Content-Type header. Parameters such as a
// charset do not change the encoding.
func codecFor(header string) (codec, bool) {
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return codec{}, false
	}
	switch mediaType {
	case ContentTypeJSON:
		return jsonCodec, true
	case ContentTypeProto:
		return protoCodec, true
	}
	return codec{}, false
}

// write sends a failure with an explicit status: routing failures use HTTP's
// own status rather than the code's.
func write(w http.ResponseWriter, status int, failure *Error) {
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(failure.marshal())
}

func fail(w http.ResponseWriter, failure *Error) {
	write(w, failure.Code.HTTPStatus(), failure)
}

// deadline applies the request's timeout header. Connect-Timeout-Ms is one to
// ten ASCII digits; strconv alone would also take a sign.
func deadline(ctx context.Context, header string) (context.Context, context.CancelFunc, error) {
	if header == "" {
		return ctx, func() {}, nil
	}
	if len(header) > maxTimeoutDigits || strings.Trim(header, "0123456789") != "" {
		return nil, nil, Errorf(InvalidArgument, "invalid %s", HeaderTimeout)
	}
	// Ten digits at most, so this cannot overflow.
	ms, _ := strconv.ParseInt(header, 10, 64)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	return ctx, cancel, nil
}

func readFailure(err error) *Error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return Errorf(ResourceExhausted, "the request body is larger than %d bytes", tooLarge.Limit)
	}
	return Errorf(InvalidArgument, "cannot read the request body")
}

// serve carries one call. The checks run in the order that gives a client the
// most useful failure: an unservable request is refused before its body is read.
func serve(w http.ResponseWriter, r *http.Request, req proto.Message, call func(context.Context) (proto.Message, error)) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		write(w, http.StatusMethodNotAllowed, Errorf(Unimplemented, "only POST is served"))
		return
	}
	encoding, ok := codecFor(r.Header.Get("Content-Type"))
	if !ok {
		w.Header().Set("Accept-Post", ContentTypeJSON+", "+ContentTypeProto)
		write(w, http.StatusUnsupportedMediaType, Errorf(Unimplemented, "unsupported content type"))
		return
	}
	if version := r.Header.Get(HeaderProtocolVersion); version != "" && version != ProtocolVersion {
		fail(w, Errorf(InvalidArgument, "%s must be %s", HeaderProtocolVersion, ProtocolVersion))
		return
	}
	if contentEncoding := r.Header.Get("Content-Encoding"); contentEncoding != "" && contentEncoding != "identity" {
		fail(w, Errorf(Unimplemented, "unsupported content encoding"))
		return
	}
	ctx, cancel, err := deadline(r.Context(), r.Header.Get(HeaderTimeout))
	if err != nil {
		fail(w, FromError(err))
		return
	}
	defer cancel()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		fail(w, readFailure(err))
		return
	}
	if err := encoding.unmarshal(body, req); err != nil {
		fail(w, Errorf(InvalidArgument, "cannot decode the request body"))
		return
	}
	// A deadline of zero, or one spent reading the body, never reaches the service.
	if err := ctx.Err(); err != nil {
		fail(w, FromError(err))
		return
	}
	res, err := call(ctx)
	if err != nil {
		fail(w, FromError(err))
		return
	}
	out, err := encoding.marshal(res)
	if err != nil {
		fail(w, Errorf(Internal, "cannot encode the response"))
		return
	}
	w.Header().Set("Content-Type", encoding.contentType)
	_, _ = w.Write(out)
}
