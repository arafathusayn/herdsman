package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
)

// coreParty records a registered core execution of route harbor whose process
// is a fresh pid of the fake process table.
func coreParty(t *testing.T, h *harness, w *coretest.World, repo, id, agent, role string, gen int64, kind ...string) runtime.Execution {
	t.Helper()
	harness := "claude"
	if len(kind) > 0 {
		harness = kind[0]
	}
	pid := w.Procs.Add(1, harness)
	start, _ := w.Procs.Identify(pid)
	e := runtime.Execution{ID: id, Repo: repo, Route: "harbor", Agent: agent, Role: role, Backend: runtime.BackendCore, HarnessKind: harness,
		LaunchIntent: "i-" + id, Generation: gen, Desired: runtime.DesiredRunning, EndpointID: w.Core.ID,
		TerminalAddress: w.Core.Add(map[string]string{herdrapp.LabelExecution: id}), Evidence: runtime.ProcessEvidence{PID: pid, Start: core.StartString(start)},
		Observation: liveObservation()}
	if err := h.d.recordExecution(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	return e
}

// as places this test process below an execution's process, so every call it
// makes is that execution's; pid 1 places it outside every execution.
func as(w *coretest.World, pid int) { w.Procs.Put(os.Getpid(), pid, "herdsman", "message") }

const (
	execAdj  = "1a2b3c4d-0000-4000-8000-00000000000a"
	execOrc  = "1a2b3c4d-0000-4000-8000-00000000000b"
	execAdj2 = "1a2b3c4d-0000-4000-8000-00000000000c"
)

// Pair boundary, on core: the route's channel is the daemon's, a party is the
// execution its process belongs to, an agent cannot change the attested pair,
// a replaced execution's binding fails and is bound again only by up, a view
// change keeps the pair, and the gate reads the core inventory.
func TestCoreChannelCarriesThePairByExecution(t *testing.T) {
	h, w := newCoreHarness(t)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	adj := coreParty(t, h, w, repo, execAdj, "proj-harbor-adjutant", "adjutant", 1)
	orc := coreParty(t, h, w, repo, execOrc, "proj-harbor-orchestrator", "orchestrator", 1)
	ch := coreChannel{h.d}
	box := filepath.Join(h.ph, "proj-harbor")
	if err := ch.Open(ctx, route.ChannelOpen{Route: "harbor", Repo: repo, Inbox: box, PolicyFiles: []string{h.rules}}); err != nil {
		t.Fatal(err)
	}
	if err := ch.Open(ctx, route.ChannelOpen{Route: "harbor", Repo: repo, Inbox: box, PolicyFiles: []string{h.rules}}); err != nil {
		t.Fatalf("a repeat open: %v", err)
	}
	if !inbox.DaemonOwned(box) {
		t.Fatal("the core route's inbox is not daemon-owned")
	}
	if !h.d.channelOnCore("harbor") {
		t.Fatal("the channel is not on core")
	}
	for _, e := range []runtime.Execution{adj, orc} {
		if err := ch.Enroll(ctx, "harbor", e.Agent, e.Role, e); err != nil {
			t.Fatal(err)
		}
	}
	// An attested pair that is not the pending one is never approved.
	if err := ch.Approve(ctx, "harbor", []runtime.Execution{adj}); err == nil || !strings.Contains(err.Error(), "not the one up attested") {
		t.Fatalf("a partial pair approved: %v", err)
	}
	if err := ch.Approve(ctx, "harbor", []runtime.Execution{adj, orc}); err != nil {
		t.Fatal(err)
	}
	if !ch.Approved("harbor") {
		t.Fatal("not approved")
	}
	if err := ch.Handshake(ctx, "harbor", adj.Agent, orc.Agent, "handshake harbor\n"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Handshake(ctx, "harbor", adj.Agent, orc.Agent, "handshake harbor\n"); err != nil {
		t.Fatalf("a retried handshake: %v", err)
	}

	party := func(name string) *Client {
		c := NewClient(SocketPath(h.ph), testVersion)
		c.Name = name
		t.Cleanup(func() { c.Close() })
		return c
	}
	// The adjutant's process sends; no pane, no bearer value.
	as(w, adj.Evidence.PID)
	if _, err := party(adj.Agent).Send(ctx, "harbor", SendRequest{To: orc.Agent, Kind: "observation", Body: "hello\n", Key: "00000000000000000000000000000001"}); err != nil {
		t.Fatalf("the adjutant's send: %v", err)
	}
	// The same claim by another process is refused: a name is a hint.
	if _, err := party(orc.Agent).Send(ctx, "harbor", SendRequest{To: adj.Agent, Kind: "observation", Body: "forged\n", Key: "00000000000000000000000000000002"}); err == nil || !strings.Contains(err.Error(), "identity_mismatch") {
		t.Fatalf("the adjutant's process sent as the orchestrator: %v", err)
	}
	// A process outside every execution (a recovery shell, a person's
	// shell) is no party.
	as(w, 1)
	if _, err := party(adj.Agent).Send(ctx, "harbor", SendRequest{To: orc.Agent, Kind: "observation", Body: "shell\n", Key: "00000000000000000000000000000003"}); err == nil || !strings.Contains(err.Error(), "no registered execution") {
		t.Fatalf("a shell sent as the adjutant: %v", err)
	}
	// The orchestrator's process reads the handshake and the message.
	as(w, orc.Evidence.PID)
	got, err := party(orc.Agent).Claim(ctx, "harbor", ClaimRequest{ReadKey: "00000000000000000000000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Manifest) != 2 {
		t.Fatalf("the orchestrator read %d messages: %+v", len(got.Manifest), got.Manifest)
	}

	// A view change (the agent moved in the app) keeps the pair.
	moved := adj
	moved.App = &runtime.AppBinding{CoreID: w.Core.ID, SpaceID: "s-2", TabID: "t-2", Visibility: runtime.VisibilityVerified}
	if err := h.d.recordExecution(ctx, moved); err != nil {
		t.Fatal(err)
	}
	as(w, adj.Evidence.PID)
	if _, err := party(adj.Agent).Send(ctx, "harbor", SendRequest{To: orc.Agent, Kind: "observation", Body: "moved\n", Key: "00000000000000000000000000000004"}); err != nil {
		t.Fatalf("a moved view lost the pair: %v", err)
	}

	// The adjutant is replaced: the new execution is another identity, so
	// the stale binding fails until up binds the name again.
	stopped := moved
	stopped.Desired = runtime.DesiredStopped
	if err := h.d.recordExecution(ctx, stopped); err != nil {
		t.Fatal(err)
	}
	adj2 := coreParty(t, h, w, repo, execAdj2, adj.Agent, "adjutant", 2)
	as(w, adj2.Evidence.PID)
	if _, err := party(adj.Agent).Send(ctx, "harbor", SendRequest{To: orc.Agent, Kind: "observation", Body: "new\n", Key: "00000000000000000000000000000005"}); err == nil || !strings.Contains(err.Error(), "identity_mismatch") {
		t.Fatalf("a replacement used the old binding: %v", err)
	}
	// A name bound to a running execution is never taken.
	if err := ch.Enroll(ctx, "harbor", orc.Agent, "orchestrator", adj2); err == nil {
		t.Fatal("a running execution's name was taken")
	}
	if err := ch.Enroll(ctx, "harbor", adj.Agent, "adjutant", adj2); err != nil {
		t.Fatalf("rebind after replacement: %v", err)
	}
	if ch.Approved("harbor") {
		t.Fatal("a rebound pair is approved before up attests it")
	}
	if err := ch.Approve(ctx, "harbor", []runtime.Execution{adj2, orc}); err != nil {
		t.Fatal(err)
	}
	if _, err := party(adj.Agent).Send(ctx, "harbor", SendRequest{To: orc.Agent, Kind: "observation", Body: "new\n", Key: "00000000000000000000000000000006"}); err != nil {
		t.Fatalf("after the rebind: %v", err)
	}
	// The old execution's process is now refused: its late report fails.
	as(w, adj.Evidence.PID)
	if _, err := party(adj.Agent).Send(ctx, "harbor", SendRequest{To: orc.Agent, Kind: "observation", Body: "late\n", Key: "00000000000000000000000000000007"}); err == nil {
		t.Fatal("the replaced execution's late send was accepted")
	}

	// Hold and unhold through the channel; a repeat unhold is fine.
	if err := ch.Hold(ctx, "harbor", orc.Agent, true); err != nil || !ch.Held("harbor", orc.Agent) {
		t.Fatalf("hold: %v", err)
	}
	if err := ch.Hold(ctx, "harbor", orc.Agent, false); err != nil || ch.Held("harbor", orc.Agent) {
		t.Fatalf("unhold: %v", err)
	}
	if err := ch.Hold(ctx, "harbor", orc.Agent, false); err != nil {
		t.Fatalf("a repeat unhold: %v", err)
	}

	// The gate's inventory on core: a terminal with the route's labels and
	// no registration is a fault the setup inventory names.
	labels := map[string]string{}
	for k, v := range w.Core.Terminal(orc.TerminalAddress)["labels"].(map[string]string) {
		labels[k] = v
	}
	labels[herdrapp.LabelExecution] = "e-copied"
	labels[herdrapp.LabelRoute] = core.UUIDOf("x")
	impostor := w.Core.Add(labels)
	_ = impostor
	var setup SetupInventory
	if e := h.d.getSetup(ctx, "harbor", &setup); e != nil {
		t.Fatal(e)
	}
	if setup.Proof == nil {
		t.Error("no proof on a core channel")
	}
	for _, a := range setup.Inventory {
		if a.Pane == adj2.TerminalAddress || a.Pane == orc.TerminalAddress {
			t.Errorf("a member is listed as unknown: %+v", a)
		}
	}
	if n := w.Core.Calls("terminal.send") + w.Core.Calls("terminal.keys"); n != 0 {
		t.Errorf("the channel typed %d times", n)
	}
}

// Status forgery: a shell in a terminal that carries an execution's labels
// and a status record it printed itself (an OSC 7501 "idle") is no
// execution: it is not a party of the channel and not a notice caller. (The
// doctor test names such a terminal as an inventory fault.)
func TestCoreForgedStatusGivesAShellNothing(t *testing.T) {
	h, w := newCoreHarness(t)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	adj := coreParty(t, h, w, repo, execAdj, "proj-harbor-adjutant", "adjutant", 1)
	orc := coreParty(t, h, w, repo, execOrc, "proj-harbor-orchestrator", "orchestrator", 1)
	ch := coreChannel{h.d}
	if err := ch.Open(ctx, route.ChannelOpen{Route: "harbor", Repo: repo, Inbox: filepath.Join(h.ph, "proj-harbor"), PolicyFiles: []string{h.rules}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []runtime.Execution{adj, orc} {
		if err := ch.Enroll(ctx, "harbor", e.Agent, e.Role, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := ch.Approve(ctx, "harbor", []runtime.Execution{adj, orc}); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for k, v := range w.Core.Terminal(orc.TerminalAddress)["labels"].(map[string]string) {
		labels[k] = v
	}
	forged := w.Core.Add(labels) // the orchestrator's labels, copied
	shell := w.Procs.Add(1, "sh")
	w.SetProcess(forged, shell)
	w.Core.SetStatus(forged, "idle") // the record the shell printed
	if _, ok := h.d.coreRT().callerExecution(ctx, shell); ok {
		t.Fatal("a shell with a forged status is an execution")
	}
	as(w, shell)
	c := NewClient(SocketPath(h.ph), testVersion)
	c.Name = orc.Agent
	t.Cleanup(func() { c.Close() })
	if _, err := c.Send(ctx, "harbor", SendRequest{To: adj.Agent, Kind: "observation", Body: "forged\n", Key: "0000000000000000000000000000000f"}); err == nil {
		t.Fatal("a shell with a forged status sent as the orchestrator")
	}
	// The notice service answers only a registered execution's process.
	if _, ok := h.d.coreRT().callerExecution(ctx, os.Getpid()); ok {
		t.Fatal("a process under a forged terminal is a notice caller")
	}
	if n := w.Core.Calls("terminal.send") + w.Core.Calls("terminal.keys"); n != 0 {
		t.Errorf("a forged status drew %d typed inputs", n)
	}
}

// resume-goal on core types nothing: the daemon raises needs_attention with
// reason goal_paused and the line, and the answer is held.
func TestCoreResumeGoalRaisesAttentionAndTypesNothing(t *testing.T) {
	h, w := newCoreHarness(t)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	adj := coreParty(t, h, w, repo, execAdj, "proj-harbor-adjutant", "adjutant", 1)
	orc := coreParty(t, h, w, repo, execOrc, "proj-harbor-orchestrator", "orchestrator", 1, "codex")
	ch := coreChannel{h.d}
	if err := ch.Open(ctx, route.ChannelOpen{Route: "harbor", Repo: repo, Inbox: filepath.Join(h.ph, "proj-harbor"), PolicyFiles: []string{h.rules}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []runtime.Execution{adj, orc} {
		if err := ch.Enroll(ctx, "harbor", e.Agent, e.Role, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := ch.Approve(ctx, "harbor", []runtime.Execution{adj, orc}); err != nil {
		t.Fatal(err)
	}
	c := NewClient(SocketPath(h.ph), testVersion)
	c.Name = adj.Agent
	t.Cleanup(func() { c.Close() })
	as(w, adj.Evidence.PID)
	if _, err := c.ResumeGoal(ctx, "harbor", adj.Agent); err == nil || !strings.Contains(err.Error(), "adjutant") {
		t.Fatalf("the adjutant was a resume target: %v", err)
	}
	got, err := c.ResumeGoal(ctx, "harbor", orc.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "held" || got.Reason != core.AttentionGoalPaused || got.Line != core.GoalResumeLine || got.Execution != orc.ID {
		t.Fatalf("resume-goal: %+v", got)
	}
	seen := false
	for _, ev := range runtimeEvents(t, h) {
		if ev.GetKind() == "needs_attention" && ev.GetReason() == core.AttentionGoalPaused && ev.GetLine() == core.GoalResumeLine && ev.GetExecutionId() == orc.ID {
			seen = true
		}
	}
	if !seen {
		t.Errorf("no needs_attention goal_paused event: %+v", runtimeEvents(t, h))
	}
	// A shell outside every execution is no party.
	as(w, 1)
	if _, err := c.ResumeGoal(ctx, "harbor", orc.Agent); err == nil {
		t.Fatal("a shell asked for a resume")
	}
	if n := w.Core.Calls("terminal.send") + w.Core.Calls("terminal.keys"); n != 0 {
		t.Errorf("resume-goal typed %d times", n)
	}
}
