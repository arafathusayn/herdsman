package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestIssuedBy: an approval record is the route's own issue only when
// the route's credentials are its parties with its options.
func TestIssuedBy(t *testing.T) {
	file := []ApprovedCredential{{Name: "adj-1", Option: "file"}, {Name: "orch-1", Option: "file"}}
	for _, c := range []struct {
		name  string
		creds map[string]credential
		rec   []ApprovedCredential
		want  bool
	}{
		{"no credentials", nil, file, false},
		{"same generation", map[string]credential{"adj-1": {Option: "file"}, "orch-1": {Option: "file"}}, file, true},
		{"another count", map[string]credential{"adj-1": {Option: "file"}}, file, false},
		{"another party", map[string]credential{"adj-1": {Option: "file"}, "orch-2": {Option: "file"}}, file, false},
		{"another option", map[string]credential{"adj-1": {Option: "identity"}, "orch-1": {Option: "file"}}, file, false},
	} {
		r := &Route{creds: c.creds}
		if got := r.issuedBy(approvalRecord{Credentials: c.rec}); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCurrentApprovalEvidenceGetsCredentials: the inbox's evidence of the
// approval of the current pair (file mode's state/approval.json, from a
// first pairing or an earlier adoption) is imported, and is never taken
// for the adoption's own credential issue: the adoption issues a fresh
// file credential for each party, and the evidence stays in the route
// database.
func TestCurrentApprovalEvidenceGetsCredentials(t *testing.T) {
	for _, interaction := range []string{"first-pairing", "adopt"} {
		t.Run(interaction, func(t *testing.T) {
			h := newHarness(t)
			dir, sum := fileInbox(t, h)
			rec := approvalRecord{SHA256: sum, Interaction: interaction,
				Credentials: []ApprovedCredential{{Name: "adj-1", Option: "file"}, {Name: "orch-1", Option: "file"}},
				Revoked:     []string{},
			}
			b, err := encode(rec)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "state", "approval.json"), b, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
			if err != nil {
				t.Fatalf("current-pair approval evidence prevented adoption: %v", err)
			}
			for _, name := range []string{"adj-1", "orch-1"} {
				if !exists(filepath.Join(dir, "state", "credentials", name)) {
					t.Errorf("adoption issued no fresh credential for %s", name)
				}
			}
			c := adoptDB(t, h, "old")
			if got := rowsOf(t, c, `SELECT interaction FROM approvals WHERE approved_by = 'import'`); len(got) != 1 || got[0] != interaction {
				t.Errorf("imported approval evidence: %v", got)
			}
			// A retry of the adoption's own approval is still answered by
			// its record and issues nothing new.
			rt, e := h.d.route("old")
			if e != nil {
				t.Fatal(e)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "state", "credentials", "adj-1"))
			res, e := rt.approve(ApproveRequest{SHA256: sum, Interaction: "adopt"})
			after, _ := os.ReadFile(filepath.Join(dir, "state", "credentials", "adj-1"))
			if e != nil || !res.Approved || string(before) != string(after) {
				t.Errorf("a retry of the adoption's approval: %+v %v, credential changed %v", res, e, string(before) != string(after))
			}
		})
	}
}
