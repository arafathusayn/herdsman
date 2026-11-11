package route_test

import (
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
	"github.com/arafathusayn/herdsman/internal/runtime/coreconfig"
)

// A core start journals its endpoint and app with each launch step, and a
// step runs on a provider with that app and connect timeout.
func TestCoreStepsRunWithTheRoutesAppAndConnectTimeout(t *testing.T) {
	r := newCoreRig(t)
	var asked []coreconfig.App
	app := herdrapp.New(coretest.AppRunner{W: r.w}, r.w.AppHome)
	r.coreApp = func(a coreconfig.App) *herdrapp.App {
		asked = append(asked, a)
		return app
	}
	e, closeDB := r.engine()
	defer closeDB()
	q := r.request()
	q.NoAdjutant = true
	q.Core.Endpoint = coreconfig.Endpoint{Socket: r.w.Core.Socket, Session: "", Binary: "/opt/hc"}
	q.Core.App = coreconfig.App{Binary: "/opt/herdr-app", Home: r.w.AppHome}
	q.Core.Timeouts.Connect = 3 * time.Second
	p := r.plan(q)
	if !strings.Contains(p.Text, "core: "+r.w.Core.Socket) {
		t.Errorf("the plan does not name the core:\n%s", p.Text)
	}
	op := drive(t, e, p.Spec)
	ep, ok := route.CoreEndpointOf(op)
	if !ok || ep != q.Core.Endpoint {
		t.Errorf("journaled endpoint %+v %v", ep, ok)
	}
	if got := route.CoreAppOf(op); got != q.Core.App {
		t.Errorf("journaled app %+v", got)
	}
	if len(asked) == 0 || asked[0] != q.Core.App {
		t.Fatalf("the app asked for: %+v", asked)
	}
	r.openedMu.Lock()
	defer r.openedMu.Unlock()
	seen := false
	for _, cp := range r.opened {
		if cp.App == app {
			seen = true
			if cp.Options.ConnectTimeout != 3*time.Second {
				t.Errorf("connect timeout %s", cp.Options.ConnectTimeout)
			}
		}
	}
	if !seen {
		t.Fatal("no step ran with the route's app")
	}
}
