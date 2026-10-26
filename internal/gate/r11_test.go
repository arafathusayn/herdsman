package gate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Round 11 (review 10): the gate's own exposure writer. A large live
// observation is published as a bounded strict record with its omitted
// count, mode 0600, through the gate's stop-aware write: HUMAN, exit 3,
// nothing consumed, a retry stays held. A gate stopped during that
// observation writes nothing after the stop and consumes nothing.
func TestR11GateExposureBoundAndStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%v", stop), func(t *testing.T) {
			r := newRig(t)
			r.gojson("bound", 600, nil)
			lists := 0
			g := &Gate{}
			r.srv.Handle("agent.list", func(map[string]any) (any, string) {
				lists++
				if stop {
					g.Stop(143)
				}
				agents := []any{herdrtest.Agent("sender-pane", "sender-session"), herdrtest.Agent("receiver-pane", "receiver-session")}
				for i := range 700 {
					agents = append(agents, herdrtest.Agent(fmt.Sprintf("rogue-%d", i), fmt.Sprintf("session-%d-", i)+strings.Repeat("x", 300)))
				}
				return map[string]any{"type": "agent_list", "agents": agents}, ""
			})
			var out bytes.Buffer
			o := r.c.Options(false)
			o.Now = r.clock
			o.Sleep = func(time.Duration) {}
			g.Options, g.Client, g.Stdout, g.Stderr = o, r.hc, &out, &out
			target := filepath.Join(r.c.Dir, "state", "boundary-tainted")
			writes := 0
			g.beforeWrite = func(p string) {
				if p == target {
					writes++
				}
			}
			code := g.Run(context.Background(), []string{"consume", r.goFile("bound"), "post", "pr-comment", "head1", r.draft})
			if lists != 1 || writes != 1 || exists(r.consumed("bound")) {
				t.Fatalf("lists %d, writes through the gate %d, consumed %v", lists, writes, exists(r.consumed("bound")))
			}
			if stop {
				if code != 143 || exists(target) || out.Len() != 0 {
					t.Fatalf("stopped gate: exit %d, output %q, record written %v", code, out.String(), exists(target))
				}
				return
			}
			if code != 3 || !strings.Contains(out.String(), "HUMAN") {
				t.Fatalf("exit %d %q", code, out.String())
			}
			b, ok := inbox.ReadStrict(r.c.Dir, target)
			if !ok || len(b) > inbox.MaxSize || !bytes.Contains(b, []byte(`"omitted"`)) {
				t.Fatalf("not a bounded strict record with an omitted count (strict %v, %d bytes)", ok, len(b))
			}
			if st, err := os.Stat(target); err != nil || st.Mode().Perm() != 0o600 {
				t.Fatalf("record mode: %v %v", st, err)
			}
			match(t, "a retry stays held", `rc=3`, r.consume("bound"))
			if exists(r.consumed("bound")) {
				t.Fatal("the retry consumed")
			}
		})
	}
}
