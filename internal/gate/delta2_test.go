package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewDeltaGatePendingUnreadable(t *testing.T) {
	for _, mode := range []string{"file", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t)
			r.gojson("pending", 600, nil)
			p := filepath.Join(r.c.Dir, "state", "exposure-pending")
			if mode == "file" {
				r.c.Write(p, "exposure")
			} else {
				r.c.Write(filepath.Join(p, "x.json"), `{"agents":[]}`)
				if err := os.Chmod(p, 0000); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(p, 0700)
			}
			out := r.consume("pending")
			if strings.Contains(out, "CONSUME") || exists(r.consumed("pending")) {
				t.Errorf("%s pending exposure allowed an approval consumption: %q", mode, out)
			}
		})
	}
}
