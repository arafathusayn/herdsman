package inbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// v2Inbox is a daemon-owned inbox with an approved pair and a version 2
// proof for the executable it returns: one rule, the binary, the full
// boundary of the pair, and watch targets. Its files sit under base.
type v2Inbox struct {
	base, dir, exe, rule string
}

func newV2Inbox(t *testing.T) v2Inbox {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	x := v2Inbox{base: base, dir: filepath.Join(base, "inbox"), exe: filepath.Join(base, "herdsman"), rule: filepath.Join(base, "ignore-rule")}
	if err := os.MkdirAll(filepath.Join(x.dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	x.write(t, x.exe, []byte("binary bytes\n"))
	x.write(t, x.rule, []byte("sandbox policy\n"))
	x.write(t, filepath.Join(x.dir, "state", "owner.json"), []byte(`{"owner":"daemon","epoch":"e1","contract":2}`+"\n"))
	id := func(pane, session string) map[string]any {
		return map[string]any{"pane": pane, "session": map[string]any{"agent": "claude", "kind": "id", "source": "hook", "value": session}}
	}
	pair := map[string]any{
		"route-adjutant":     map[string]any{"identity": id("p1", "s1"), "role": "adjutant"},
		"route-orchestrator": map[string]any{"identity": id("p2", "s2"), "role": "orchestrator"},
	}
	x.writeJSON(t, filepath.Join(x.dir, "peers"), pair)
	x.writeJSON(t, filepath.Join(x.dir, "peers.approved"), pair)
	x.writeJSON(t, filepath.Join(x.dir, "watch-targets.json"), []any{})
	x.writeJSON(t, filepath.Join(x.dir, "setup.json"), map[string]any{"version": 2, "inbox": x.dir, "policy_verified": true,
		"rules":         []any{map[string]any{"path": x.rule, "sha256": sum(t, x.rule)}},
		"binary":        map[string]any{"path": x.exe, "sha256": sum(t, x.exe)},
		"watch_targets": []any{},
		"boundary": map[string]any{"version": 1, "inventory_current": true, "private_home": true, "human_checked": true,
			"protected_domain": true, "launch_admission": true, "pair": pair, "courier_panes": []any{}, "lower_trust": []any{}},
	})
	return x
}

func (x v2Inbox) write(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (x v2Inbox) writeJSON(t *testing.T, p string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	x.write(t, p, append(b, '\n'))
}

func sum(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
