package main

import (
	"github.com/arafathusayn/herdsman/internal/daemon"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

// The verbs' tests call the real services in this process.
func init() {
	dialSuggest = func() (suggestv1.SuggestService, func(), error) {
		return daemon.NewSuggestService(), func() {}, nil
	}
	dialTrace = func() (tracev1.TraceService, func(), error) {
		return daemon.NewTraceService(false), func() {}, nil
	}
}
