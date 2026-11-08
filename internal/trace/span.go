// Package trace turns what a route left on disk into spans, exports them as
// OpenTelemetry, and keeps the records evaluation needs: suite runs and
// reviewer findings with what later happened to each.
//
// It reads and appends plain files in a route folder. It runs no git, opens no
// store and signals nothing, so the command line can use it directly.
package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SchemaVersion is the version of the trace JSON. Span kinds, names and fields
// only grow within a major version.
const SchemaVersion = "1.0.0"

// The kinds of span.
const (
	KindRoute   = "route"
	KindTurn    = "turn"
	KindCommand = "command"
	KindCheck   = "check"
	KindReview  = "review"
	KindHook    = "hook"
)

// Files inside a route folder.
const (
	spansFile = "spans.jsonl"
	traceDir  = "trace"
)

// Schema names the version of a document.
type Schema struct {
	Version string `json:"version"`
}

// Span is one timed piece of a route. Exit, Tokens and CostMicros are nil when
// nothing reported them, which differs from a reported zero.
type Span struct {
	Schema     string    `json:"schema,omitempty"`
	TraceID    string    `json:"trace_id,omitempty"`
	SpanID     string    `json:"span_id,omitempty"`
	ParentID   string    `json:"parent_id,omitempty"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Task       string    `json:"task,omitempty"`
	Start      time.Time `json:"start"`
	DurationMS float64   `json:"duration_ms"`
	Exit       *int      `json:"exit,omitempty"`
	Tokens     *int64    `json:"tokens,omitempty"`
	CostMicros *int64    `json:"cost_micros,omitempty"`
	Args       string    `json:"args,omitempty"`
	// StartApproximate marks a start taken from the run as a whole rather
	// than from this one span.
	StartApproximate bool `json:"start_approximate,omitempty"`
}

// Trace is one route's spans, the root first and the rest by start time.
type Trace struct {
	Schema  Schema `json:"schema"`
	Route   string `json:"route"`
	TraceID string `json:"trace_id"`
	Spans   []Span `json:"spans"`
	Skipped int    `json:"skipped"`
}

// recordedKind reports whether a span of this kind can come from a log. The
// root is always derived.
func recordedKind(kind string) bool {
	return slices.Contains([]string{KindTurn, KindCommand, KindCheck, KindReview, KindHook}, kind)
}

func (s Span) valid() bool {
	return recordedKind(s.Kind) && s.Name != "" && s.DurationMS >= 0 && !s.Start.IsZero()
}

// Append records a span in the route's own log, for the work no other log
// covers: agent turns, checks and reviews.
func Append(route string, span Span) error {
	if !span.valid() {
		return fmt.Errorf("trace: a span needs a known kind, a name, a start and a duration of zero or more (kind %q, name %q)", span.Kind, span.Name)
	}
	span.Schema, span.TraceID, span.SpanID, span.ParentID = SchemaVersion, "", "", ""
	return appendLine(route, spansFile, span)
}

// appendLine writes one JSON line under the route's trace folder, in one write
// call so that lines from concurrent processes do not interleave. The route
// must exist: a mistyped path must fail, not create a new folder.
func appendLine(route, file string, record any) error {
	info, err := os.Stat(route)
	if err != nil {
		return fmt.Errorf("trace: route: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("trace: route %s is not a folder", route)
	}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("trace: encode: %w", err)
	}
	dir := filepath.Join(route, traceDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("trace: %w", err)
	}
	handle, err := os.OpenFile(filepath.Join(dir, file), os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("trace: %w", err)
	}
	_, err = handle.Write(append(line, '\n'))
	return errors.Join(err, handle.Close())
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// finish sorts the spans and gives each an id derived from its content, so
// exporting the same route twice produces the same ids. A collector can then
// drop the repeat. usage is the route's measured usage; nil sums the spans.
func finish(name string, spans []Span, skipped int, usage *measured) Trace {
	traceID := digest("trace", name)[:32]
	result := Trace{Schema: Schema{Version: SchemaVersion}, Route: name, TraceID: traceID, Spans: []Span{}, Skipped: skipped}
	if len(spans) == 0 {
		return result
	}
	slices.SortStableFunc(spans, func(a, b Span) int {
		if order := a.Start.Compare(b.Start); order != 0 {
			return order
		}
		return strings.Compare(a.Kind+"\x00"+a.Name, b.Kind+"\x00"+b.Name)
	})
	root := rootSpan(name, spans, usage)
	root.TraceID, root.SpanID = traceID, digest(traceID, KindRoute)[:16]
	seen := map[string]int{}
	result.Spans = append(result.Spans, root)
	for _, span := range spans {
		key := strings.Join([]string{span.Kind, span.Name, span.Task, span.Start.UTC().Format(time.RFC3339Nano)}, "\x00")
		span.Schema, span.TraceID, span.ParentID = "", traceID, root.SpanID
		span.SpanID = digest(traceID, key, strconv.Itoa(seen[key]))[:16]
		seen[key]++
		result.Spans = append(result.Spans, span)
	}
	return result
}

func (s Span) end() time.Time {
	return s.Start.Add(time.Duration(s.DurationMS * float64(time.Millisecond)))
}

func rootSpan(name string, spans []Span, usage *measured) Span {
	first, last := spans[0].Start, spans[0].end()
	var total measured
	var reported bool
	for _, span := range spans {
		if span.end().After(last) {
			last = span.end()
		}
		if span.Tokens != nil {
			total.tokens += *span.Tokens
			reported = true
		}
		if span.CostMicros != nil {
			total.costMicros += *span.CostMicros
			reported = true
		}
	}
	root := Span{Kind: KindRoute, Name: name, Start: first, DurationMS: float64(last.Sub(first)) / float64(time.Millisecond)}
	if usage != nil {
		total, reported = *usage, true
	}
	if reported {
		root.Tokens, root.CostMicros = &total.tokens, &total.costMicros
	}
	return root
}
