package daemon

import (
	"strings"
	"testing"
)

// approvalJSON is a contract 2 state/approval.json with the given
// credentials and revoked lists.
func approvalJSON(creds, revoked any) []byte {
	return prettyJSON(obj("sha256", strings.Repeat("a", 64), "interaction", "i", "credentials", creds, "revoked", revoked))
}

// TestImportApprovalNoCredentialValues: approval evidence is only the
// typed metadata (a credential's name and option, the revoked names); a
// credential entry with any other field, a secret value among them, or
// a malformed entry refuses the import, and no value enters the database.
func TestImportApprovalNoCredentialValues(t *testing.T) {
	const secret = "secret-value-must-never-enter-the-db"
	for name, b := range map[string][]byte{
		"credential secret field": approvalJSON([]any{obj("name", "adj-1", "option", "file", "secret", secret)}, []any{}),
		"credential is a string":  approvalJSON([]any{secret}, []any{}),
		"credential no option":    approvalJSON([]any{obj("name", "adj-1")}, []any{}),
		"credential bad option":   approvalJSON([]any{obj("name", "adj-1", "option", secret)}, []any{}),
		"credential empty name":   approvalJSON([]any{obj("name", "", "option", "file")}, []any{}),
		"credential bad name":     approvalJSON([]any{obj("name", "Adj 1", "option", "file")}, []any{}),
		"revoked is an object":    approvalJSON([]any{}, []any{obj("name", "adj-1", "secret", secret)}),
		"revoked bad name":        approvalJSON([]any{}, []any{secret + " x"}),
		"credentials not a list":  approvalJSON(obj("adj-1", secret), []any{}),
		"interaction not a text":  prettyJSON(obj("sha256", strings.Repeat("a", 64), "interaction", 1, "credentials", []any{}, "revoked", []any{})),
	} {
		t.Run(name, func(t *testing.T) {
			x := newImportInbox(t)
			x.write(t, "state/approval.json", b)
			c, e := x.run(t)
			if e == nil {
				t.Fatal("imported")
			}
			for _, r := range rowsOf(t, c, `SELECT result FROM approvals`) {
				if strings.Contains(r, secret) {
					t.Fatalf("approvals.result holds a credential value: %s", r)
				}
			}
		})
	}
}

// TestImportApprovalEvidence: a well-formed record becomes the evidence
// row built from its typed fields only.
func TestImportApprovalEvidence(t *testing.T) {
	x := newImportInbox(t)
	x.write(t, "state/approval.json", approvalJSON([]any{obj("name", "adj-1", "option", "file"), obj("name", "orch-1", "option", "identity")}, []any{"adj-0"}))
	c, e := x.run(t)
	if e != nil {
		t.Fatal(e)
	}
	got := rowsOf(t, c, `SELECT result FROM approvals`)
	want := `{"approved":true,"credentials":[{"name":"adj-1","option":"file"},{"name":"orch-1","option":"identity"}],"revoked":["adj-0"]}`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("approvals.result %q, want %q", got, want)
	}
}
