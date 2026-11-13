package route

import (
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/coreconfig"
)

// CoreEndpointOf is the core endpoint a journaled start on herdr-core was
// planned with: the canonical connection its launch steps carry, or, for a
// start journaled before they carried one, the socket of its steps. found is
// false for a start on another backend.
func CoreEndpointOf(op *ops.Operation) (coreconfig.Endpoint, bool) {
	if op == nil || BackendOf(op) != runtime.BackendCore {
		return coreconfig.Endpoint{}, false
	}
	socket := ""
	for _, st := range op.Steps {
		d, err := decode(st)
		if err != nil {
			continue
		}
		if d.Core != nil && d.Core.Endpoint != nil && d.Core.Endpoint.Socket != "" {
			return *d.Core.Endpoint, true
		}
		if socket == "" && d.Socket != "" {
			socket = d.Socket
		}
	}
	return coreconfig.Endpoint{Socket: socket}, socket != ""
}

// CoreAppOf is the app a journaled start on herdr-core shows its agents in,
// as its launch steps carry it; empty for a start without one, which reads as
// the daemon's own app.
func CoreAppOf(op *ops.Operation) coreconfig.App {
	if op == nil {
		return coreconfig.App{}
	}
	for _, st := range op.Steps {
		if d, err := decode(st); err == nil && d.Core != nil && d.Core.App != nil {
			return *d.Core.App
		}
	}
	return coreconfig.App{}
}
