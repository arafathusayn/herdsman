package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/launch"
)

// Launch is one reserved launch (spec section 8, step 2): everything the
// later steps need, allocated before the first effect and journaled with the
// step that uses it. A retry of the same journaled step reads the same
// values; a new attempt reserves new ones.
type Launch struct {
	Intent    string `json:"intent"`
	Execution string `json:"execution"`
	// Owner is the installation id; RouteID and AgentID are opaque ids of
	// the route and the logical agent, stable across executions.
	Owner     string `json:"owner"`
	RouteID   string `json:"route_id"`
	AgentID   string `json:"agent_id"`
	Repo      string `json:"repo"`
	RouteName string `json:"route"`
	AgentName string `json:"agent"`
	Role      string `json:"role"`
	Kind      string `json:"kind"`
	// Placement is the route's space and this execution's own tab.
	Placement herdrapp.PlacementIDs `json:"placement"`
	// Name is the bounded core name, a hint only.
	Name string `json:"name"`
	// Wrapper is the argv core runs: the wrapper and the launch socket and
	// intent it names. Nothing secret.
	Wrapper    []string              `json:"wrapper"`
	Cwd        string                `json:"cwd"`
	Descriptor launch.CoreDescriptor `json:"descriptor"`
}

// Labels are the launch's placement and ownership labels.
func (l Launch) Labels() herdrapp.LaunchLabels {
	return herdrapp.LaunchLabels{Owner: l.Owner, Route: l.RouteID, Agent: l.AgentID, Execution: l.Execution, Role: l.Role, Placement: l.Placement}
}

// Validate refuses a reservation that cannot be spawned.
func (l Launch) Validate() error {
	if l.Intent == "" || l.Execution == "" || l.Name == "" || len(l.Wrapper) == 0 {
		return errors.New("core: the launch is not reserved")
	}
	if _, err := l.Labels().Map(); err != nil {
		return err
	}
	if l.Descriptor.Intent != l.Intent || l.Descriptor.Execution != l.Execution {
		return errors.New("core: the descriptor belongs to another launch")
	}
	return nil
}

// OpaqueID derives a stable opaque id from parts: the same parts give the
// same id, and nothing of them can be read back from it.
func OpaqueID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "hs-" + hex.EncodeToString(sum[:16])
}

// UUIDOf derives a stable UUID-shaped id from parts.
func UUIDOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var nameUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// maxName bounds a core name well inside core's naming limits.
const maxName = 48

// CoreName is a compact generated terminal name: the route, the role and the
// start of the execution id. A name is a diagnostic hint, never identity, and
// core may reuse it once a terminal finishes.
func CoreName(route, role, execution string) string {
	clip := func(s string, n int) string {
		s = strings.Trim(nameUnsafe.ReplaceAllString(strings.ToLower(s), "-"), "-")
		if len(s) > n {
			s = strings.TrimRight(s[:n], "-")
		}
		return s
	}
	id := clip(strings.ReplaceAll(execution, "-", ""), 8)
	name := "hs-" + clip(route, 20) + "-" + clip(role, 12) + "-" + id
	if len(name) > maxName {
		name = name[:maxName]
	}
	return name
}
