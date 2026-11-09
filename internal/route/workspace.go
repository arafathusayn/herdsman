package route

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
)

func workspaceID(snapshot herdr.Snapshot, label string) string {
	for _, w := range snapshot.Workspaces {
		if w.Label == label {
			return w.ID
		}
	}
	return ""
}

func applyWorkspace(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return err
	}
	if workspaceID(snapshot, d.Workspace) != "" {
		return nil
	}
	env, err := paneEnv(deps, d)
	if err != nil {
		return err
	}
	params := map[string]any{"label": d.Workspace, "cwd": d.Repo, "focus": false, "env": env}
	return c.Call(ctx, "workspace.create", params, nil)
}

func probeWorkspace(ctx context.Context, _ Deps, c *herdr.Client, d stepData) (ops.Verdict, string, error) {
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return ops.Unknown, "", err
	}
	if workspaceID(snapshot, d.Workspace) != "" {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

func labelOf(p herdr.Pane) string {
	if p.Label == nil {
		return ""
	}
	return *p.Label
}

// groupLabels names the panes that share a tab.
var groupLabels = map[string][]string{
	"coordinators": {roleAdjutant, roleOrchestrator, labelCheckpoint},
	"services":     {labelCourier, labelCardWatch},
}

// ensurePane finds the pane with the step's label or makes it: an unlabeled
// pane is the one a crash left before its rename, a pane of the same group
// splits, and otherwise the group gets a new tab.
func ensurePane(ctx context.Context, deps Deps, c *herdr.Client, d stepData) (string, error) {
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	workspace := workspaceID(snapshot, d.Workspace)
	if workspace == "" {
		return "", fmt.Errorf("the workspace %s is gone", d.Workspace)
	}
	var unlabeled, sibling string
	for _, p := range snapshot.Panes {
		if p.WorkspaceID != workspace {
			continue
		}
		switch label := labelOf(p); {
		case label == d.Label:
			return p.ID, nil
		case label == "":
			if unlabeled == "" {
				unlabeled = p.ID
			}
		case inGroup(d.Group, label):
			sibling = p.ID
		}
	}
	pane := unlabeled
	var env map[string]string
	if pane == "" {
		if env, err = paneEnv(deps, d); err != nil {
			return "", err
		}
	}
	switch {
	case pane != "":
	case sibling != "":
		var r struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			} `json:"pane"`
		}
		params := map[string]any{"target_pane_id": sibling, "direction": "right", "cwd": d.Repo, "focus": false, "env": env}
		if err := c.Call(ctx, "pane.split", params, &r); err != nil {
			return "", err
		}
		pane = r.Pane.PaneID
	default:
		var r struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		}
		params := map[string]any{"workspace_id": workspace, "cwd": d.Repo, "label": d.Group, "focus": false, "env": env}
		if err := c.Call(ctx, "tab.create", params, &r); err != nil {
			return "", err
		}
		pane = r.RootPane.PaneID
	}
	if pane == "" {
		return "", errors.New("herdr answered without a pane id")
	}
	if err := c.Call(ctx, "pane.rename", map[string]any{"pane_id": pane, "label": d.Label}, nil); err != nil {
		return "", err
	}
	return pane, nil
}

func inGroup(group, label string) bool {
	for _, l := range groupLabels[group] {
		if l == label {
			return true
		}
	}
	return false
}

func applyPane(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	_, err := ensurePane(ctx, deps, c, d)
	return err
}

func probePane(ctx context.Context, _ Deps, c *herdr.Client, d stepData) (ops.Verdict, string, error) {
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return ops.Unknown, "", err
	}
	workspace := workspaceID(snapshot, d.Workspace)
	for _, p := range snapshot.Panes {
		if p.WorkspaceID == workspace && labelOf(p) == d.Label {
			return ops.Matches, "", nil
		}
	}
	return ops.Absent, "", nil
}

// sendLine submits one line in one call, so that no other input interleaves.
func sendLine(ctx context.Context, c *herdr.Client, pane, line string) error {
	return c.Call(ctx, "pane.send_input", map[string]any{"pane_id": pane, "text": line, "keys": []string{"enter"}}, nil)
}

func serviceStamp(d stepData) string {
	return filepath.Join(d.Folder, ".steps", "service-"+d.Label)
}

func applyService(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	if _, err := os.Stat(serviceStamp(d)); err == nil {
		return nil
	}
	pane, err := ensurePane(ctx, deps, c, d)
	if err != nil {
		return err
	}
	line := d.Line
	if d.Label == labelCheckpoint {
		record, err := c.AgentGet(ctx, d.Name)
		if err != nil {
			return fmt.Errorf("agent %s: %w", d.Name, err)
		}
		line = "TARGET=" + record.PaneID + " INTERVAL=1800 LIMIT=1000000 " + line
	}
	if err := sendLine(ctx, c, pane, line); err != nil {
		return err
	}
	return writeFileNew(serviceStamp(d), nil)
}

func probeService(ctx context.Context, deps Deps, c *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(serviceStamp(d)); err == nil {
		return ops.Matches, "", nil
	}
	v, _, err := probePane(ctx, deps, c, d)
	if err != nil {
		return ops.Unknown, "", err
	}
	if v == ops.Absent {
		return ops.Absent, "", nil
	}
	return ops.Unknown, "the " + d.Label + " pane exists but its start is not recorded", nil
}
