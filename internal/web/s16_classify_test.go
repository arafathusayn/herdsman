package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// otherRouteDB makes route b's database, with its own peer bob.
func otherRouteDB(t *testing.T, path string) string {
	t.Helper()
	b := filepath.Join(filepath.Dir(path), "b.db")
	c, err := store.Open(b, store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := store.InitRoute(c, s16Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at, approved_at) VALUES ('bob', 'adjutant', 'p9', '{}', 1, 'approved', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestReadChannelRouteFromRoutesFile: the inbox's route comes from the
// private home's routes.json, so an owner record that names another
// route's database, or an inbox no route lists, is refused and nothing of
// that database is shown.
func TestReadChannelRouteFromRoutesFile(t *testing.T) {
	t.Run("another route's database", func(t *testing.T) {
		_, dir, path := s16Route(t)
		b := otherRouteDB(t, path)
		if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(s16Owner(b, nil)), 0o600); err != nil {
			t.Fatal(err)
		}
		ch := readChannel(dir, time.Now())
		if !strings.HasPrefix(ch.Error, "owner record: ") || len(ch.Peers) != 0 || len(ch.Boxes) != 0 {
			t.Fatalf("error %q peers %+v boxes %+v", ch.Error, ch.Peers, ch.Boxes)
		}
	})
	t.Run("an inbox no route lists", func(t *testing.T) {
		base, dir, _ := s16Route(t)
		s16Routes(t, base, `{"r":{"inbox":"`+filepath.Join(base, "elsewhere")+`"}}`)
		ch := readChannel(dir, time.Now())
		if !strings.HasPrefix(ch.Error, "owner record: ") || !strings.Contains(ch.Error, "routes.json") || len(ch.Peers) != 0 {
			t.Fatalf("error %q peers %+v", ch.Error, ch.Peers)
		}
	})
}

// TestReadChannelStoreErrorNoHoldFiles: a contract 3 inbox whose route
// database cannot be read shows the error and none of the inbox's
// state/hold-* files, as the database's holds replace them.
func TestReadChannelStoreErrorNoHoldFiles(t *testing.T) {
	_, dir, path := s16Route(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "hold-orch"), []byte("adj\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ch := readChannel(dir, time.Now())
	if !strings.HasPrefix(ch.Error, "route database: ") {
		t.Fatalf("error %q", ch.Error)
	}
	for _, h := range ch.Holds {
		if strings.HasPrefix(h.Name, "hold-") {
			t.Errorf("hold file shown: %+v", ch.Holds)
		}
	}
}

// TestReadChannelRefusedReadsNothing: a refused owner record shows only
// its error: no file flags, no health, no hold files.
func TestReadChannelRefusedReadsNothing(t *testing.T) {
	_, dir, path := s16Route(t)
	if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(s16Owner(path, map[string]string{"contract": "4"})), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"automation-held", "route-paused", "hold-orch", "heartbeat", "watch-heartbeat"} {
		if err := os.WriteFile(filepath.Join(dir, "state", f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "timing.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ch := readChannel(dir, time.Now())
	if !strings.HasPrefix(ch.Error, "owner record: ") || len(ch.Flags) != 0 || len(ch.Health) != 0 || len(ch.Holds) != 0 {
		t.Fatalf("error %q flags %v health %+v holds %+v", ch.Error, ch.Flags, ch.Health, ch.Holds)
	}
}

// TestMessageBodyClassifies: the body endpoint serves an inbox's files
// only in file mode; a contract 3 inbox or a refused owner record serves
// none of them.
func TestMessageBodyClassifies(t *testing.T) {
	for _, c := range []struct{ name, contract string }{{"contract 3", "3"}, {"refused", "4"}} {
		t.Run(c.name, func(t *testing.T) {
			_, dir, path := s16Route(t)
			if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(s16Owner(path, map[string]string{"contract": c.contract})), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, "front", "ready", "m000000000002.md"), envelopeJSON("m000000000002", "boss", "front", "question", ""))
			if body, ok := verifiedBody(dir, "front", "m000000000002"); ok {
				t.Fatalf("served %q from the inbox's files", body)
			}
		})
	}
	root := newInbox(t)
	if body, ok := verifiedBody(root, "front", "m000000000002"); !ok || body != `{"version":1,"text":"hello"}` {
		t.Fatalf("file mode body: %q %v", body, ok)
	}
}
