package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// S16: `message status` with no daemon reads a contract 3 inbox's route
// database through the store's reader, which creates nothing.

const s16Epoch = "0123456789abcdef0123456789abcdef"

// s16Route makes a private home with route r, its inbox of contract 3 and
// the route database's path; with db it also writes a v1 database: an
// approved pair, one ready message for the orchestrator, a hold.
func s16Route(t *testing.T, db bool) (ph, dir, path string) {
	t.Helper()
	base, err := os.MkdirTemp("", "s16")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, _ = filepath.EvalSymlinks(base)
	ph, dir = filepath.Join(base, "ph"), filepath.Join(base, "inbox")
	path = filepath.Join(ph, "daemon", "routes", "r.db")
	for _, d := range []string{filepath.Dir(path), filepath.Join(dir, "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := encode(RoutesFile{Version: 1, Routes: map[string]RouteEntry{"r": {Inbox: dir, Repo: "repo", HerdrSocket: "/nonexistent", Created: 1}}})
	if err := os.WriteFile(filepath.Join(ph, "daemon", "routes.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	owner := `{"owner":"daemon","epoch":"` + s16Epoch + `","contract":3,"protocol":1,"adopted_from":null,"store":"` + path + `"}`
	if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	if db {
		s16DB(t, path)
	}
	return ph, dir, path
}

func s16DB(t *testing.T, path string) {
	t.Helper()
	c, err := store.Open(path, store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := store.InitRoute(c, s16Epoch); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at, approved_at) VALUES ('adj', 'adjutant', 'p1', '{"agent":"a"}', 1, 'approved', 1, 2)`,
		`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at, approved_at) VALUES ('orch', 'orchestrator', 'p2', '{"agent":"o"}', 1, 'approved', 1, 2)`,
		`INSERT INTO messages(id, sender, recipient, kind, envelope, body, body_sha256, state, created_seq, created_at, origin) VALUES (1, 'adj', 'orch', 'task', '{}', X'78', 'x', 'ready', 1, 1, 'daemon')`,
		`INSERT INTO holds(target, owner, reason, time) VALUES ('orch', 'adj', 'busy', 1)`,
	} {
		if _, err := c.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func s16Status(t *testing.T, ph string) (string, string, int) {
	t.Helper()
	env := map[string]string{"HERDSMAN_PRIVATE_HOME": ph, "HERDSMAN_ROUTE": "r", "HERDSMAN_NAME": "adj", "HERDR_PANE_ID": "p1"}
	var out, errb bytes.Buffer
	e := &MessageEnv{Getenv: func(k string) string { return env[k] }, Stdout: &out, Stderr: &errb, Version: testVersion}
	code := RunMessage(e, []string{"status"})
	return out.String(), errb.String(), code
}

// files lists every path under dir.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			out = append(out, p)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func TestOfflineStatusReadsTheRouteDB(t *testing.T) {
	ph, dir, path := s16Route(t, true)
	before := files(t, filepath.Dir(path))
	out, errOut, code := s16Status(t, ph)
	for _, want := range []string{
		"daemon=down heartbeat_age=",
		"route r epoch " + s16Epoch + " last_seq 0 status daemon-down\n",
		"registry approved \n",
		"peer adj role adjutant pane p1 session {\"agent\":\"a\"}\n",
		"peer orch role orchestrator pane p2 session {\"agent\":\"o\"}\n",
		"box orch new 0 ready 1 cur 0 unacked 1 quarantine 0 claim false\n", // ready+cur, as the live daemon and file mode count
		"hold orch by adj\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s%s", want, out, errOut)
		}
	}
	if code != 1 {
		t.Errorf("exit %d, want 1 (daemon down)", code)
	}
	if after := files(t, filepath.Dir(path)); !slices.Equal(before, after) {
		t.Errorf("the reader changed the database folder: %v -> %v", before, after)
	}
	if _, err := os.Stat(filepath.Join(dir, "peers.approved")); err == nil {
		t.Error("status wrote a file in the inbox")
	}
}

func TestOfflineStatusMissingDB(t *testing.T) {
	ph, _, path := s16Route(t, false)
	before := files(t, ph)
	out, errOut, code := s16Status(t, ph)
	if code != 1 || !strings.Contains(errOut, "route database "+path+" is missing") || !strings.Contains(out, "status store-missing") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if after := files(t, ph); !slices.Equal(before, after) {
		t.Fatalf("status created files: %v -> %v", before, after)
	}
}

func TestOfflineStatusRefusesNewerSchema(t *testing.T) {
	ph, _, path := s16Route(t, true)
	c, err := store.Open(path, store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	out, errOut, code := s16Status(t, ph)
	if code != 1 || !strings.Contains(errOut, "newer than this herdsman") || !strings.Contains(out, "status store-unreadable") || strings.Contains(out, "peer adj") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}
