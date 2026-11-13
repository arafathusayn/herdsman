package rpc

import (
	"context"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Preview media types carry a safety requirement, not merely a new optional
// field. Older servers reject the encoding before calling a mutation.
const (
	ContentTypePreviewProto = "application/vnd.herdsman.preview+proto"
	ContentTypePreviewJSON  = "application/vnd.herdsman.preview+json"
)

func isPreview(req proto.Message) bool {
	if req == nil {
		return false
	}
	m := req.ProtoReflect()
	field := m.Descriptor().Fields().ByName("dry_run")
	return field != nil && field.Kind() == protoreflect.BoolKind && !field.IsList() && m.Get(field).Bool()
}

// RequestContentType chooses the binary binding, with fail-closed preview
// semantics when dry_run is true. Responses use the standard binary encoding.
func RequestContentType(req proto.Message) string {
	if isPreview(req) {
		return ContentTypePreviewProto
	}
	return ContentTypeProto
}

type previewKey struct{}

// WithPreview marks a raw RPC call as a preview for Transport. Typed clients
// select the preview media type directly from their request.
func WithPreview(ctx context.Context) context.Context {
	return context.WithValue(ctx, previewKey{}, true)
}
