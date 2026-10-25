package main

import (
	"fmt"
	"io"
	"os"

	"github.com/arafathusayn/herdsman/internal/audit"
)

func init() {
	register(command{name: "transcript-audit", summary: "Audit a Claude or Codex transcript for writes: transcript-audit claude|codex <transcript> <session-id> [root/ ...].", run: runAudit})
}

// runAudit is transcript-audit.sh: GAP, FLAG and AUDIT lines on stdout, exit
// 0 clean, 1 flagged, 2 a gap.
func runAudit(args []string, _ io.Reader, stdout, _ io.Writer) int {
	if len(args) < 3 {
		_, _ = fmt.Fprintln(stdout, "GAP "+audit.Usage)
		return 2
	}
	return audit.Run(audit.Options{Harness: args[0], Transcript: args[1], Session: args[2], Roots: args[3:],
		GrantFile: os.Getenv("AUDIT_GRANT_FILE"), ChannelDir: os.Getenv("AUDIT_CHANNEL_DIR")}, stdout)
}
