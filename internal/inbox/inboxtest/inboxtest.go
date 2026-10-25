// Package inboxtest builds message-channel inboxes for tests, as the skill's
// message-tests.sh new_case does: a private inbox (canonical, mode 700) with
// a setup proof over an installed script folder, and on request an enrolled
// and approved pair and the protected boundary.
package inboxtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// The names of the enrolled pair, as in message-tests.sh.
const (
	Adjutant     = "route-adjutant"
	Orchestrator = "route-orchestrator"
)

// Case is one test inbox.
type Case struct {
	t *testing.T
	// Base holds the inbox, the installed scripts and the rule file.
	Base string
	// Dir is the inbox, HERDSMAN_INBOX.
	Dir string
	// Proof is the setup proof, HERDSMAN_PREFLIGHT.
	Proof string
	// Installed is the installed channel script folder; its parent holds the adapters.
	Installed string
}

// New builds an inbox with a valid setup proof and no registry.
func New(t *testing.T) *Case {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &Case{t: t, Base: base, Dir: filepath.Join(base, "inbox"), Installed: filepath.Join(base, "skill", "messages")}
	c.Proof = filepath.Join(c.Dir, "setup.json")
	if err := os.Mkdir(c.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Installed, 0o755); err != nil {
		t.Fatal(err)
	}
	c.Write(filepath.Join(base, "ignore-rule"), "sandbox policy\n")
	for _, s := range []string{"message-lib.sh", "message.sh", "message-send.sh"} {
		c.Write(filepath.Join(c.Installed, s), "#!/bin/bash\n# "+s+"\n")
	}
	for _, s := range []string{"approval-gate.sh", "transcript-audit.sh", "checkpointer-guard.sh", "checkpointer.sh"} {
		c.Write(filepath.Join(base, "skill", s), "#!/bin/bash\n# "+s+"\n")
	}
	c.Prove()
	return c
}

// Write writes a file, with its folders.
func (c *Case) Write(p, text string) {
	c.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		c.t.Fatal(err)
	}
}

// WriteJSON writes v as JSON.
func (c *Case) WriteJSON(p string, v any) {
	c.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	c.Write(p, string(b)+"\n")
}

// ReadJSON reads a JSON object.
func (c *Case) ReadJSON(p string) map[string]any {
	c.t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		c.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		c.t.Fatal(err)
	}
	return m
}

// Edit changes a JSON object file in place.
func (c *Case) Edit(p string, fn func(map[string]any)) {
	c.t.Helper()
	m := c.ReadJSON(p)
	fn(m)
	c.WriteJSON(p, m)
}

func (c *Case) entry(p string) map[string]any {
	c.t.Helper()
	sum, err := inbox.DigestFile(p)
	if err != nil {
		c.t.Fatal(err)
	}
	return map[string]any{"path": p, "checksum": sum}
}

// Prove writes the setup proof for the current installed files, keeping a
// boundary already recorded.
func (c *Case) Prove() {
	c.t.Helper()
	var scripts []any
	files, _ := filepath.Glob(filepath.Join(c.Installed, "*.sh"))
	adapters, _ := filepath.Glob(filepath.Join(filepath.Dir(c.Installed), "*.sh"))
	for _, f := range append(files, adapters...) {
		scripts = append(scripts, c.entry(f))
	}
	proof := map[string]any{"inbox": c.Dir, "policy_verified": true,
		"rules": []any{c.entry(filepath.Join(c.Base, "ignore-rule"))}, "scripts": scripts}
	if inbox.Exists(c.Proof) {
		if b := c.ReadJSON(c.Proof)["boundary"]; b != nil {
			proof["boundary"] = b
		}
	}
	c.WriteJSON(c.Proof, proof)
}

// Identity is an identity object as the registry records it.
func Identity(pane, session string) map[string]any {
	return map[string]any{"pane": pane, "session": map[string]any{"agent": "claude", "kind": "id", "source": "hook", "value": session}}
}

// Enroll writes an approved registry of the adjutant in adjPane and the
// orchestrator in orchPane (sessions adjSession, orchSession), the state
// folder and a fresh courier heartbeat.
func (c *Case) Enroll(adjPane, adjSession, orchPane, orchSession string) {
	c.t.Helper()
	reg := map[string]any{
		Adjutant:     map[string]any{"identity": Identity(adjPane, adjSession), "role": "adjutant"},
		Orchestrator: map[string]any{"identity": Identity(orchPane, orchSession), "role": "orchestrator"},
	}
	c.WriteJSON(filepath.Join(c.Dir, "peers"), reg)
	c.WriteJSON(filepath.Join(c.Dir, "peers.approved"), reg)
	c.Write(filepath.Join(c.Dir, "state", "heartbeat"), "")
}

// RelayBoundary records the protected human setup for the agents (herdr
// agent records): the approved pair pinned and every other agent a
// sandboxed lower-trust launch, as message-tests.sh relay_boundary does.
func (c *Case) RelayBoundary(agents ...map[string]any) {
	c.t.Helper()
	pair := c.ReadJSON(filepath.Join(c.Dir, "peers.approved"))
	var ids []any
	for _, p := range pair {
		ids = append(ids, p.(map[string]any)["identity"])
	}
	lower := []any{}
	for _, a := range agents {
		id := map[string]any{"pane": a["pane_id"], "session": a["agent_session"]}
		known := false
		for _, x := range ids {
			if inbox.Equal(roundTrip(c.t, x), roundTrip(c.t, id)) {
				known = true
			}
		}
		if !known {
			lower = append(lower, map[string]any{"identity": id, "harness": a["agent"], "launch_hash": "fixture-check",
				"sandbox": true, "inbox_denied": true, "socket_denied": true, "code_denied": true, "proof_denied": true})
		}
	}
	c.Edit(c.Proof, func(m map[string]any) {
		m["boundary"] = map[string]any{"version": 1, "inventory_current": true, "private_home": true, "human_checked": true,
			"protected_domain": true, "launch_admission": true, "pair": pair, "courier_panes": []any{}, "lower_trust": lower}
	})
}

func roundTrip(t *testing.T, v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := inbox.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Options are the inbox options of this case.
func (c *Case) Options(readOnly bool) inbox.Options {
	return inbox.Options{Dir: c.Dir, Preflight: c.Proof, Installed: c.Installed, ReadOnly: readOnly}
}

// Env is the case's environment for a command: HERDSMAN_INBOX,
// HERDSMAN_PREFLIGHT and HERDSMAN_MESSAGES_DIR.
func (c *Case) Env() []string {
	return []string{"HERDSMAN_INBOX=" + c.Dir, "HERDSMAN_PREFLIGHT=" + c.Proof, "HERDSMAN_MESSAGES_DIR=" + c.Installed}
}
