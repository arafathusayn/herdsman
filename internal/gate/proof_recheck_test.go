package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// TestGateRechecksProofUnderLock: on a daemon-owned inbox the gate checks
// the version 2 proof again once it holds the input lock. A policy change
// or a new state/proof-stale while it waits for the lock refuses the go,
// though the proof was valid when the gate opened the inbox.
func TestGateRechecksProofUnderLock(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(r *rig, rule string)
	}{
		{"proof-stale latch", func(r *rig, _ string) {
			r.c.Write(filepath.Join(r.c.Dir, "state", "proof-stale"), "a checksummed file changed\n")
		}},
		{"policy rewritten", func(r *rig, rule string) {
			r.c.Write(rule, "changed while the gate waited\n")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			v1, err := os.ReadFile(r.c.Proof)
			if err != nil {
				t.Fatal(err)
			}
			var old map[string]any
			_ = json.Unmarshal(v1, &old)
			r.c.Write(filepath.Join(r.c.Dir, "state", "owner.json"), `{"owner":"daemon","epoch":"e1","contract":2,"protocol":1,"adopted_from":null}`+"\n")
			rule := filepath.Join(r.c.Base, "ignore-rule")
			exe := inbox.RunningExecutable()
			r.c.WriteJSON(r.c.Proof, map[string]any{"version": 2, "inbox": r.c.Dir, "policy_verified": true,
				"rules": []any{map[string]any{"path": rule, "sha256": sum(t, rule)}}, "binary": map[string]any{"path": exe, "sha256": sum(t, exe)},
				"boundary": old["boundary"], "probes": []any{}})
			r.gojson("d1", 600, nil)

			// Another process holds the input lock; the change lands while
			// the gate waits for it, then the lock is freed.
			lock := filepath.Join(r.c.Dir, inbox.MutationLock)
			if err := os.Mkdir(lock, 0o700); err != nil {
				t.Fatal(err)
			}
			waited := false
			var out bytes.Buffer
			o := r.c.Options(false)
			o.Now = r.clock
			o.Sleep = func(time.Duration) {
				if !waited {
					waited = true
					c.change(r, rule)
					_ = os.Remove(lock)
				}
			}
			g := &Gate{Options: o, Client: r.hc, Stdout: &out, Stderr: &out}
			code := g.Run(context.Background(), []string{"consume", r.goFile("d1"), "post", "pr-comment", "head1", r.draft})
			got := strings.ReplaceAll(out.String(), "\n", " ") + fmt.Sprintf("rc=%d", code)
			if !waited {
				t.Fatalf("the gate did not wait for the lock: %s", got)
			}
			if code == 0 || strings.HasPrefix(got, "CONSUME") || exists(r.consumed("d1")) {
				t.Fatalf("a go consumed under a proof that went stale during the lock wait: %s", got)
			}
		})
	}
}
