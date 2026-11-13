package herdr

import (
	"path/filepath"
	"slices"
	"strings"
)

// CoreNames lists the names herdr-core's program runs under. Its server and
// the holder of each terminal show one of them in the process table: the name
// they were started with.
var CoreNames = []string{"hc", "herdr-core"}

// IsCoreProcess reports whether a process-table command line belongs to
// herdr-core: its server, or a holder, the process that owns a terminal and is
// the parent of the terminal's program.
//
// Only the program's name is read. The name is what herdr-core shows today,
// not a promise it makes, so `herdsman doctor` checks it against the holders
// a running core reports.
func IsCoreProcess(command string) bool {
	words := strings.Fields(command)
	return len(words) > 0 && slices.Contains(CoreNames, filepath.Base(words[0]))
}
