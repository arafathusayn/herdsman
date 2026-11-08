package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/route"
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

func redactingServer(f *fixture, r *secrets.Redactor) *Server {
	return NewServer(herdr.New(f.herdr.Socket), f.hub, f.queue, Config{Redactor: r})
}

func TestActionNeverWritesASecretToTheRouteLog(t *testing.T) {
	f := newFixture(t)
	redactingServer(f, secrets.NewRedactor(testSecret))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.queue.Run(ctx)
	f.herdr.Handle("agent.prompt", func(map[string]any) (any, string) { return nil, "failed on " + testSecret })
	// The padding pushes the value across the clipping point of a summary.
	for _, text := range []string{"use " + testSecret, strings.Repeat("x", 112) + testSecret} {
		res := f.queue.Do(ctx, Action{Type: "prompt", Pane: "w1:p1", Text: text})
		if strings.Contains(res.Summary, testSecret[:6]) {
			t.Fatalf("summary = %q", res.Summary)
		}
	}
	f.queue.Do(ctx, Action{Type: "start", Pane: "w1:p1", Name: "agent-b", Kind: "codex", Args: []string{"--token", testSecret}})
	b, err := os.ReadFile(filepath.Join(f.routeDir, route.RouteFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "web: ") || strings.Contains(string(b), testSecret[:6]) {
		t.Fatalf("route file:\n%s", b)
	}
}

func TestPaneRedactsAcrossACharsetSequence(t *testing.T) {
	f := newFixture(t)
	screen := testSecret[:6] + "\x1b(B\x1b[m" + testSecret[6:]
	f.herdr.Handle("pane.read", func(p map[string]any) (any, string) {
		return map[string]any{"read": map[string]any{"pane_id": p["pane_id"], "text": screen}}, ""
	})
	s := redactingServer(f, secrets.NewRedactor(testSecret))
	req := httptest.NewRequest(http.MethodGet, "/api/pane/w1:p2", nil)
	req.SetPathValue("id", "w1:p2")
	rec := httptest.NewRecorder()
	s.pane(rec, req)
	if strings.Contains(rec.Body.String(), testSecret[:6]) || !strings.Contains(rec.Body.String(), "redacted") {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestRedirectLocationIsRedacted(t *testing.T) {
	s := &Server{cfg: Config{Redactor: secrets.NewRedactor(testSecret)}}
	rec := httptest.NewRecorder()
	s.redact(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs?key="+testSecret, http.StatusFound)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(rec.Header().Get("Location"), testSecret) || !strings.Contains(rec.Header().Get("Location"), secrets.Marker) {
		t.Fatalf("location = %q", rec.Header().Get("Location"))
	}
}

func TestHeadersRedactedOnEveryCommitPath(t *testing.T) {
	s := &Server{cfg: Config{Redactor: secrets.NewRedactor(testSecret)}}
	for name, act := range map[string]func(http.ResponseWriter){
		"write":    func(w http.ResponseWriter) { w.Write([]byte("x")) },
		"flush":    func(w http.ResponseWriter) { w.(http.Flusher).Flush() },
		"implicit": func(http.ResponseWriter) {},
	} {
		rec := httptest.NewRecorder()
		s.redact(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Note", testSecret)
			act(w)
		})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if strings.Contains(rec.Header().Get("X-Note"), testSecret) {
			t.Errorf("%s: header = %q", name, rec.Header().Get("X-Note"))
		}
	}
}

func TestJSONResponseKeepsItsDelimiters(t *testing.T) {
	const quoted = `value"`
	s := &Server{cfg: Config{Redactor: secrets.NewRedactor(quoted)}}
	rec := httptest.NewRecorder()
	s.redact(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"a": "value", "b": quoted})
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v in %s", err, rec.Body)
	}
	if got["a"] != "value" || got["b"] != secrets.Marker {
		t.Fatalf("got %v", got)
	}
}

func TestRestrictedProfileRefusesToStartAgents(t *testing.T) {
	f := newFixture(t)
	NewServer(herdr.New(f.herdr.Socket), f.hub, f.queue, Config{RefuseAgentStart: "profile restricted"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.queue.Run(ctx)
	var started atomic.Bool
	f.herdr.Handle("agent.start", func(map[string]any) (any, string) { started.Store(true); return map[string]any{}, "" })
	res := f.queue.Do(ctx, Action{Type: "start", Pane: "w1:p1", Name: "agent-a", Kind: "codex"})
	if res.OK || !strings.Contains(res.Error, "restricted") || started.Load() {
		t.Fatalf("res = %+v, started %v", res, started.Load())
	}
}

func TestRestrictedProfileRefusesToCreateARoute(t *testing.T) {
	s := &Server{cfg: Config{RefuseAgentStart: "profile restricted"}}
	rec := httptest.NewRecorder()
	s.createRoute(rec, httptest.NewRequest(http.MethodPost, "/api/routes", strings.NewReader("{}")))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "restricted") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
