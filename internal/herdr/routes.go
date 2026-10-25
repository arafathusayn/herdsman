package herdr

import (
	"context"
	"errors"
)

// The calls a new route needs: a tab for the coordinator, its agent started
// through herdr (so it can enroll in the message channel), and a pane beside
// it for the checkpointer. Each is one request; the caller orders them.

// WorkspaceWorktree is the git checkout herdr found for a workspace.
type WorkspaceWorktree struct {
	RepoKey          string `json:"repo_key"`
	RepoName         string `json:"repo_name"`
	RepoRoot         string `json:"repo_root"`
	CheckoutPath     string `json:"checkout_path"`
	IsLinkedWorktree bool   `json:"is_linked_worktree"`
}

// TabCreate opens a tab in a workspace without taking the user's focus and
// returns the new tab's id and its root pane's id.
func (c *Client) TabCreate(ctx context.Context, workspaceID, cwd, label string) (tabID, paneID string, err error) {
	var r struct {
		Tab struct {
			TabID string `json:"tab_id"`
		} `json:"tab"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	params := map[string]any{"workspace_id": workspaceID, "cwd": cwd, "label": label, "focus": false}
	if err := c.Call(ctx, "tab.create", params, &r); err != nil {
		return "", "", err
	}
	if r.Tab.TabID == "" || r.RootPane.PaneID == "" {
		return r.Tab.TabID, "", errors.New("tab.create: herdr answered without a tab or root pane")
	}
	return r.Tab.TabID, r.RootPane.PaneID, nil
}

// AgentStart starts an agent of a supported kind in a pane at its shell
// prompt and waits until herdr sees it ready for input. Each arg is one
// argv element: herdr runs no shell over them.
func (c *Client) AgentStart(ctx context.Context, name, kind, paneID string, args []string, timeoutMS int) (Agent, error) {
	if args == nil {
		args = []string{}
	}
	var r struct {
		Agent Agent `json:"agent"`
	}
	params := map[string]any{"name": name, "kind": kind, "pane_id": paneID, "args": args, "timeout_ms": timeoutMS}
	err := c.Call(ctx, "agent.start", params, &r)
	return r.Agent, err
}

// PaneSplit splits a pane without taking the focus and returns the new
// pane's id. An empty cwd keeps herdr's default.
func (c *Client) PaneSplit(ctx context.Context, target, direction, cwd string) (string, error) {
	var r struct {
		Pane struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	}
	params := map[string]any{"target_pane_id": target, "direction": direction, "focus": false}
	if cwd != "" {
		params["cwd"] = cwd
	}
	if err := c.Call(ctx, "pane.split", params, &r); err != nil {
		return "", err
	}
	if r.Pane.PaneID == "" {
		return "", errors.New("pane.split: herdr answered without the new pane")
	}
	return r.Pane.PaneID, nil
}
