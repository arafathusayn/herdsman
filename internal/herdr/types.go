package herdr

import "context"

// Status is an agent state as herdr detects it. herdr can be wrong (Codex
// shows working at its prompt), so the pane text stays the truth.
type Status string

type Workspace struct {
	ID          string `json:"workspace_id"`
	Number      int    `json:"number"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	ActiveTabID string `json:"active_tab_id"`
	AgentStatus Status `json:"agent_status"`
	// Worktree is the git checkout herdr found for the workspace, if any.
	Worktree *WorkspaceWorktree `json:"worktree,omitempty"`
}

type Tab struct {
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Number      int    `json:"number"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	PaneCount   int    `json:"pane_count"`
	AgentStatus Status `json:"agent_status"`
}

type Pane struct {
	ID            string  `json:"pane_id"`
	WorkspaceID   string  `json:"workspace_id"`
	TabID         string  `json:"tab_id"`
	Focused       bool    `json:"focused"`
	Label         *string `json:"label"`
	Cwd           *string `json:"cwd"`
	ForegroundCwd *string `json:"foreground_cwd"`
	Title         *string `json:"terminal_title_stripped"`
	Agent         *string `json:"agent"`
	AgentStatus   Status  `json:"agent_status"`
	Revision      uint64  `json:"revision"`
	Scroll        *Scroll `json:"scroll,omitempty"`
}

// Scroll is where a pane's view sits in herdr's scrollback. A full-screen
// program (an agent's UI, an editor) has no scrollback, so Max is 0.
type Scroll struct {
	Offset uint64 `json:"offset_from_bottom"`
	Max    uint64 `json:"max_offset_from_bottom"`
	Rows   uint64 `json:"viewport_rows"`
}

type Agent struct {
	PaneID           string  `json:"pane_id"`
	TabID            string  `json:"tab_id"`
	WorkspaceID      string  `json:"workspace_id"`
	Name             *string `json:"name"`
	Agent            *string `json:"agent"`
	DisplayAgent     *string `json:"display_agent"`
	Status           Status  `json:"agent_status"`
	Focused          bool    `json:"focused"`
	Cwd              *string `json:"cwd"`
	Title            *string `json:"terminal_title_stripped"`
	InteractiveReady bool    `json:"interactive_ready"`
	LaunchPending    bool    `json:"launch_pending"`
	StateChangeSeq   uint64  `json:"state_change_seq"`
}

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type LayoutPane struct {
	PaneID  string `json:"pane_id"`
	Focused bool   `json:"focused"`
	Rect    Rect   `json:"rect"`
}

type Layout struct {
	WorkspaceID string       `json:"workspace_id"`
	TabID       string       `json:"tab_id"`
	Zoomed      bool         `json:"zoomed"`
	Area        Rect         `json:"area"`
	Panes       []LayoutPane `json:"panes"`
}

type Snapshot struct {
	Version            string      `json:"version"`
	Protocol           int         `json:"protocol"`
	FocusedWorkspaceID *string     `json:"focused_workspace_id"`
	FocusedTabID       *string     `json:"focused_tab_id"`
	FocusedPaneID      *string     `json:"focused_pane_id"`
	Workspaces         []Workspace `json:"workspaces"`
	Tabs               []Tab       `json:"tabs"`
	Panes              []Pane      `json:"panes"`
	Layouts            []Layout    `json:"layouts"`
	Agents             []Agent     `json:"agents"`
}

func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	var r struct {
		Snapshot Snapshot `json:"snapshot"`
	}
	err := c.Call(ctx, "session.snapshot", nil, &r)
	return r.Snapshot, err
}

type PaneRead struct {
	PaneID    string `json:"pane_id"`
	Text      string `json:"text"`
	Revision  uint64 `json:"revision"`
	Truncated bool   `json:"truncated"`
}

// ReadOptions picks what ReadPane returns.
type ReadOptions struct {
	// Lines is how many of the last lines to read; Visible ignores it.
	Lines int
	// ANSI keeps color and style codes (SGR sequences only, as herdr's
	// "ansi" format gives them); without it the text is plain.
	ANSI bool
	// Visible reads the screen as herdr shows it, at its scroll position,
	// instead of the last lines unwrapped.
	Visible bool
}

// ReadPane reads a pane's text.
func (c *Client) ReadPane(ctx context.Context, paneID string, o ReadOptions) (PaneRead, error) {
	var r struct {
		Read PaneRead `json:"read"`
	}
	params := map[string]any{"pane_id": paneID, "source": "recent_unwrapped", "lines": o.Lines, "strip_ansi": true}
	if o.Visible {
		params["source"] = "visible"
		delete(params, "lines")
	}
	if o.ANSI {
		params["strip_ansi"] = false
		params["format"] = "ansi"
	}
	err := c.Call(ctx, "pane.read", params, &r)
	return r.Read, err
}

// Pane reads one pane as herdr sees it now, with its scroll position.
func (c *Client) Pane(ctx context.Context, paneID string) (Pane, error) {
	var r struct {
		Pane Pane `json:"pane"`
	}
	err := c.Call(ctx, "pane.get", map[string]any{"pane_id": paneID}, &r)
	return r.Pane, err
}
