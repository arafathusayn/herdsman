package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	otlpPath     = "/v1/traces"
	otlpBatch    = 512
	otlpTimeout  = 30 * time.Second
	otlpMaxReply = 1 << 20

	statusOK     = 1
	statusError  = 2
	spanInternal = 1
)

type otlpValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpStatus struct {
	Code int `json:"code,omitempty"`
}

type otlpSpan struct {
	TraceID      string          `json:"traceId"`
	SpanID       string          `json:"spanId"`
	ParentSpanID string          `json:"parentSpanId,omitempty"`
	Name         string          `json:"name"`
	Kind         int             `json:"kind"`
	Start        string          `json:"startTimeUnixNano"`
	End          string          `json:"endTimeUnixNano"`
	Attributes   []otlpAttribute `json:"attributes"`
	Status       otlpStatus      `json:"status"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

// otlpReply is a collector's answer. Rejected spans arrive with a success
// status. proto3 JSON writes the count as a string, others as a number;
// json.Number takes both.
type otlpReply struct {
	PartialSuccess struct {
		RejectedSpans json.Number `json:"rejectedSpans"`
		ErrorMessage  string      `json:"errorMessage"`
	} `json:"partialSuccess"`
}

func textAttribute(key, value string) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{StringValue: &value}}
}

func intAttribute(key string, value int64) otlpAttribute {
	text := strconv.FormatInt(value, 10)
	return otlpAttribute{Key: key, Value: otlpValue{IntValue: &text}}
}

func toOTLP(span Span) otlpSpan {
	attributes := []otlpAttribute{textAttribute("herdsman.kind", span.Kind)}
	if span.Task != "" {
		attributes = append(attributes, textAttribute("herdsman.task", span.Task))
	}
	if span.Args != "" {
		attributes = append(attributes, textAttribute("herdsman.args", span.Args))
	}
	if span.Tokens != nil {
		attributes = append(attributes, intAttribute("herdsman.tokens", *span.Tokens))
	}
	if span.CostMicros != nil {
		attributes = append(attributes, intAttribute("herdsman.cost_micros", *span.CostMicros))
	}
	var status otlpStatus
	if span.Exit != nil {
		attributes = append(attributes, intAttribute("herdsman.exit", int64(*span.Exit)))
		status.Code = statusOK
		if *span.Exit != 0 {
			status.Code = statusError
		}
	}
	return otlpSpan{
		TraceID: span.TraceID, SpanID: span.SpanID, ParentSpanID: span.ParentID, Name: span.Name, Kind: spanInternal,
		Start:      strconv.FormatInt(span.Start.UnixNano(), 10),
		End:        strconv.FormatInt(span.end().UnixNano(), 10),
		Attributes: attributes, Status: status,
	}
}

// otlpURL makes the address to post to: the base address of a collector, or
// the full traces path. Plain HTTP is for this machine only, since spans name
// commands and tasks, and credentials in the address are refused so that they
// never reach a log.
func otlpURL(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("trace: otlp endpoint: %w", err)
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("trace: otlp endpoint %q must be an http or https address", endpoint)
	}
	if parsed.User != nil {
		return "", errors.New("trace: otlp endpoint must not hold credentials")
	}
	if parsed.Scheme == "http" && !loopback(parsed.Hostname()) {
		return "", fmt.Errorf("trace: otlp endpoint %q: plain http is only for this machine; use https", endpoint)
	}
	if !strings.HasSuffix(parsed.Path, otlpPath) {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + otlpPath
	}
	return parsed.String(), nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ExportOTLP posts the trace as OTLP over HTTP with JSON bodies, in batches. A
// redirect is an error, so that spans go only to the address that was given.
func ExportOTLP(ctx context.Context, endpoint string, trace Trace) error {
	target, err := otlpURL(endpoint)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: otlpTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resource := otlpResource{Attributes: []otlpAttribute{textAttribute("service.name", "herdsman"), textAttribute("herdsman.route", trace.Route)}}
	for start := 0; start < len(trace.Spans); start += otlpBatch {
		batch := make([]otlpSpan, 0, otlpBatch)
		for _, span := range trace.Spans[start:min(start+otlpBatch, len(trace.Spans))] {
			batch = append(batch, toOTLP(span))
		}
		request := otlpRequest{ResourceSpans: []otlpResourceSpans{{
			Resource:   resource,
			ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: "herdsman", Version: SchemaVersion}, Spans: batch}},
		}}}
		if err := postOTLP(ctx, client, target, request); err != nil {
			return fmt.Errorf("trace: otlp: after %d of %d spans: %w", start, len(trace.Spans), err)
		}
	}
	return nil
}

func postOTLP(ctx context.Context, client *http.Client, target string, payload otlpRequest) error {
	// The payload holds only strings and integers, which always encode.
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(response.Body, otlpMaxReply))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("collector answered %d", response.StatusCode)
	}
	var parsed otlpReply
	if json.Unmarshal(reply, &parsed) != nil {
		return nil // A success need not carry a body, or a JSON one.
	}
	if rejected := parsed.PartialSuccess.RejectedSpans.String(); rejected != "" && rejected != "0" {
		return fmt.Errorf("collector rejected %s spans: %s", rejected, parsed.PartialSuccess.ErrorMessage)
	}
	return nil
}
