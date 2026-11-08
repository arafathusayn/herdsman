package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/secrets"
)

const testSecret = "sk-very-Secret-value-42"

func TestPaneRedactsASecretSplitByColorAndWrap(t *testing.T) {
	f := newFixture(t)
	screen := "token " + testSecret[:6] + "\x1b[32m" + testSecret[6:12] + "\r\n" + testSecret[12:] + " done"
	f.herdr.Handle("pane.read", func(p map[string]any) (any, string) {
		return map[string]any{"read": map[string]any{"pane_id": p["pane_id"], "text": screen}}, ""
	})
	s := NewServer(herdr.New(f.herdr.Socket), f.hub, f.queue, Config{Redactor: secrets.NewRedactor(testSecret)})
	req := httptest.NewRequest(http.MethodGet, "/api/pane/w1:p2", nil)
	req.SetPathValue("id", "w1:p2")
	rec := httptest.NewRecorder()
	s.pane(rec, req)
	var got herdr.PaneRead
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Text != "token "+secrets.Marker+" done" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestEveryResponseIsRedacted(t *testing.T) {
	s := &Server{cfg: Config{Redactor: secrets.NewRedactor(testSecret)}}
	h := s.redact(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "999")
		w.WriteHeader(http.StatusTeapot)
		fmt.Fprint(w, "start "+testSecret[:5])
		http.NewResponseController(w).Flush()
		fmt.Fprint(w, testSecret[5:]+" end")
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusTeapot || string(body) != "start "+secrets.Marker+" end" {
		t.Fatalf("%d %q", res.StatusCode, body)
	}
}

func TestRedactingWriterWithoutExplicitHeaderAndUnwrap(t *testing.T) {
	s := &Server{cfg: Config{Redactor: secrets.NewRedactor(testSecret)}}
	rec := httptest.NewRecorder()
	s.redact(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("the stream endpoint needs a flusher")
		}
		if w.(interface{ Unwrap() http.ResponseWriter }).Unwrap() != http.ResponseWriter(rec) {
			t.Error("unwrap")
		}
		w.Header().Set("Content-Length", "3")
		w.Write([]byte(testSecret))
		w.WriteHeader(http.StatusTeapot)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(rec.Body.String(), testSecret) || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("%q %v", rec.Body.String(), rec.Header())
	}
}

func TestNoSecretsLeavesTheHandlerUntouched(t *testing.T) {
	s := &Server{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	rec := httptest.NewRecorder()
	h := s.redact(next)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	if _, wrapped := h.(http.HandlerFunc); !wrapped {
		t.Fatal("type changed")
	}
}
