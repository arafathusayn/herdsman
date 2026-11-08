// Package ledger decides from evidence what a teardown may act on, and carries
// it out. It reads the output of lsof, ps and docker (or podman) into a
// Snapshot and builds a plan document from it, and everything else in the
// package is pure over a Snapshot. Only signal.go signals a process and only
// the live_ files run a command.
package ledger

import (
	"strconv"
	"time"
)

// The resource kinds a plan line can have. A hook may register other kinds
// through a Record; those pass through unchanged.
const (
	KindProcess   = "process"
	KindPort      = "port"
	KindContainer = "container"
	KindCompose   = "compose"
	KindVolume    = "volume"
	KindNetwork   = "network"
	KindScratch   = "scratch"
	KindDatabase  = "database"
	// KindRecord is a line for a record that matched nothing live.
	KindRecord = "record"
)

// Identity names one resource for its whole life: (pid, start time) for a
// process, the id for a container, the name plus creation time for a volume or
// network. A slug or a bare pid is never an identity, since pids are reused.
type Identity struct {
	Kind  string
	ID    string
	Start time.Time
}

// ProcessIdentity is the (pid, start time) pair of a process.
func ProcessIdentity(pid int, start time.Time) Identity {
	return Identity{Kind: KindProcess, ID: strconv.Itoa(pid), Start: start}
}

// String is the form the plan document carries.
func (i Identity) String() string {
	base := i.Kind + ":" + i.ID
	if i.Start.IsZero() {
		return base
	}
	return base + "@" + i.Start.UTC().Format(time.RFC3339)
}
