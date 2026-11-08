package web

import (
	"net/http"

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
		rw := &redactingWriter{ResponseWriter: w, stream: s.cfg.Redactor.Writer(w)}
		defer rw.stream.Close()
		next.ServeHTTP(rw, r)
	})
}

// redactingWriter sends the body through a redacting stream. Redaction changes
// the length, so a length set by the handler is dropped.
type redactingWriter struct {
	http.ResponseWriter
	stream *secrets.Writer
}

func (w *redactingWriter) WriteHeader(code int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(code)
}

func (w *redactingWriter) Write(b []byte) (int, error) {
	w.Header().Del("Content-Length")
	return w.stream.Write(b)
}

// Flush sends what the stream has released. The end of the data that could
// still become a secret stays held.
func (w *redactingWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets a ResponseController reach the connection's deadlines.
func (w *redactingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
