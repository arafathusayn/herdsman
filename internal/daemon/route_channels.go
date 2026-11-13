package daemon

import (
	"context"
	"path/filepath"
)

// routeChannels is the route package's view of routes.json: a deleted
// route's channel is forgotten once its inbox is gone, on either backend, so
// no entry outlives the files it names.
type routeChannels struct{ d *Daemon }

// Registered implements route.ChannelRegistry.
func (r routeChannels) Registered(name, inbox string) bool {
	e, ok := r.d.routeEntry(name)
	return ok && filepath.Clean(e.Inbox) == filepath.Clean(inbox)
}

// Forget implements route.ChannelRegistry: a route registered at another
// inbox is someone else's and is left alone.
func (r routeChannels) Forget(_ context.Context, name, inbox string) error {
	if !r.Registered(name, inbox) {
		return nil
	}
	if e := r.d.removeRoute(name); e != nil && e.Code != "unknown_route" {
		return e
	}
	r.d.logf("route %s: its channel was forgotten with its deleted inbox", name)
	return nil
}
