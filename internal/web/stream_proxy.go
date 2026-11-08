//go:build eventservice

package web

import (
	"context"
	"net/http"
	"sync"

	"github.com/arafathusayn/herdsman/sdk/events"
)

func init() { streamProxy = (*gateway).stream }

// stream joins a browser's WebSocket to one of its own on the daemon's socket.
// It keeps no position of its own: the daemon's cursors are what a browser
// resumes from, on a new connection, whichever side dropped.
func (g *gateway) stream(w http.ResponseWriter, r *http.Request) {
	var session string
	browser, err := events.Accept(w, r, events.AcceptOptions{CheckOrigin: func(r *http.Request) bool {
		id, ok := g.session(r)
		session = id
		return ok && g.originAllowed(r)
	}})
	if err != nil {
		return
	}
	daemon, err := events.Dial(r.Context(), events.DialOptions{
		URL:         "ws://" + daemonHost + events.Path,
		DialContext: g.dial,
		Header:      http.Header{HeaderSession: {session}},
	})
	if err != nil {
		_ = browser.Close(events.CloseGoingAway, "the daemon is not reachable")
		return
	}

	var once sync.Once
	finished := make(chan struct{})
	end := func() {
		once.Do(func() {
			close(finished)
			// Each Close waits for its peer's answer, so neither may hold up
			// the other.
			go func() { _ = browser.Close(events.CloseGoingAway, "") }()
			go func() { _ = daemon.Close() }()
		})
	}
	defer context.AfterFunc(r.Context(), end)()

	var pumps sync.WaitGroup
	pumps.Go(func() {
		defer end()
		for {
			frame, err := browser.Read()
			if err != nil || daemon.Send(frame) != nil {
				return
			}
		}
	})
	pumps.Go(func() {
		defer end()
		for {
			frame, err := daemon.Recv()
			if err != nil || browser.Write(frame) != nil {
				return
			}
		}
	})
	pumps.Wait()
	<-finished
}
