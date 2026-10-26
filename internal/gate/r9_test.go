package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

const orchestratorAskLine = "the orchestrator's own ask is no go in a route with an adjutant; ask again through the adjutant"

// AG15 (PR 28): with an adjutant every question goes through it, so only
// the approved adjutant's ask-tool answer is the go.
func TestR9AskedByTheApprovedAdjutant(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		name, req, want string
		edit            func(m map[string]any)
	}{
		{"AG15 an answer to the orchestrator's own ask is no go", "r15", "^REFUSED " + orchestratorAskLine + " rc=1$",
			func(m map[string]any) { m["asked_by"] = inboxtest.Orchestrator }},
		{"AG15c an answer that names no asker is no go", "r16", "^REFUSED trusted interaction was not asked by the approved adjutant rc=1$",
			func(m map[string]any) { delete(m, "asked_by") }},
		{"AG15d an answer asked by an unknown name is no go", "r17", "^REFUSED trusted interaction was not asked by the approved adjutant rc=1$",
			func(m map[string]any) { m["asked_by"] = "route-nobody" }},
		{"an asker that is not a string is no go", "r18", "^REFUSED trusted interaction was not asked by the approved adjutant rc=1$",
			func(m map[string]any) { m["asked_by"] = []any{inboxtest.Adjutant} }},
	} {
		r.gojson(tc.req, 600, nil)
		r.c.Edit(r.interaction(tc.req), tc.edit)
		match(t, tc.name, tc.want, r.consume(tc.req))
		if exists(r.consumed(tc.req)) {
			t.Errorf("%s: and records nothing", tc.name)
		}
	}
	r.gojson("r19", 600, nil)
	match(t, "the approved adjutant's answer is the go", `^CONSUME r19.*rc=0`, r.consume("r19"))
}

// The approved pair names the adjutant: a missing or invalid peers.approved
// and a pair without an adjutant are refused.
func TestR9ApprovedPairNamesTheAdjutant(t *testing.T) {
	approved := func(r *rig) string { return filepath.Join(r.c.Dir, "peers.approved") }
	for _, tc := range []struct {
		name, want string
		setup      func(r *rig)
	}{
		{"missing", "no approved pair to name the adjutant", func(r *rig) { os.Remove(approved(r)) }},
		{"invalid", "no approved pair to name the adjutant", func(r *rig) { r.c.Write(approved(r), `{"x":1}`+"\n") }},
		{"duplicate key", "no approved pair to name the adjutant", func(r *rig) { r.c.Write(approved(r), `{"a":1,"a":2}`+"\n") }},
		{"empty pair", "no approved adjutant", func(r *rig) { r.c.Write(approved(r), "{}\n") }},
		{"orchestrator only", "no approved adjutant", func(r *rig) {
			r.c.Edit(approved(r), func(m map[string]any) { delete(m, inboxtest.Adjutant) })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.gojson("p1", 600, nil)
			tc.setup(r)
			match(t, tc.name, "^REFUSED "+tc.want+" rc=1$", r.consume("p1"))
			if exists(r.consumed("p1")) {
				t.Error("and records nothing")
			}
		})
	}
}

// The asker check keeps its place: after the action, target and draft
// match, before the gate lock and the boundary.
func TestR9AskedByCheckOrder(t *testing.T) {
	r := newRig(t)
	orch := func(m map[string]any) { m["asked_by"] = inboxtest.Orchestrator }
	r.gojson("o1", 600, nil)
	r.c.Edit(r.interaction("o1"), func(m map[string]any) { orch(m); m["action"] = "merge" })
	match(t, "a mismatched approval is refused first", `approves another action.*rc=1`, r.consume("o1"))
	r.gojson("o2", 600, nil)
	r.c.Edit(r.interaction("o2"), orch)
	match(t, "a changed target is refused first", `target differs.*rc=1`, r.consume("o2", "other-pr"))
	r.gojson("o3", 600, nil)
	r.c.Edit(r.interaction("o3"), orch)
	approved := filepath.Join(r.c.Dir, "peers.approved")
	saved, _ := os.ReadFile(approved)
	os.Remove(approved)
	match(t, "a changed draft is refused before the pair is read", `draft differs.*rc=1`, r.consume("o3", "pr-comment", "head1", "-"))
	os.WriteFile(approved, saved, 0o600)
	os.Mkdir(filepath.Join(r.apr, inbox.ActionLock), 0o700)
	match(t, "a busy gate lock comes after the asker check", "^REFUSED "+orchestratorAskLine+" rc=1$", r.consume("o3"))
	os.Remove(filepath.Join(r.apr, inbox.ActionLock))
	r.c.Write(filepath.Join(r.c.Dir, "state", "boundary-tainted"), `{"agents":[],"time":1}`+"\n")
	match(t, "the HUMAN boundary comes after the asker check", "^REFUSED "+orchestratorAskLine+" rc=1$", r.consume("o3"))
}

