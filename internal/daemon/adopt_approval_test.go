package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCurrentApprovalEvidenceGetsCredentials: the inbox's evidence of the
// approval of the current pair (file mode's state/approval.json, from a
// first pairing or an earlier adoption) is imported and then moved to
// state/legacy, so it is never taken for the adoption's own credential
// issue: the adoption issues a fresh file credential for each party, and
// the evidence stays in the route database.
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
			if kept, err := os.ReadFile(filepath.Join(dir, "state", "legacy", "approval-v1.json")); err != nil || string(kept) != string(b) {
				t.Errorf("file mode's approval.json was not moved aside: %v", err)
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

// TestRevokeThenRetryIssuesNothing: after a revoke, a retry of the
// adoption's consumed approval is answered by its record (7.5): it
// re-issues no revoked credential and replaces no other.
func TestRevokeThenRetryIssuesNothing(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	h.do("POST", "/v1/routes/old/credentials/adj-1/revoke", nil, nil)
	if exists(filepath.Join(dir, "state", "credentials", "adj-1")) {
		t.Fatal("revoke left the credential")
	}
	orch, err := os.ReadFile(filepath.Join(dir, "state", "credentials", "orch-1"))
	if err != nil {
		t.Fatal(err)
	}
	rt, e := h.d.route("old")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := rt.approve(ApproveRequest{SHA256: sum, Interaction: "adopt"}); e != nil {
		t.Fatalf("retry of the consumed approval: %v", e)
	}
	if exists(filepath.Join(dir, "state", "credentials", "adj-1")) {
		t.Error("the retry re-issued the revoked party adj-1's credential")
	}
	if now, _ := os.ReadFile(filepath.Join(dir, "state", "credentials", "orch-1")); string(now) != string(orch) {
		t.Error("the retry replaced orch-1's credential")
	}
}
