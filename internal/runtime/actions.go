package runtime

import "slices"

// The controls a person's surface (the web, a status page) can offer for one
// execution. Each backend has its own explicit list; a surface offers nothing
// outside it, and an action a backend does not list is refused by name rather
// than forwarded (herdr-core backend spec, section 15, "internal/web").
const (
	// ActionShow focuses the execution's existing view in the native app.
	ActionShow = "show"
	// ActionSubmit is one person's typed submit, with its confirmation
	// (section 10, rule 2): the only way text reaches a core terminal.
	ActionSubmit = "submit"

	// The Herdr TUI's pane actions, as the web forwards them by pane.
	ActionPrompt = "prompt"
	ActionKeys   = "keys"
	ActionText   = "text"
	ActionInput  = "input"
	ActionScroll = "scroll"
	ActionFocus  = "focus"
	ActionClose  = "close"
)

var actions = map[string][]string{
	BackendCore: {ActionShow, ActionSubmit},
	BackendTUI:  {ActionPrompt, ActionKeys, ActionText, ActionInput, ActionScroll, ActionFocus, ActionClose},
}

// Actions is the explicit control list of a backend, in display order; nil for
// a name that is not a backend.
func Actions(backend string) []string {
	return slices.Clone(actions[backend])
}

// Allows reports whether a backend lists an action.
func Allows(backend, action string) bool {
	return slices.Contains(actions[backend], action)
}

// Types reports whether an action puts text or keys into a terminal. On
// herdr-core every such action becomes a person's typed submit or is refused.
func Types(action string) bool {
	switch action {
	case ActionPrompt, ActionKeys, ActionText, ActionInput, ActionSubmit:
		return true
	}
	return false
}
