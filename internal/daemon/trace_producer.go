package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/trace"
)

// routeFolderEnv names the route folder a process works for. Route starts set
// it for the agents they launch.
const routeFolderEnv = "HERDSMAN_ROUTE_FOLDER"

// turnClock remembers when each open claim began. A daemon restart forgets
// them, and a turn then starts at its acknowledgement with no duration.
type turnClock struct {
	mu      sync.Mutex
	started map[string]time.Time
}

func turnKey(routeName, token string) string { return routeName + "\x00" + token }

// begin keeps the earliest time: a repeated read of the same claim is not a
// new turn.
func (c *turnClock) begin(routeName, token string) {
	if token == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started == nil {
		c.started = map[string]time.Time{}
	}
	if _, ok := c.started[turnKey(routeName, token)]; !ok {
		c.started[turnKey(routeName, token)] = time.Now().UTC()
	}
}

func (c *turnClock) since(routeName, token string, now time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if at, ok := c.started[turnKey(routeName, token)]; ok {
		return at
	}
	return now
}

func (c *turnClock) end(routeName, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.started, turnKey(routeName, token))
}

// turnKind tells a review from other work by the party's role name.
func turnKind(party string) string {
	if strings.Contains(strings.ToLower(party), "reviewer") {
		return trace.KindReview
	}
	return trace.KindTurn
}

var routeDateSuffix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// routeFolderIn finds a route's folder under a repository: named for the route
// alone or for the route and its start date, the newest when there are several.
func routeFolderIn(repo, name string) string {
	for _, folder := range route.Find([]string{repo}) {
		base := filepath.Base(folder)
		if base == name {
			return folder
		}
		if date, ok := strings.CutPrefix(base, name+"-"); ok && routeDateSuffix.MatchString(date) {
			return folder
		}
	}
	return ""
}

// recordTurn adds one finished piece of agent work to the route's trace. The
// message has already been acknowledged, so a failure here is only logged.
func (d *Daemon) recordTurn(routeName, party, token string) {
	entry, ok := d.routeEntry(routeName)
	if !ok || entry.Repo == "" {
		return
	}
	folder := routeFolderIn(entry.Repo, routeName)
	if folder == "" {
		return
	}
	now := time.Now().UTC()
	start := d.turns.since(routeName, token, now)
	span := trace.Span{Kind: turnKind(party), Name: party, Start: start, DurationMS: float64(now.Sub(start)) / float64(time.Millisecond)}
	if err := trace.Append(folder, span); err != nil && !os.IsNotExist(err) {
		d.logf("route %s: the trace has no record of this turn: %v", routeName, err)
	}
}
