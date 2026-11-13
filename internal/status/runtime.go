package status

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Agent is one execution of a route as status shows it (herdr-core backend
// spec, sections 9 and 16). Each field comes from its own evidence and none is
// derived from another: a "done" program says nothing about the task, a live
// process is not ready by itself, and an attention reason is not a readiness.
type Agent struct {
	Agent   string `json:"agent"`
	Role    string `json:"role,omitempty"`
	Backend string `json:"backend"`
	// Execution and Generation identify the harness launch; Address is the
	// core terminal address or the TUI pane, never identity.
	Execution  string `json:"execution_id,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	Address    string `json:"address,omitempty"`
	// Source is "record" or "legacy" (a start journaled before execution
	// records existed).
	Source string `json:"source,omitempty"`

	Process    string `json:"process,omitempty"`
	Readiness  string `json:"readiness,omitempty"`
	Program    string `json:"program,omitempty"`
	RawProgram string `json:"raw_program_status,omitempty"`
	// Visibility is the app placement's state; App whether the app is open.
	Visibility    string    `json:"visibility,omitempty"`
	App           string    `json:"app,omitempty"`
	Orchestration string    `json:"orchestration,omitempty"`
	LastVerified  time.Time `json:"last_verified,omitzero"`

	Attention  []Attention  `json:"attention,omitempty"`
	Unresolved []Unresolved `json:"unresolved,omitempty"`
	// Notices is nil for a backend without notices.
	Notices *Notices `json:"notices,omitempty"`
	// Actions are the controls the backend allows for this execution.
	Actions []string `json:"actions,omitempty"`
}

// Attention is one reason an execution waits on a person.
type Attention struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	Notice string `json:"notice_id,omitempty"`
	Gate   string `json:"gate,omitempty"`
	Line   string `json:"line,omitempty"`
}

// Unresolved is one thing whose outcome nobody can tell.
type Unresolved struct {
	Kind    string   `json:"kind"`
	ID      string   `json:"id"`
	State   string   `json:"state"`
	Detail  string   `json:"detail,omitempty"`
	Choices []string `json:"choices,omitempty"`
}

// Notices counts an execution's notices by state.
type Notices struct {
	Issued    int    `json:"issued"`
	Claimed   int    `json:"claimed"`
	Handled   int    `json:"handled"`
	Returned  int    `json:"returned"`
	OpenClaim string `json:"open_claim,omitempty"`
	HeldGate  string `json:"held_gate,omitempty"`
}

// WriteRuntime prints a route's executions, one block each. A line that needs
// a person ends in the "  <-- " flag the agent snapshot uses, so Flags reads
// both the same way.
func WriteRuntime(w io.Writer, agents []Agent) {
	if len(agents) == 0 {
		fmt.Fprintln(w, "executions: none")
		return
	}
	for _, a := range agents {
		id := a.Source
		if a.Execution != "" {
			id = fmt.Sprintf("%s gen %d", a.Execution, a.Generation)
		}
		fmt.Fprintf(w, "== %s (%s, %s, %s)\n", a.Agent, or(a.Role, "-"), a.Backend, id)
		if a.Address != "" {
			fmt.Fprintf(w, "   address: %s\n", a.Address)
		}
		flag := ""
		if a.Process == "lost" || a.Process == "replaced" {
			flag = "  <-- " + strings.ToUpper(a.Process)
		}
		fmt.Fprintf(w, "   process: %s%s\n", or(a.Process, "-"), flag)
		fmt.Fprintf(w, "   readiness: %s\n", or(a.Readiness, "-"))
		program := or(a.Program, "-")
		if a.RawProgram != "" && a.RawProgram != a.Program {
			program += " (reported " + a.RawProgram + ")"
		}
		fmt.Fprintf(w, "   program: %s\n", program)
		if a.Visibility != "" || a.App != "" {
			flag = ""
			if a.Visibility == "missing" {
				flag = "  <-- NOT VISIBLE"
			}
			app := ""
			if a.App != "" {
				app = ", app " + a.App
			}
			fmt.Fprintf(w, "   visibility: %s%s%s\n", or(a.Visibility, "-"), app, flag)
		}
		if a.Orchestration != "" {
			fmt.Fprintf(w, "   orchestration: %s\n", a.Orchestration)
		}
		if !a.LastVerified.IsZero() {
			fmt.Fprintf(w, "   last verified: %s\n", a.LastVerified.UTC().Format(time.RFC3339))
		}
		for _, r := range a.Attention {
			what := r.Reason
			if r.Gate != "" {
				what += " (" + r.Gate + ")"
			}
			fmt.Fprintf(w, "   attention: %s: %s  <-- NEEDS PERSON %s\n", what, r.Detail, r.Reason)
			if r.Line != "" {
				fmt.Fprintf(w, "      a person types: %s\n", r.Line)
			}
		}
		for _, u := range a.Unresolved {
			fmt.Fprintf(w, "   unresolved: %s %s %s: %s  <-- UNRESOLVED %s\n", u.Kind, u.ID, u.State, u.Detail, u.Kind)
			if len(u.Choices) > 0 {
				fmt.Fprintf(w, "      choices: %s\n", strings.Join(u.Choices, ", "))
			}
		}
		if n := a.Notices; n != nil {
			line := fmt.Sprintf("issued %d, claimed %d, handled %d, returned %d", n.Issued, n.Claimed, n.Handled, n.Returned)
			if n.OpenClaim != "" {
				line += ", open claim " + n.OpenClaim
			}
			if n.HeldGate != "" {
				line += ", held by " + n.HeldGate
			}
			fmt.Fprintf(w, "   notices: %s\n", line)
		}
		if len(a.Actions) > 0 {
			fmt.Fprintf(w, "   actions: %s\n", strings.Join(a.Actions, ", "))
		}
	}
}
