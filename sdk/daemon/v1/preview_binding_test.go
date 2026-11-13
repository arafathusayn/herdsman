package daemonv1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

type previewService struct {
	MessageService
	called bool
}

func (s *previewService) Send(_ context.Context, q *SendRequest) (*SendResponse, error) {
	s.called = true
	if !q.GetDryRun() {
		panic("preview became a write")
	}
	return &SendResponse{Plan: &corev1.Plan{}}, nil
}

func TestGeneratedPreviewUsesFailClosedEncoding(t *testing.T) {
	impl := &previewService{}
	server := newServer(t, impl)
	client := NewMessageServiceClient(server.Client(), server.URL)
	if _, err := client.Send(t.Context(), &SendRequest{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if !impl.called {
		t.Fatal("preview was not served")
	}
}

func TestGeneratedPreviewCannotMutateAnOlderDaemon(t *testing.T) {
	mutated := false
	older := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The older binding only accepts these two media types and ignores new
		// protobuf fields. It would mutate if the preview used either one.
		if r.Header.Get("Content-Type") == rpc.ContentTypeProto || r.Header.Get("Content-Type") == rpc.ContentTypeJSON {
			mutated = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnsupportedMediaType)
	}))
	defer older.Close()
	client := NewMessageServiceClient(older.Client(), older.URL)
	if _, err := client.Send(t.Context(), &SendRequest{DryRun: true}); err == nil {
		t.Fatal("old daemon accepted preview")
	}
	if mutated {
		t.Fatal("old daemon executed preview as a write")
	}
}
