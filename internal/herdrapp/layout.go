package herdrapp

import "strings"

// Layout is `herdr-app ls`: the open window's spaces, their tabs and the
// terminals in them. Unknown fields are ignored, so a newer app that adds one
// still parses. It carries no core identity (app 0.1.14 and 0.1.15).
type Layout struct {
	Spaces []Space  `json:"spaces"`
	Recent []Recent `json:"recent"`
}

// Space is one space. Machine is null for this computer; a remote space's
// ids carry a "<machine>/" prefix.
type Space struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Folder  string  `json:"folder"`
	Machine *string `json:"machine"`
	Active  bool    `json:"active"`
	Tabs    []Tab   `json:"tabs"`
}

// Local reports whether the space lives on this computer's core.
func (s Space) Local() bool { return s.Machine == nil && !strings.Contains(s.ID, "/") }

// Tab is one tab of a space.
type Tab struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Active    bool       `json:"active"`
	Settled   bool       `json:"settled"`
	Terminals []Terminal `json:"terminals"`
}

// Terminal is one terminal pane of a tab. Status is set only for a program
// that reports one (an agent).
type Terminal struct {
	Terminal  string     `json:"terminal"`
	Pane      string     `json:"pane"`
	Status    *string    `json:"status"`
	App       *string    `json:"app"`
	Name      string     `json:"name"`
	SubAgents []Terminal `json:"sub_agents"`
}

// Recent is a closed space the window remembers.
type Recent struct {
	Name   string `json:"name"`
	Folder string `json:"folder"`
}

// Location is where a terminal is shown.
type Location struct {
	Space    string
	Tab      string
	Pane     string
	Terminal string
	Local    bool
	Status   string
}

// Locate returns every place the layout shows the terminal address,
// including sub-agent entries, in layout order.
func (l Layout) Locate(address string) []Location {
	var found []Location
	var walk func(space Space, tab Tab, terms []Terminal)
	walk = func(space Space, tab Tab, terms []Terminal) {
		for _, t := range terms {
			if t.Terminal == address {
				loc := Location{Space: space.ID, Tab: tab.ID, Pane: t.Pane, Terminal: t.Terminal, Local: space.Local()}
				if t.Status != nil {
					loc.Status = *t.Status
				}
				found = append(found, loc)
			}
			walk(space, tab, t.SubAgents)
		}
	}
	for _, space := range l.Spaces {
		for _, tab := range space.Tabs {
			walk(space, tab, tab.Terminals)
		}
	}
	return found
}

// space returns the space with this id.
func (l Layout) space(id string) (Space, bool) {
	for _, s := range l.Spaces {
		if s.ID == id {
			return s, true
		}
	}
	return Space{}, false
}

// tab returns the tab with this id and its space.
func (l Layout) tab(id string) (Space, Tab, bool) {
	for _, s := range l.Spaces {
		for _, t := range s.Tabs {
			if t.ID == id {
				return s, t, true
			}
		}
	}
	return Space{}, Tab{}, false
}
