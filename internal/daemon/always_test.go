package daemon

import (
	"sync/atomic"
	"testing"
	"time"
)

// A caller reads what its function wrote as soon as always returns, so a route
// that stops mid-operation must not release the caller before the function ends.
func TestAlwaysWaitsForAnOperationAlreadyRunningWhenTheRouteStops(t *testing.T) {
	h := newHarness(t)
	h.route("r")
	rt := h.d.routes["r"]
	entered, release, returned, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		rt.always(func() {
			close(entered)
			<-release
		})
		close(returned)
	}()
	<-entered
	go func() {
		rt.stop()
		close(stopped)
	}()
	<-rt.quit
	select {
	case <-returned:
		close(release)
		t.Fatal("always returned while its function was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-returned
	<-stopped
}

// An operation still queued when the route stops is withdrawn: its caller has
// already moved on, so the function must never run.
func TestAlwaysWithdrawsAQueuedOperationWhenTheRouteStops(t *testing.T) {
	h := newHarness(t)
	h.route("r")
	rt := h.d.routes["r"]
	held, release, returned, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	rt.ops <- func() {
		close(held)
		<-release
	}
	<-held
	var ran atomic.Bool
	go func() {
		rt.always(func() { ran.Store(true) })
		close(returned)
	}()
	eventually(t, func() bool { return len(rt.ops) == 1 })
	go func() {
		rt.stop()
		close(stopped)
	}()
	<-returned
	close(release)
	<-stopped
	// The actor may have left the operation queued; run whatever it left.
	select {
	case op := <-rt.ops:
		op()
	default:
	}
	if ran.Load() {
		t.Fatal("a withdrawn operation ran")
	}
}
