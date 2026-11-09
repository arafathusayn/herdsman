package web

import (
	"net/http"
	"strings"

	"github.com/arafathusayn/herdsman/internal/secrets"
)

// redact covers every response, including errors and the event stream, so a
// new endpoint cannot forget to. Pane text is also redacted where it is read,
// because only there are its colors and line wraps still raw text.
func (s *Server) redact(next http.Handler) http.Handler {
	if !s.cfg.Redactor.Active() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &redactingWriter{ResponseWriter: w, redactor: s.cfg.Redactor}
		defer rw.finish()
		next.ServeHTTP(rw, r)
	})
}

// redactingWriter sends the headers and the body through redaction.
type redactingWriter struct {
	http.ResponseWriter
	redactor *secrets.Redactor
	stream   *secrets.Writer
}

// commit prepares the response once, before anything can be sent: headers are
// cleaned, a length that redaction would falsify is dropped, and the body's
// format picks how it is matched.
func (w *redactingWriter) commit() {
	if w.stream != nil {
		return
	}
	h := w.Header()
	h.Del("Content-Length")
	for _, values := range h {
		for i, v := range values {
			values[i] = w.redactor.String(v)
		}
	}
	r := w.redactor
	if t := h.Get("Content-Type"); strings.Contains(t, "json") || strings.Contains(t, "event-stream") {
		r = r.JSON()
	}
	w.stream = r.Writer(w.ResponseWriter)
}

func (w *redactingWriter) finish() {
	w.commit()
	_ = w.stream.Close()
}

func (w *redactingWriter) WriteHeader(code int) {
	w.commit()
	w.ResponseWriter.WriteHeader(code)
}

func (w *redactingWriter) Write(b []byte) (int, error) {
	w.commit()
	return w.stream.Write(b)
}

// Flush sends what the stream has released. The end of the data that could
// still become a secret stays held.
func (w *redactingWriter) Flush() {
	w.commit()
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets a ResponseController reach the connection's deadlines.
func (w *redactingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
