package daemonv1

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const statusPath = "/rpc/herdsman.daemon.v1.MessageService/Status"

// fakeService answers Status; any other method is left to the nil interface.
type fakeService struct {
	MessageService
	got *StatusRequest
	err error
}

func (f *fakeService) Status(_ context.Context, req *StatusRequest) (*StatusResponse, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	return &StatusResponse{Status: "ok", LastSeq: 7, Route: req.GetRoute()}, nil
}

func newServer(t *testing.T, impl MessageService) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(NewMessageServiceHandler(impl))
	t.Cleanup(server.Close)
	return server
}

func request() *StatusRequest {
	return &StatusRequest{Route: &corev1.RouteId{Value: "alpha"}}
}

func post(t *testing.T, url, contentType string, body []byte) (*http.Response, string) {
	t.Helper()
	res, err := http.Post(url, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, string(data)
}

func TestClientCallsHandler(t *testing.T) {
	impl := &fakeService{}
	server := newServer(t, impl)
	client := NewMessageServiceClient(server.Client(), server.URL+"/")
	res, err := client.Status(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "ok" || res.GetLastSeq() != 7 || res.GetRoute().GetValue() != "alpha" {
		t.Fatalf("response %v", res)
	}
	if !proto.Equal(impl.got, request()) {
		t.Fatalf("handler got %v", impl.got)
	}
}

func TestHandlerServesJSON(t *testing.T) {
	server := newServer(t, &fakeService{})
	res, body := post(t, server.URL+statusPath, "application/json", []byte(`{"route":{"value":"alpha"},"later_field":1}`))
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s: %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	var got StatusResponse
	if err := protojson.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.GetRoute().GetValue() != "alpha" {
		t.Fatalf("response %v", &got)
	}
}

func TestHandlerRefusesOtherMethods(t *testing.T) {
	server := newServer(t, &fakeService{})
	res, err := http.Get(server.URL + statusPath)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != "POST" {
		t.Fatalf("%d, Allow %q", res.StatusCode, res.Header.Get("Allow"))
	}
}

func TestHandlerRejectsBadRequests(t *testing.T) {
	server := newServer(t, &fakeService{})
	cases := []struct {
		name, contentType string
		body              []byte
		status            int
		code              string
	}{
		{"content type", "text/plain", []byte("x"), http.StatusUnsupportedMediaType, `"unimplemented"`},
		{"bad json", "application/json", []byte("{"), http.StatusBadRequest, `"invalid_argument"`},
		{"bad proto", "application/proto", []byte{0xff}, http.StatusBadRequest, `"invalid_argument"`},
		{"too large", "application/proto", make([]byte, messageServiceMaxBody+1), http.StatusBadRequest, `"invalid_argument"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, body := post(t, server.URL+statusPath, c.contentType, c.body)
			if res.StatusCode != c.status || !strings.Contains(body, c.code) {
				t.Fatalf("%d: %s", res.StatusCode, body)
			}
		})
	}
}

func TestClientReportsServiceError(t *testing.T) {
	server := newServer(t, &fakeService{err: errors.New("boom")})
	client := NewMessageServiceClient(server.Client(), server.URL)
	_, err := client.Status(context.Background(), request())
	if err == nil || err.Error() != "unknown: boom" {
		t.Fatalf("error %v", err)
	}
}

func clientFor(t *testing.T, handler http.HandlerFunc) MessageService {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewMessageServiceClient(server.Client(), server.URL)
}

func TestClientReportsBadResponses(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"not json", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "<html>", http.StatusBadGateway)
		}, "HTTP 502"},
		{"too large", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(make([]byte, messageServiceMaxBody+1))
		}, "too large"},
		{"bad proto", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte{0xff})
		}, "proto"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := clientFor(t, c.handler).Status(context.Background(), request())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want %q", err, c.want)
			}
		})
	}
}

func TestClientReportsTransportAndRequestFailures(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	client := NewMessageServiceClient(server.Client(), server.URL)
	server.Close()
	if _, err := client.Status(context.Background(), request()); err == nil {
		t.Fatal("no error from a closed server")
	}
	bad := NewMessageServiceClient(http.DefaultClient, "http://bad host")
	if _, err := bad.Status(context.Background(), request()); err == nil {
		t.Fatal("no error from an invalid URL")
	}
}
