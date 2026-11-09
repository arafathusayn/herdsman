package main

import (
	"github.com/arafathusayn/herdsman/internal/daemon"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

// The verbs' tests call the real services in this process.
func init() {
	dialTrace = func() (tracev1.TraceService, func(), error) {
		return daemon.NewTraceService(false), func() {}, nil
	}
}
