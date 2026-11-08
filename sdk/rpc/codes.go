package rpc

import "net/http"

// Code is the stable name of a failure on the wire.
type Code string

// The failure codes. Their spelling and meaning match the Connect protocol, so
// a stock Connect client understands them.
const (
	Canceled           Code = "canceled"
	Unknown            Code = "unknown"
	InvalidArgument    Code = "invalid_argument"
	DeadlineExceeded   Code = "deadline_exceeded"
	NotFound           Code = "not_found"
	AlreadyExists      Code = "already_exists"
	PermissionDenied   Code = "permission_denied"
	ResourceExhausted  Code = "resource_exhausted"
	FailedPrecondition Code = "failed_precondition"
	Aborted            Code = "aborted"
	OutOfRange         Code = "out_of_range"
	Unimplemented      Code = "unimplemented"
	Internal           Code = "internal"
	Unavailable        Code = "unavailable"
	DataLoss           Code = "data_loss"
	Unauthenticated    Code = "unauthenticated"
)

// statusClientClosed is the status for a call its client gave up on. net/http
// has no name for it.
const statusClientClosed = 499

var codeStatus = map[Code]int{
	Canceled:           statusClientClosed,
	Unknown:            http.StatusInternalServerError,
	InvalidArgument:    http.StatusBadRequest,
	DeadlineExceeded:   http.StatusGatewayTimeout,
	NotFound:           http.StatusNotFound,
	AlreadyExists:      http.StatusConflict,
	PermissionDenied:   http.StatusForbidden,
	ResourceExhausted:  http.StatusTooManyRequests,
	FailedPrecondition: http.StatusBadRequest,
	Aborted:            http.StatusConflict,
	OutOfRange:         http.StatusBadRequest,
	Unimplemented:      http.StatusNotImplemented,
	Internal:           http.StatusInternalServerError,
	Unavailable:        http.StatusServiceUnavailable,
	DataLoss:           http.StatusInternalServerError,
	Unauthenticated:    http.StatusUnauthorized,
}

// Codes lists every failure code.
func Codes() []Code {
	return []Code{Canceled, Unknown, InvalidArgument, DeadlineExceeded, NotFound, AlreadyExists, PermissionDenied,
		ResourceExhausted, FailedPrecondition, Aborted, OutOfRange, Unimplemented, Internal, Unavailable, DataLoss, Unauthenticated}
}

// Valid reports whether c is one of the failure codes.
func (c Code) Valid() bool {
	_, ok := codeStatus[c]
	return ok
}

// HTTPStatus is the status a failure with this code is served with. A code
// outside the table is served as Unknown.
func (c Code) HTTPStatus() int {
	if status, ok := codeStatus[c]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// CodeFromHTTP is the code of a failure whose body names none, as when a proxy
// answered instead of the server.
func CodeFromHTTP(status int) Code {
	switch status {
	case http.StatusBadRequest:
		return Internal
	case http.StatusUnauthorized:
		return Unauthenticated
	case http.StatusForbidden:
		return PermissionDenied
	case http.StatusNotFound:
		return Unimplemented
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return Unavailable
	}
	return Unknown
}
