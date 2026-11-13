package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
)

func TestPreviewEncodingRejectsAnUnsupportedMethod(t *testing.T) {
	called := false
	handler := statusMux(func(context.Context, *corev1.RouteId) (*corev1.Error, error) {
		called = true
		return &corev1.Error{}, nil
	})
	for _, encoding := range []string{ContentTypePreviewProto, ContentTypePreviewJSON} {
		body := []byte(`{"dry_run":true}`)
		if encoding == ContentTypePreviewProto {
			body = []byte{0x10, 0x01}
		}
		rec := do(handler, http.MethodPost, statusPath, encoding, body, nil)
		wantFailure(t, rec, http.StatusNotImplemented, Unimplemented)
	}
	if called {
		t.Fatal("preview reached a method that cannot plan")
	}
}

func TestRawPreviewTransportKeepsTheSafetyEncoding(t *testing.T) {
	for _, encoding := range []string{ContentTypeProto, ContentTypeJSON} {
		got := ""
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusOK)
		}))
		req, err := http.NewRequestWithContext(WithPreview(t.Context()), http.MethodPost, server.URL, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", encoding)
		res, err := (&http.Client{Transport: &Transport{}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		server.Close()
		want := ContentTypePreviewProto
		if encoding == ContentTypeJSON {
			want = ContentTypePreviewJSON
		}
		if got != want {
			t.Fatalf("%s became %s", encoding, got)
		}
	}
}
