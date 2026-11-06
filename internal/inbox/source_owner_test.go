package inbox

import (
	"os"
	"strings"
	"testing"
)

// TestSourceOwner: a source of another user, or one group or others can
// write, is refused; this user's private file is accepted.
func TestSourceOwner(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  int
		perm os.FileMode
		want string
	}{
		{"own private", os.Getuid(), 0o600, ""},
		{"own folder", os.Getuid(), 0o700 | os.ModeDir, ""},
		{"other owner", os.Getuid() + 1, 0o600, "not owned by this user"},
		{"group writable", os.Getuid(), 0o620, "writable by group or others"},
		{"others writable", os.Getuid(), 0o602, "writable by group or others"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := sourceOwner(tc.uid, tc.perm, "/x/state")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "/x/state") {
				t.Fatalf("%v, want %q naming the path", err, tc.want)
			}
		})
	}
}
