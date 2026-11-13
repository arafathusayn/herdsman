package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
)

// fabricatedDelete is a finished delete of a route whose forget step names
// the inbox, as PlanControl journals it.
func fabricatedDelete(t *testing.T, start *ops.Operation) *ops.Operation {
	t.Helper()
	plan, err := route.PlanControl(context.Background(), route.ControlRequest{Verb: route.VerbDelete, Start: start, Generation: 1, Force: true, Date: "2026-11-11"})
	if err != nil {
		t.Fatal(err)
	}
	op := &ops.Operation{ID: "op-delete", Kind: route.KindDelete, State: ops.OpDone}
	for _, sp := range plan.Spec.Steps {
		op.Steps = append(op.Steps, ops.Step{StepSpec: sp})
	}
	return op
}

// Undo of a delete registers the route's channel again at the inbox it put
// back, on herdr-core; a name taken since and an inbox the daemon does not
// own are left alone.
func TestUndoRegistersTheDeletedRoutesChannelAgain(t *testing.T) {
	h, _ := newCoreHarness(t)
	c := routeClient(t, h)
	repo := coreRepo(t, h)
	git(t, repo, "init", "-q")
	if _, err := c.Route.Up(context.Background(), coreUp(repo)); err != nil {
		t.Fatal(err)
	}
	waitHalt(t, c, repo)
	entry, ok := h.d.routeEntry("harbor")
	if !ok || entry.Inbox == "" {
		t.Fatal("the core route registered no channel")
	}
	del := fabricatedDelete(t, journaledStart(t, h, repo, "harbor"))
	forgetAndRestore(t, h, del, entry.Inbox)
	if got, ok := h.d.routeEntry("harbor"); !ok || got.Inbox != entry.Inbox || got.Repo != repo || got.HerdrSocket != "" {
		t.Fatalf("after undo: %+v %v", got, ok)
	}
	if _, e := h.d.route("harbor"); e != nil {
		t.Fatalf("the route is not served again: %v", e)
	}
	// Restoring again changes nothing: the name is registered.
	if err := h.d.restoreChannel(del); err != nil {
		t.Fatal(err)
	}
}

// On the Herdr TUI, a daemon-owned inbox comes back with the Herdr socket it
// was registered with; a script-owned inbox, never registered, stays so.
func TestUndoRegistersATUIRoutesChannelAgain(t *testing.T) {
	h := newHarness(t)
	repo := filepath.Join(h.ph, "proj")
	inbox := filepath.Join(h.ph, "inboxes", "tharbor")
	if err := (coreChannel{h.d}).Open(context.Background(), route.ChannelOpen{Route: "tharbor", Repo: repo, Inbox: inbox, PolicyFiles: []string{h.rules}}); err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(map[string]any{"key": "k", "route": "tharbor", "repo": repo, "folder": filepath.Join(repo, "f"), "inbox": inbox,
		"workspace": "w", "socket": h.srv.Socket, "executable": h.cfg.Executable})
	if err != nil {
		t.Fatal(err)
	}
	del := &ops.Operation{ID: "op-delete-tui", Kind: route.KindDelete, State: ops.OpDone,
		Steps: []ops.Step{{StepSpec: ops.StepSpec{Effect: "route.forget-channel", Key: "forget-channel", Spec: string(spec)}}}}
	forgetAndRestore(t, h, del, inbox)
	if got, ok := h.d.routeEntry("tharbor"); !ok || got.Inbox != inbox || got.HerdrSocket != h.srv.Socket {
		t.Fatalf("after undo: %+v %v", got, ok)
	}
	// A script-owned inbox has no owner record: nothing is registered.
	script := filepath.Join(h.ph, "inboxes", "script")
	if err := os.MkdirAll(script, 0o700); err != nil {
		t.Fatal(err)
	}
	spec, _ = json.Marshal(map[string]any{"route": "script", "repo": repo, "inbox": script, "socket": h.srv.Socket})
	scriptDel := &ops.Operation{ID: "op-delete-script", Kind: route.KindDelete, State: ops.OpDone,
		Steps: []ops.Step{{StepSpec: ops.StepSpec{Effect: "route.forget-channel", Key: "forget-channel", Spec: string(spec)}}}}
	if err := h.d.restoreChannel(scriptDel); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.d.routeEntry("script"); ok {
		t.Fatal("a script-owned inbox was registered")
	}
}

// forgetAndRestore takes the inbox away and forgets its channel as a delete
// does, puts the inbox back as undo does, and restores the channel.
func forgetAndRestore(t *testing.T, h *harness, del *ops.Operation, inbox string) {
	t.Helper()
	name := ""
	if ch, ok := route.ForgottenChannelOf(del); ok {
		name = ch.Route
	} else {
		t.Fatal("the delete forgets no channel")
	}
	aside := inbox + ".undo-test"
	if err := os.Rename(inbox, aside); err != nil {
		t.Fatal(err)
	}
	if err := (routeChannels{h.d}).Forget(context.Background(), name, inbox); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.d.routeEntry(name); ok {
		t.Fatal("the delete left the channel")
	}
	if err := os.Rename(aside, inbox); err != nil {
		t.Fatal(err)
	}
	if err := h.d.restoreChannel(del); err != nil {
		t.Fatal(err)
	}
}
