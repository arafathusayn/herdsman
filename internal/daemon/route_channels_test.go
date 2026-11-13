package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

// A deleted route's channel is forgotten: its routes.json entry and its open
// route go, and a forget that names another inbox leaves the entry alone.
func TestRouteChannelsForgetADeletedRoute(t *testing.T) {
	h, _ := newCoreHarness(t)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	box := filepath.Join(h.ph, "proj-harbor")
	if err := (coreChannel{h.d}).Open(ctx, route.ChannelOpen{Route: "harbor", Repo: repo, Inbox: box, PolicyFiles: []string{h.rules}}); err != nil {
		t.Fatal(err)
	}
	reg := routeChannels{h.d}
	if !reg.Registered("harbor", box) || reg.Registered("harbor", box+"-other") {
		t.Fatal("the registration is not read by its inbox")
	}
	if err := reg.Forget(ctx, "harbor", box+"-other"); err != nil || !reg.Registered("harbor", box) {
		t.Fatalf("a forget naming another inbox: %v", err)
	}
	if err := reg.Forget(ctx, "harbor", box); err != nil {
		t.Fatal(err)
	}
	rf, err := ReadRoutes(h.ph)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rf.Routes["harbor"]; ok {
		t.Fatal("routes.json still names the deleted route")
	}
	if _, e := h.d.route("harbor"); e == nil {
		t.Fatal("the deleted route is still open")
	}
	if err := reg.Forget(ctx, "harbor", box); err != nil {
		t.Fatalf("a repeat: %v", err)
	}
}
