package rpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// typeURLPrefix turns a detail's message name back into a type URL.
const typeURLPrefix = "type.googleapis.com/"

// Error is a failed call: what a handler returns and what a client receives.
type Error struct {
	Code    Code
	Message string
	Details []*anypb.Any
}

// Errorf builds an Error with a formatted message.
func Errorf(code Code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// AddDetail appends m to the error's details.
func (e *Error) AddDetail(m proto.Message) error {
	detail, err := anypb.New(m)
	if err != nil {
		return err
	}
	e.Details = append(e.Details, detail)
	return nil
}

// Detail fills m from the first detail of m's type and reports whether there
// was one.
func (e *Error) Detail(m proto.Message) bool {
	for _, detail := range e.Details {
		if detail.MessageIs(m) && detail.UnmarshalTo(m) == nil {
			return true
		}
	}
	return false
}

// FromError is the failure a handler's error is served as. Only an *Error or a
// context error names a code; anything else is Unknown.
func FromError(err error) *Error {
	var failure *Error
	switch {
	case errors.As(err, &failure):
		return failure
	case errors.Is(err, context.DeadlineExceeded):
		return Errorf(DeadlineExceeded, "the call's deadline passed")
	case errors.Is(err, context.Canceled):
		return Errorf(Canceled, "the call was canceled")
	}
	return Errorf(Unknown, "%s", err.Error())
}

type wireDetail struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type wireError struct {
	Code    string       `json:"code"`
	Message string       `json:"message,omitempty"`
	Details []wireDetail `json:"details,omitempty"`
}

// marshal is the failure's JSON body, whatever the request's encoding. A code
// outside the table goes out as unknown, so a client always gets a valid one.
func (e *Error) marshal() []byte {
	out := wireError{Code: string(e.Code), Message: e.Message}
	if !e.Code.Valid() {
		out.Code = string(Unknown)
	}
	for _, detail := range e.Details {
		name := detail.GetTypeUrl()
		name = name[strings.LastIndexByte(name, '/')+1:]
		out.Details = append(out.Details, wireDetail{Type: name, Value: base64.RawStdEncoding.EncodeToString(detail.GetValue())})
	}
	// A struct of strings always encodes.
	body, _ := json.Marshal(out)
	return body
}

// ParseError reads a failure's body. A body that names no valid code is
// described by its HTTP status instead, and a detail that cannot be decoded is
// left out.
func ParseError(status int, body []byte) *Error {
	var in wireError
	if json.Unmarshal(body, &in) != nil || !Code(in.Code).Valid() {
		return Errorf(CodeFromHTTP(status), "HTTP %d", status)
	}
	failure := &Error{Code: Code(in.Code), Message: in.Message}
	for _, detail := range in.Details {
		value, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(detail.Value, "="))
		if err != nil || detail.Type == "" {
			continue
		}
		failure.Details = append(failure.Details, &anypb.Any{TypeUrl: typeURLPrefix + detail.Type, Value: value})
	}
	return failure
}
