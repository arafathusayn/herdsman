package herdr_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

func TestTabCreateReturnsTabAndRootPane(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("tab.create", func(p map[string]any) (any, string) {
		return map[string]any{"type": "tab_created", "tab": map[string]any{"tab_id": "w1:t9"}, "root_pane": map[string]any{"pane_id": "w1:p9"}}, ""
	})
	tab, pane, err := herdr.New(srv.Socket).TabCreate(context.Background(), "w1", "/tmp/x", "x-orchestrator")
	if err != nil || tab != "w1:t9" || pane != "w1:p9" {
		t.Fatalf("tab=%q pane=%q err=%v", tab, pane, err)
	}
	p := srv.Requests()[len(srv.Requests())-1].Params
	if p["workspace_id"] != "w1" || p["cwd"] != "/tmp/x" || p["label"] != "x-orchestrator" || p["focus"] != false {
		t.Fatalf("params = %v", p)
	}
}

func TestTabCreateRefusesAnAnswerWithoutRootPane(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("tab.create", func(map[string]any) (any, string) {
		return map[string]any{"type": "tab_created", "tab": map[string]any{"tab_id": "w1:t9"}}, ""
	})
	if _, _, err := herdr.New(srv.Socket).TabCreate(context.Background(), "w1", "/tmp", "l"); err == nil {
		t.Fatal("want an error for a missing root pane")
	}
}

func TestTabCreatePassesHerdrError(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("tab.create", func(map[string]any) (any, string) { return nil, "workspace_not_found" })
	_, _, err := herdr.New(srv.Socket).TabCreate(context.Background(), "w9", "/tmp", "l")
	if he, ok := errors.AsType[*herdr.Error](err); !ok || he.Code != "workspace_not_found" {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentStartSendsArgsAndReadsAgent(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("agent.start", func(p map[string]any) (any, string) {
		return map[string]any{"type": "agent_started", "agent": map[string]any{"pane_id": p["pane_id"], "name": p["name"], "interactive_ready": true}, "argv": []string{"claude"}}, ""
	})
	a, err := herdr.New(srv.Socket).AgentStart(context.Background(), "orch-1", "claude", "w1:p9", []string{"--model", "fable"}, 90000)
	if err != nil || !a.InteractiveReady || a.PaneID != "w1:p9" {
		t.Fatalf("agent=%+v err=%v", a, err)
	}
	p := srv.Requests()[len(srv.Requests())-1].Params
	b, _ := json.Marshal(p)
	if string(b) != `{"args":["--model","fable"],"kind":"claude","name":"orch-1","pane_id":"w1:p9","timeout_ms":90000}` {
		t.Fatalf("params = %s", b)
	}
}

func TestAgentStartSendsEmptyArgsAsList(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("agent.start", func(map[string]any) (any, string) {
		return map[string]any{"type": "agent_started", "agent": map[string]any{}}, ""
	})
	if _, err := herdr.New(srv.Socket).AgentStart(context.Background(), "a", "pi", "w1:p1", nil, 90000); err != nil {
		t.Fatal(err)
	}
	if args, ok := srv.Requests()[len(srv.Requests())-1].Params["args"].([]any); !ok || len(args) != 0 {
		t.Fatalf("args = %v", srv.Requests()[len(srv.Requests())-1].Params["args"])
	}
}

func TestAgentStartPassesHerdrError(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("agent.start", func(map[string]any) (any, string) { return nil, "agent_name_taken" })
	_, err := herdr.New(srv.Socket).AgentStart(context.Background(), "a", "claude", "w1:p1", nil, 90000)
	if he, ok := errors.AsType[*herdr.Error](err); !ok || he.Code != "agent_name_taken" {
		t.Fatalf("err = %v", err)
	}
}

func TestPaneSplitReturnsNewPane(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("pane.split", func(map[string]any) (any, string) {
		return map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": "w1:p10"}}, ""
	})
	id, err := herdr.New(srv.Socket).PaneSplit(context.Background(), "w1:p9", "right", "/tmp/x")
	if err != nil || id != "w1:p10" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	p := srv.Requests()[len(srv.Requests())-1].Params
	if p["target_pane_id"] != "w1:p9" || p["direction"] != "right" || p["cwd"] != "/tmp/x" || p["focus"] != false {
		t.Fatalf("params = %v", p)
	}
}

func TestPaneSplitRefusesAnAnswerWithoutPane(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("pane.split", func(map[string]any) (any, string) { return map[string]any{"type": "ok"}, "" })
	if _, err := herdr.New(srv.Socket).PaneSplit(context.Background(), "w1:p9", "right", ""); err == nil {
		t.Fatal("want an error for a missing pane")
	}
}

func TestWorkspaceDecodesWorktree(t *testing.T) {
	var w herdr.Workspace
	raw := `{"workspace_id":"w1","worktree":{"repo_key":"k","repo_name":"herdsman","repo_root":"/r","checkout_path":"/r/wt","is_linked_worktree":true}}`
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	if w.Worktree == nil || w.Worktree.RepoRoot != "/r" || w.Worktree.CheckoutPath != "/r/wt" || !w.Worktree.IsLinkedWorktree {
		t.Fatalf("worktree = %+v", w.Worktree)
	}
}
