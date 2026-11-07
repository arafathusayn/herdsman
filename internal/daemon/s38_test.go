package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// S38: the account lock and the route lock owner records are strict JSON.
// An unknown field, data after the object or a duplicate key is refused;
// the record as the daemon writes it is accepted.

func TestReadAccountLockIsStrict(t *testing.T) {
	cases := []struct {
		name, body string
		ok         bool
	}{
		{"valid", `{"pid":42,"version":"1.0.0","start":1,"private_home":"/p","socket":"/p/s"}` + "\n", true},
		{"unknown field", `{"pid":42,"version":"1.0.0","start":1,"extra":true}`, false},
		{"trailing data", `{"pid":42,"version":"1.0.0","start":1}{"pid":7}`, false},
		{"duplicate key", `{"pid":42,"version":"1.0.0","start":1,"pid":7}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(AccountLockPath(home), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			l, ok := ReadAccountLock(home)
			if ok != tc.ok {
				t.Fatalf("ReadAccountLock(%s) = %+v, %v; want ok %v", tc.body, l, ok, tc.ok)
			}
			if ok && l.PID != 42 {
				t.Fatalf("pid %d, want 42", l.PID)
			}
		})
	}
}

func TestDaemonLockOwnerIsStrict(t *testing.T) {
	epoch := "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name, body string
		ok         bool
	}{
		{"valid", `{"owner":"daemon","epoch":"` + epoch + `","pid":42,"start":1}` + "\n", true},
		{"unknown field", `{"owner":"daemon","epoch":"` + epoch + `","extra":true}`, false},
		{"trailing data", `{"owner":"daemon","epoch":"` + epoch + `"} {"owner":"daemon"}`, false},
		{"duplicate key", `{"owner":"daemon","epoch":"` + epoch + `","owner":"daemon"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			o, ok := daemonLockOwner(dir)
			if ok != tc.ok {
				t.Fatalf("daemonLockOwner(%s) = %+v, %v; want ok %v", tc.body, o, ok, tc.ok)
			}
			if ok && o.Epoch != epoch {
				t.Fatalf("epoch %q, want %q", o.Epoch, epoch)
			}
		})
	}
}
