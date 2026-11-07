package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// S16 fix 1: an owner record that names the route database but is refused
// is reported, never read as a file-mode inbox. Only no record, or a
// daemon record of contract 2 or lower, keeps the file reader.

// s16Owner is the contract 3 owner record with these fields in place of
// the valid ones (raw JSON values); an empty value drops the field.
func s16Owner(path string, fields map[string]string) string {
	f := map[string]string{"owner": `"daemon"`, "epoch": `"` + s16Epoch + `"`, "contract": "3", "protocol": "1",
		"adopted_from": "null", "store": `"` + path + `"`}
	for k, v := range fields {
		f[k] = v
	}
	var b strings.Builder
	b.WriteString("{")
	for i, k := range []string{"owner", "epoch", "contract", "protocol", "adopted_from", "store"} {
		if f[k] == "" {
			continue
		}
		if i > 0 && b.Len() > 1 {
			b.WriteString(",")
		}
		b.WriteString(`"` + k + `":` + f[k])
	}
	b.WriteString("}")
	return b.String()
}

// s16Refused lists the refused owner records of a route whose database is
// path: each one rewrites dir's state/owner.json. A contract 3 record has
// mode 600, as the database file has.
func s16Refused(t *testing.T, dir, path string) map[string]func() {
	t.Helper()
	owner := filepath.Join(dir, "state", "owner.json")
	write := func(s string) func() {
		return func() {
			if err := os.WriteFile(owner, []byte(s), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	routes := filepath.Dir(path)
	return map[string]func(){
		"relative store":  write(s16Owner(path, map[string]string{"store": `"daemon/routes/r.db"`})),
		"unclean store":   write(s16Owner(path, map[string]string{"store": `"` + filepath.Join(routes, ".") + `/./r.db"`})),
		"not a .db":       write(s16Owner(path, map[string]string{"store": `"` + filepath.Join(routes, "r.sqlite") + `"`})),
		"no store":        write(s16Owner(path, map[string]string{"store": ""})),
		"duplicate key":   write(strings.Replace(s16Owner(path, nil), `"contract":3`, `"contract":3,"contract":3`, 1)),
		"string contract": write(s16Owner(path, map[string]string{"contract": `"3"`})),
		"contract 4":      write(s16Owner(path, map[string]string{"contract": "4"})),
		"not json":        write(`{"owner":"daemon",`),
		"another home": write(s16Owner(path, map[string]string{"store": `"` +
			filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(routes))), "other", "daemon", "routes", "r.db") + `"`})),
		"group writable": func() {
			write(s16Owner(path, nil))()
			if err := os.Chmod(owner, 0o620); err != nil {
				t.Fatal(err)
			}
		},
		"mode 644": func() {
			write(s16Owner(path, nil))()
			if err := os.Chmod(owner, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func() {
			real := filepath.Join(filepath.Dir(dir), "owner-real.json")
			if err := os.WriteFile(real, []byte(s16Owner(path, nil)), 0o600); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(owner)
			if err := os.Symlink(real, owner); err != nil {
				t.Fatal(err)
			}
		},
	}
}

func TestOfflineStatusRefusedOwnerRecord(t *testing.T) {
	_, dir0, path0 := s16Route(t, false)
	for name := range s16Refused(t, dir0, path0) {
		t.Run(name, func(t *testing.T) {
			ph, dir, path := s16Route(t, true)
			s16Refused(t, dir, path)[name]()
			// A legacy mailbox the file reader would show.
			if err := os.MkdirAll(filepath.Join(dir, "orch", "new"), 0o700); err != nil {
				t.Fatal(err)
			}
			out, errOut, code := s16Status(t, ph)
			if code != 1 || !strings.Contains(out, "status owner-refused") || !strings.Contains(errOut, "owner record") ||
				strings.Contains(out, "peer adj") || strings.Contains(out, "box ") {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
			}
		})
	}
}
