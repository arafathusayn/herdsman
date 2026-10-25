package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// The calls the herdsman commands make besides the web app's: the same
// methods the herdr CLI sends for `agent get`, `agent list`, `pane list`,
// `pane read`, `agent prompt`, `tab focus`, `pane report-metadata` and
// `notification show`. Every call opens its own connection (see Call).

// AgentRecord is one agent as `agent get` and `agent list` return it: the
// parsed fields the commands use, and the raw object, so a caller can check
// field types strictly (the inbox identity check does).
type AgentRecord struct {
	Agent
	TerminalID   *string         `json:"terminal_id"`
	Revision     *json.Number    `json:"revision"`
	AgentSession json.RawMessage `json:"agent_session"`
	// Raw is the whole agent object as the server sent it.
	Raw json.RawMessage `json:"-"`
}

func decodeAgent(raw json.RawMessage) (AgentRecord, error) {
	var a AgentRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&a); err != nil {
		return AgentRecord{}, err
	}
	a.Raw = append(json.RawMessage(nil), raw...)
	return a, nil
}

// AgentGet reads one agent by name or pane id ("agent.get").
func (c *Client) AgentGet(ctx context.Context, target string) (AgentRecord, error) {
	var r struct {
		Agent json.RawMessage `json:"agent"`
	}
	if err := c.Call(ctx, "agent.get", map[string]any{"target": target}, &r); err != nil {
		return AgentRecord{}, err
	}
	if len(r.Agent) == 0 || string(r.Agent) == "null" {
		return AgentRecord{}, errors.New("agent.get: no agent in the response")
	}
	return decodeAgent(r.Agent)
}

// AgentList reads every agent of the server ("agent.list"). An empty or
// missing list is an empty slice; a list that is not an array is an error.
func (c *Client) AgentList(ctx context.Context) ([]AgentRecord, error) {
	var r struct {
		Agents []json.RawMessage `json:"agents"`
	}
	if err := c.Call(ctx, "agent.list", nil, &r); err != nil {
		return nil, err
	}
	out := make([]AgentRecord, 0, len(r.Agents))
	for _, raw := range r.Agents {
		a, err := decodeAgent(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// PaneList reads every pane of the server ("pane.list").
func (c *Client) PaneList(ctx context.Context) ([]Pane, error) {
	var r struct {
		Panes []Pane `json:"panes"`
	}
	err := c.Call(ctx, "pane.list", nil, &r)
	return r.Panes, err
}

// Source is where a pane read takes its text from.
type Source string

const (
	// SourceVisible is the screen as herdr shows it (`--source visible`).
	SourceVisible Source = "visible"
	// SourceRecentUnwrapped is the last lines, unwrapped (`--source recent-unwrapped`).
	SourceRecentUnwrapped Source = "recent_unwrapped"
)

// ReadText reads a pane's plain text from source, at most lines lines (0:
// the server's default), as `herdr pane read <pane> --source <s> --lines <n>`.
func (c *Client) ReadText(ctx context.Context, paneID string, source Source, lines int) (string, error) {
	params := map[string]any{"pane_id": paneID, "source": string(source), "strip_ansi": true}
	if lines > 0 {
		params["lines"] = lines
	}
	var r struct {
		Read PaneRead `json:"read"`
	}
	err := c.Call(ctx, "pane.read", params, &r)
	return r.Read.Text, err
}

// AgentPrompt types text into an agent and submits it ("agent.prompt").
func (c *Client) AgentPrompt(ctx context.Context, target, text string) error {
	return c.Call(ctx, "agent.prompt", map[string]any{"target": target, "text": text}, nil)
}

// TabFocus focuses a tab ("tab.focus").
func (c *Client) TabFocus(ctx context.Context, tabID string) error {
	return c.Call(ctx, "tab.focus", map[string]any{"tab_id": tabID}, nil)
}

// Metadata is a pane's reported display metadata ("pane.report_metadata").
// A token set to nil is cleared (`--clear-token NAME`).
type Metadata struct {
	PaneID string
	Source string
	Tokens map[string]*string
}

// ReportMetadata reports display metadata for a pane.
func (c *Client) ReportMetadata(ctx context.Context, m Metadata) error {
	params := map[string]any{"pane_id": m.PaneID, "source": m.Source}
	if len(m.Tokens) > 0 {
		params["tokens"] = m.Tokens
	}
	return c.Call(ctx, "pane.report_metadata", params, nil)
}

// Notification is a desktop notification ("notification.show").
type Notification struct {
	Title string
	Body  string
	Sound string // "", "none", "done" or "request"
}

// ShowNotification shows a notification and reports whether herdr showed it
// (it may not, for example when notifications are off).
func (c *Client) ShowNotification(ctx context.Context, n Notification) (bool, error) {
	params := map[string]any{"title": n.Title}
	if n.Body != "" {
		params["body"] = n.Body
	}
	if n.Sound != "" {
		params["sound"] = n.Sound
	}
	var r struct {
		Shown bool `json:"shown"`
	}
	err := c.Call(ctx, "notification.show", params, &r)
	return r.Shown, err
}

// ErrWaitTimeout is returned by WaitAgentStatus when the time runs out.
var ErrWaitTimeout = errors.New("herdr: timed out waiting for the agent status")

// WaitAgentStatus waits until the agent target shows one of the statuses,
// or timeout passes (0: no limit beyond ctx). It reads the agent for its
// pane, subscribes to pane.agent_status_changed for that pane, and reads
// the agent again after the subscription starts and after each event, so a
// change between the reads is never missed and the event's own shape does
// not matter.
func (c *Client) WaitAgentStatus(ctx context.Context, target string, want []Status, timeout time.Duration) (Status, error) {
	a, err := c.AgentGet(ctx, target)
	if err != nil {
		return "", err
	}
	if slices.Contains(want, a.Status) {
		return a.Status, nil
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if timeout > 0 {
		var tcancel context.CancelFunc
		wctx, tcancel = context.WithTimeout(wctx, timeout)
		defer tcancel()
	}
	fail := func(err error) (Status, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if wctx.Err() != nil {
			return "", ErrWaitTimeout
		}
		return "", err
	}
	hits := make(chan struct{}, 1)
	ready := make(chan struct{})
	errc := make(chan error, 1)
	subs := []map[string]any{{"type": "pane.agent_status_changed", "pane_id": a.PaneID}}
	go func() {
		errc <- c.subscribe(wctx, subs, func() { close(ready) }, func(Event) {
			select {
			case hits <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-ready:
	case err := <-errc:
		return fail(err)
	}
	for {
		a, err := c.AgentGet(wctx, target)
		if err != nil {
			return fail(err)
		}
		if slices.Contains(want, a.Status) {
			return a.Status, nil
		}
		select {
		case <-hits:
		case err := <-errc:
			// The subscription also ends when wctx does: fail maps that
			// to the caller's error or ErrWaitTimeout.
			return fail(err)
		}
	}
}
