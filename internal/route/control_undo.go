package route

import (
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// ForgottenChannel is the daemon channel a finished delete forgot: the route,
// its repository and the inbox its forget step named, the Herdr socket a TUI
// route was registered with, and the backend. ok is false for a delete that
// forgot none (a route without an inbox).
type ForgottenChannel struct {
	Route, Repo, Inbox, HerdrSocket, Backend string
}

// ForgottenChannelOf reads the channel a delete operation forgets.
func ForgottenChannelOf(del *ops.Operation) (ForgottenChannel, bool) {
	if del == nil || del.Kind != KindDelete {
		return ForgottenChannel{}, false
	}
	for _, st := range del.Steps {
		if st.Effect != effectForget {
			continue
		}
		d, err := decode(st)
		if err != nil || d.Inbox == "" {
			return ForgottenChannel{}, false
		}
		ch := ForgottenChannel{Route: d.Route, Repo: d.Repo, Inbox: d.Inbox, Backend: runtime.LegacyBackend(d.Backend)}
		if ch.Backend == runtime.BackendTUI {
			ch.HerdrSocket = d.Socket
		}
		return ch, true
	}
	return ForgottenChannel{}, false
}
