package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func sum(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestGateAcceptsDaemonProof: on a contract 2 (daemon-owned) inbox the
// gate consumes under the version 2 proof and refuses the contract 1
// proof forms; a contract 1 inbox keeps its proof forms (4.6).
func TestGateAcceptsDaemonProof(t *testing.T) {
	r := newRig(t)
	v1, err := os.ReadFile(r.c.Proof)
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	_ = json.Unmarshal(v1, &old)
	r.c.Write(filepath.Join(r.c.Dir, "state", "owner.json"), `{"owner":"daemon","epoch":"e1","contract":2,"protocol":1,"adopted_from":null}`+"\n")

	// Contract 2 with the contract 1 proof: stale.
	r.gojson("d1", 600, nil)
	match(t, "a contract 1 proof on a contract 2 inbox", `proof stale.*rc=2`, r.consume("d1"))

	// Contract 2 with the version 2 proof: consumed.
	rule := filepath.Join(r.c.Base, "ignore-rule")
	bin := filepath.Join(r.c.Base, "herdsman-bin")
	r.c.Write(bin, "binary\n")
	v2 := map[string]any{"version": 2, "inbox": r.c.Dir, "policy_verified": true,
		"rules": []any{map[string]any{"path": rule, "sha256": sum(t, rule)}}, "binary": map[string]any{"path": bin, "sha256": sum(t, bin)},
		"boundary": old["boundary"], "probes": []any{}}
	r.c.WriteJSON(r.c.Proof, v2)
	match(t, "the version 2 proof on a contract 2 inbox", `^CONSUME d1.*rc=0`, r.consume("d1"))

	// The daemon's stale latch holds the proof stale although its files match.
	latch := filepath.Join(r.c.Dir, "state", "proof-stale")
	r.c.Write(latch, "a checksummed file changed\n")
	r.gojson("d3", 600, nil)
	match(t, "a latched version 2 proof", `proof stale.*rc=2`, r.consume("d3"))
	_ = os.Remove(latch)

	// A changed binary makes the version 2 proof stale.
	r.c.Write(bin, "other\n")
	r.gojson("d2", 600, nil)
	match(t, "a version 2 proof with a changed binary", `proof stale.*rc=2`, r.consume("d2"))

	// Contract 1 keeps its proof: the version 2 form is not its form.
	_ = os.Remove(filepath.Join(r.c.Dir, "state", "owner.json"))
	match(t, "a version 2 proof on a contract 1 inbox", `proof stale.*rc=2`, r.consume("d2"))
	if err := os.WriteFile(r.c.Proof, v1, 0o600); err != nil {
		t.Fatal(err)
	}
	match(t, "the contract 1 proof on a contract 1 inbox", `^CONSUME d2.*rc=0`, r.consume("d2"))
}
