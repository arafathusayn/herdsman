package daemon

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// The every-kind check (section 5, TestAdoptImportsEveryKind): after the
// import, every file of the inbox has its import_log row, from a target
// or from a listed reason; a file with neither refuses the import.

// censusRule is one listed reason for a file no target imports.
type censusRule struct {
	match       func(rel string) bool
	disposition string
}

func under(prefix string) func(string) bool {
	return func(rel string) bool { return strings.HasPrefix(rel, prefix) }
}

func stateFile(names ...string) func(string) bool {
	return func(rel string) bool {
		n, ok := strings.CutPrefix(rel, "state/")
		if !ok || strings.Contains(n, "/") {
			return false
		}
		for _, x := range names {
			if base, prefix := strings.CutSuffix(x, "*"); prefix && strings.HasPrefix(n, base) || n == x {
				return true
			}
		}
		return false
	}
}

// censusRules are the reasons of the section 5 table, in order.
var censusRules = []censusRule{
	{under("herdr-mutation.lock/"), "stays in place: the input lock"},
	{under("approvals/"), "stays in place: the gate's files"},
	{func(rel string) bool { return rel == "state/route.lock" || strings.HasPrefix(rel, "state/route.lock/") }, "stays in place: the route ownership lock"},
	{stateFile("boundary-tainted", "exposure-pending"), "stays in place: the boundary files (section 6)"},
	{under("state/legacy/"), "legacy evidence: the contract 2 legacy folder"},
	{func(rel string) bool {
		return rel == "setup" || rel == "setup.json" || strings.HasPrefix(rel, "setup/")
	}, "legacy evidence: the setup proof; HUMAN until setup writes version 3"},
	{stateFile("ring-*"), "not imported: courier ring marker, file channel service state"},
	{stateFile("automation-held", "card-visible-*"), "not imported: card watcher service state; the daemon's watcher decides afresh"},
	{stateFile("heartbeat", "watch-heartbeat", "diagnostic-*", "agent-list-failures", "allocations", "allocations-seeded",
		"blocked-*", "count-*", "wait-*", "timing.json"), "not imported: file channel service state"},
	{func(rel string) bool {
		top, _, _ := strings.Cut(rel, "/")
		return top == "channel.lock" || top == "courier.lock" || top == "watch.lock"
	}, "not imported: file channel lock"},
}

// census logs every file of the inbox that has no import_log row by its
// listed reason, and refuses a file with none.
func (im *importer) census() *APIError {
	return im.censusDir(im.rt.Dir)
}

func (im *importer) censusDir(dir string) *APIError {
	names, e := im.names(dir)
	if e != nil {
		return e
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		st, err := os.Lstat(p)
		if err != nil {
			return importErr("%s: %v", im.rel(p), err)
		}
		if st.IsDir() {
			if e := im.censusDir(p); e != nil {
				return e
			}
			continue
		}
		rel := im.rel(p)
		if im.logged[rel] {
			continue
		}
		disposition := im.classify(rel)
		if disposition == "" {
			return importErr("%s has no import target and no listed reason; move it out of the inbox or report it", rel)
		}
		if e := im.logAny(p, disposition); e != nil {
			return e
		}
	}
	return nil
}

// classify is the listed reason for a file no target imported, or "".
func (im *importer) classify(rel string) string {
	base := filepath.Base(rel)
	if strings.HasPrefix(base, ".") {
		return "partial-write"
	}
	for _, r := range censusRules {
		if r.match(rel) {
			return r.disposition
		}
	}
	parts := strings.Split(rel, "/")
	if parts[0] == "ids" && len(parts) == 3 {
		if id, ok := messageID(parts[1]); ok && im.imported(id) {
			return "" // every file of an imported message has its target
		}
		return "legacy evidence: an allocation record of a message not imported"
	}
	if len(parts) == 3 && im.isBox(parts[0]) {
		switch parts[1] {
		case "tmp", "recovery":
			return "partial-write"
		case "new":
			// The daemon serves the adopted route from its files, and its
			// intake delivers new/ at the route's first open (to ready/, or
			// to quarantine/ with a sender notice).
			return "not imported: queued in new/; the daemon's intake delivers it at the route's first open"
		case "ack":
			return "legacy evidence: an ack without its cur entry"
		}
	}
	return ""
}

// isBox reports a mailbox of the imported pair.
func (im *importer) isBox(name string) bool {
	n := int64(0)
	_ = im.tx.Query(`SELECT count(*) FROM parties WHERE name = ?`, []any{name}, func(r store.Row) error { n = r.Int64(0); return nil })
	return n == 1
}