// XB7, XB10, XB11 (PR 24, a38ff72): the gate publishes a newline-named
// entry, a strict entry with unusable agents and dot-named entries, and
// refuses; a pending folder it can read but not write is published and
// kept, and the gate refuses.
func TestR9GatePendingKinds(t *testing.T) {
	type rec = map[string]any
	for _, tc := range []struct {
		name    string
		entries map[string]string
		mode    os.FileMode
		panes   string
		kept    bool
	}{
		{"XB7 newline", map[string]string{"\n": `{"agents":[{"pane":"rogue-pane"}],"time":1}`}, 0o700, "rogue-pane", false},
		{"XB7 badmeta", map[string]string{"1-1-1.json": `{"agents":"bad","time":1}`}, 0o700, "", false},
		{"XB10 dotnames", map[string]string{".record.json": `{"agents":[{"pane":"dot-pane"}],"time":1}`,
			"..record.json": `{"agents":[{"pane":"dotdot-pane"}],"time":1}`}, 0o700, "dot-pane dotdot-pane", false},
		{"XB11 readonly", map[string]string{"1-1-1.json": `{"agents":[{"pane":"rogue-pane"}],"time":1}`}, 0o500, "rogue-pane", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			p := filepath.Join(r.c.Dir, "state", "exposure-pending")
			os.MkdirAll(p, 0o700)
			for n, body := range tc.entries {
				os.WriteFile(filepath.Join(p, n), []byte(body+"\n"), 0o600)
			}
			os.Chmod(p, tc.mode)
			defer os.Chmod(p, 0o700)
			r.gojson("xb", 600, nil)
			out, code := r.gate("consume", r.goFile("xb"), "post", "pr-comment", "head1", r.draft)
			if code != 3 || exists(r.consumed("xb")) {
				t.Fatalf("the gate did not refuse: exit %d %q", code, out)
			}
			b, _ := os.ReadFile(filepath.Join(r.c.Dir, "state", "boundary-tainted"))
			var tr struct {
				Agents []rec `json:"agents"`
			}
			if err := json.Unmarshal(b, &tr); err != nil || tr.Agents == nil {
				t.Fatalf("no published record with agents: %q", b)
			}
			var panes []string
			for _, a := range tr.Agents {
				s, _ := a["pane"].(string)
				panes = append(panes, s)
			}
			sort.Strings(panes)
			if strings.Join(panes, " ") != tc.panes {
				t.Errorf("published panes %q, want %q", panes, tc.panes)
			}
			os.Chmod(p, 0o700)
			for n := range tc.entries {
				if _, err := os.Lstat(filepath.Join(p, n)); (err == nil) != tc.kept {
					t.Errorf("entry %q kept %v, want %v", n, err == nil, tc.kept)
				}
			}
			if tc.kept && !logged(r, "could not be removed") {
				t.Error("the kept entry was not logged")
			}
		})
	}
}

func logged(r *rig, s string) bool {
	logs, _ := filepath.Glob(filepath.Join(r.c.Dir, "state", "diagnostic-*.log"))
	for _, l := range logs {
		if b, _ := os.ReadFile(l); strings.Contains(string(b), s) {
			return true
		}
	}
	return false
}

// AG15 tostring (PR 28): the orchestrator lookup keys a non-string asker by
// jq's tostring, so a missing, null, true or false asker meets an approved
// orchestrator named null, true or false and gets the orchestrator line.
func TestR9AskerKeyTostring(t *testing.T) {
	for _, tc := range []struct {
		name, orchestrator string
		edit               func(m map[string]any)
	}{
		{"missing", "null", func(m map[string]any) { delete(m, "asked_by") }},
		{"null", "null", func(m map[string]any) { m["asked_by"] = nil }},
		{"true", "true", func(m map[string]any) { m["asked_by"] = true }},
		{"false", "false", func(m map[string]any) { m["asked_by"] = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.c.Edit(filepath.Join(r.c.Dir, "peers.approved"), func(m map[string]any) {
				m[tc.orchestrator] = m[inboxtest.Orchestrator]
				delete(m, inboxtest.Orchestrator)
			})
			r.gojson("k1", 600, nil)
			r.c.Edit(r.interaction("k1"), tc.edit)
			match(t, tc.name, "^REFUSED "+orchestratorAskLine+" rc=1$", r.consume("k1"))
			if exists(r.consumed("k1")) {
				t.Error("and records nothing")
			}
		})
	}
}

// XB8c/d, XB9c/d (PR 24): the executor gate on the real truncated (first
// twenty) and minimal waiting records that a busy writer keeps: HUMAN,
// exit 3, nothing consumed, and a retry stays held.
func TestR9GateOnTruncatedPendingRecords(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n, size int
		agents  int
	}{
		{"XB8 first twenty", 700, 300, 20},
		{"XB9 minimal", 21, 7000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			ch, err := inbox.OpenChannel(r.c.Options(false), "")
			if err != nil {
				t.Fatal(err)
			}
			var unknown []inbox.Unknown
			for i := range tc.n {
				s, _ := json.Marshal(map[string]string{"agent": "claude", "kind": "id", "source": "hook", "value": fmt.Sprintf("s%d-%s", i, strings.Repeat("x", tc.size))})
				unknown = append(unknown, inbox.Unknown{Pane: fmt.Sprintf("rogue-%d", i), Agent: "claude", Session: s})
			}
			if err := ch.KeepPending(unknown); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(r.c.Dir, "state", "exposure-pending")
			ents, _ := os.ReadDir(p)
			if len(ents) != 1 {
				t.Fatalf("pending records %d", len(ents))
			}
			b, _ := os.ReadFile(filepath.Join(p, ents[0].Name()))
			var rec struct {
				Agents    []any `json:"agents"`
				Truncated bool  `json:"truncated"`
			}
			if json.Unmarshal(b, &rec) != nil || !rec.Truncated || len(rec.Agents) != tc.agents {
				t.Fatalf("not the %s record: %d agents, truncated %v", tc.name, len(rec.Agents), rec.Truncated)
			}
			r.gojson("xt", 600, nil)
			for _, try := range []string{"first", "retry"} {
				out, code := r.gate("consume", r.goFile("xt"), "post", "pr-comment", "head1", r.draft)
				if code != 3 || !strings.Contains(out, "HUMAN") || exists(r.consumed("xt")) {
					t.Errorf("%s go: exit %d %q", try, code, out)
				}
			}
		})
	}
}
