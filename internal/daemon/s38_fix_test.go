package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// S38 F2: encoding/json matches struct fields case-insensitively, so a
// case variant of a field name must be refused, not read as the field.
func TestLockRecordsRefuseCaseVariantKeys(t *testing.T) {
	epoch := "0123456789abcdef0123456789abcdef"
	for _, body := range []string{
		`{"pid":0,"PID":42,"version":"1.0.0","start":1}`,
		`{"PID":42,"version":"1.0.0","start":1}`,
		`{"pid":42,"Version":"1.0.0","start":1}`,
	} {
		home := t.TempDir()
		if err := os.WriteFile(AccountLockPath(home), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if l, ok := ReadAccountLock(home); ok {
			t.Errorf("ReadAccountLock(%s) = %+v, true; want refused", body, l)
		}
	}
	for _, body := range []string{
		`{"owner":"foreign","Owner":"daemon","epoch":"` + epoch + `"}`,
		`{"Owner":"daemon","epoch":"` + epoch + `"}`,
		`{"owner":"daemon","EPOCH":"` + epoch + `"}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if o, ok := daemonLockOwner(dir); ok {
			t.Errorf("daemonLockOwner(%s) = %+v, true; want refused", body, o)
		}
	}
}

// S38 F1: while the account lock is held, a record that is refused is no
// "no other daemon": discovery answers with a diagnostic that names the
// file to inspect, so the CLI takes the daemon path and fails closed.
func TestOtherHomeRefusedRecordFailsClosed(t *testing.T) {
	home, selected := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": home, "HERDSMAN_PRIVATE_HOME": selected}
	getenv := func(k string) string { return env[k] }
	f, err := os.OpenFile(AccountLockPath(home), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(AccountLockPath(home), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Controls: the selected daemon's own record names no other home.
	write(`{"pid":42,"version":"1.0.0","start":1,"private_home":"` + selected + `","socket":"s"}` + "\n")
	if got := OtherHome(getenv); got != "" {
		t.Fatalf("own record: OtherHome = %q, want \"\"", got)
	}
	write(`{"pid":42,"version":"1.0.0","start":1,"private_home":"/other","socket":"s"}` + "\n")
	if got := OtherHome(getenv); !strings.Contains(got, "the running daemon uses /other") {
		t.Fatalf("other record: OtherHome = %q", got)
	}
	for name, body := range map[string]string{
		"unknown field": `{"pid":42,"version":"1.0.0","start":1,"private_home":"/other","extra":1}`,
		"duplicate key": `{"pid":42,"version":"1.0.0","start":1,"private_home":"/other","pid":7}`,
		"trailing data": `{"pid":42,"version":"1.0.0","start":1,"private_home":"/other"} {}`,
		"case variant":  `{"pid":42,"version":"1.0.0","start":1,"Private_Home":"/other"}`,
		"no pid":        `{"pid":0,"version":"1.0.0","start":1,"private_home":"/other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			write(body)
			got := OtherHome(getenv)
			want := "inspect " + AccountLockPath(home)
			if !strings.Contains(got, "the account lock is held but its record is refused") || !strings.Contains(got, want) {
				t.Fatalf("OtherHome = %q, want the refusal and %q", got, want)
			}
		})
	}
}

// S38 F1, round 2: a lock file that cannot be checked is not "not held",
// and a held record without a private home is not the selected home.
func TestOtherHomeIndeterminateFailsClosed(t *testing.T) {
	home, selected := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": home, "HERDSMAN_PRIVATE_HOME": selected}
	getenv := func(k string) string { return env[k] }
	want := "inspect " + AccountLockPath(home)
	if got := OtherHome(getenv); got != "" {
		t.Fatalf("no lock file: OtherHome = %q, want \"\"", got)
	}
	f, err := os.OpenFile(AccountLockPath(home), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	for name, body := range map[string]string{
		"no private_home":    `{"pid":42}`,
		"empty private_home": `{"pid":42,"private_home":""}`,
		"null private_home":  `{"pid":42,"private_home":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(AccountLockPath(home), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := OtherHome(getenv); !strings.Contains(got, want) {
				t.Fatalf("OtherHome = %q, want %q", got, want)
			}
		})
	}
	t.Run("unreadable lock file", func(t *testing.T) {
		if err := os.Chmod(AccountLockPath(home), 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(AccountLockPath(home), 0o600)
		if _, err := os.Open(AccountLockPath(home)); err == nil {
			t.Skip("mode 0000 does not stop this user from opening the file")
		}
		if got := OtherHome(getenv); !strings.Contains(got, want) {
			t.Fatalf("OtherHome = %q, want %q", got, want)
		}
	})
}
